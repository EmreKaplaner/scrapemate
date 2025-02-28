package scrapemateapp

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sync"
	"time"

	"github.com/EmreKaplaner/scrapemate"
	"golang.org/x/sync/errgroup"

	"github.com/EmreKaplaner/scrapemate/adapters/cache/filecache"
	"github.com/EmreKaplaner/scrapemate/adapters/cache/leveldbcache"
	jsfetcher "github.com/EmreKaplaner/scrapemate/adapters/fetchers/jshttp"
	fetcher "github.com/EmreKaplaner/scrapemate/adapters/fetchers/nethttp"
	"github.com/EmreKaplaner/scrapemate/adapters/fetchers/stealth"
	parser "github.com/EmreKaplaner/scrapemate/adapters/parsers/goqueryparser"
	memprovider "github.com/EmreKaplaner/scrapemate/adapters/providers/memory"
	"github.com/EmreKaplaner/scrapemate/adapters/proxy"
)

// RetryConfig holds configuration for retry operations
type RetryConfig struct {
	MaxAttempts     int
	InitialInterval time.Duration
	MaxInterval     time.Duration
	Multiplier      float64
}

// DefaultRetryConfig provides reasonable defaults for retries
var DefaultRetryConfig = RetryConfig{
	MaxAttempts:     3,
	InitialInterval: 100 * time.Millisecond,
	MaxInterval:     2 * time.Second,
	Multiplier:      2.0,
}

type ScrapemateApp struct {
	cfg *Config

	ctx    context.Context
	cancel context.CancelCauseFunc

	provider scrapemate.JobProvider
	cacher   scrapemate.Cacher
	fetcher  scrapemate.HTTPFetcher

	externalFetcher bool
	fetcherMutex    sync.Mutex
	retryConfig     RetryConfig

	// Health monitoring
	healthMutex sync.RWMutex
	healthy     bool
	lastError   error
}

// NewScrapeMateApp creates a new ScrapemateApp, optionally accepting an external fetcher.
func NewScrapeMateApp(cfg *Config, externalFetcher scrapemate.HTTPFetcher) (*ScrapemateApp, error) {
	app := ScrapemateApp{
		cfg:             cfg,
		fetcher:         externalFetcher,
		externalFetcher: externalFetcher != nil,
		retryConfig:     DefaultRetryConfig,
		healthy:         true,
	}

	return &app, nil
}

// IsHealthy returns the current health status of the app
func (app *ScrapemateApp) IsHealthy() (bool, error) {
	app.healthMutex.RLock()
	defer app.healthMutex.RUnlock()
	return app.healthy, app.lastError
}

// setHealth updates the health status of the app
func (app *ScrapemateApp) setHealth(healthy bool, err error) {
	app.healthMutex.Lock()
	defer app.healthMutex.Unlock()
	app.healthy = healthy
	app.lastError = err
}

// Start starts the app.
func (app *ScrapemateApp) Start(ctx context.Context, seedJobs ...scrapemate.IJob) error {
	g, ctx := errgroup.WithContext(ctx)
	ctx, cancel := context.WithCancelCause(ctx)

	defer cancel(errors.New("closing app"))
	app.cancel = cancel

	mate, err := app.getMate(ctx)
	if err != nil {
		app.setHealth(false, err)
		return err
	}

	app.setHealth(true, nil)
	defer app.Close()

	if !app.externalFetcher {
		defer mate.Close()
	}

	for i := range app.cfg.Writers {
		writer := app.cfg.Writers[i]

		g.Go(func() error {
			if err := writer.Run(ctx, mate.Results()); err != nil {
				app.setHealth(false, err)
				cancel(err)
				return err
			}

			return nil
		})
	}

	g.Go(func() error {
		return mate.Start()
	})

	g.Go(func() error {
		for i := range seedJobs {
			if err := app.provider.Push(ctx, seedJobs[i]); err != nil {
				app.setHealth(false, err)
				return err
			}
		}

		return nil
	})

	// Add health check monitoring goroutine
	g.Go(func() error {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				// Perform health check - check if cacher is accessible
				if app.cacher != nil {
					// Simple health check by trying to access a known key
					_, err := app.cacher.Get(ctx, "health_check")
					if err != nil && !errors.Is(err, os.ErrNotExist) {
						app.setHealth(false, err)
					} else {
						app.setHealth(true, nil)
					}
				}
			}
		}
	})

	return g.Wait()
}

// Close closes the app.
func (app *ScrapemateApp) Close() error {
	if app.cacher != nil {
		app.cacher.Close()
	}

	if app.fetcher != nil && !app.externalFetcher {
		return app.fetcher.Close()
	}

	return nil
}

// withRetry attempts an operation with retries based on the retry config
func (app *ScrapemateApp) withRetry(operation func() error) error {
	var err error
	backoff := app.retryConfig.InitialInterval

	for attempt := 0; attempt < app.retryConfig.MaxAttempts; attempt++ {
		err = operation()
		if err == nil {
			return nil
		}

		if attempt == app.retryConfig.MaxAttempts-1 {
			break
		}

		time.Sleep(backoff)
		backoff = time.Duration(float64(backoff) * app.retryConfig.Multiplier)
		if backoff > app.retryConfig.MaxInterval {
			backoff = app.retryConfig.MaxInterval
		}
	}

	return err
}

func (app *ScrapemateApp) getMate(ctx context.Context) (*scrapemate.ScrapeMate, error) {
	var err error

	// Initialize provider with retry
	err = app.withRetry(func() error {
		var providerErr error
		app.provider, providerErr = app.getProvider()
		return providerErr
	})

	if err != nil {
		return nil, err
	}

	// Initialize fetcher with retry and lock
	app.fetcherMutex.Lock()
	if app.fetcher == nil {
		err = app.withRetry(func() error {
			var fetcherErr error
			app.fetcher, fetcherErr = app.getFetcher()
			return fetcherErr
		})

		if err != nil {
			app.fetcherMutex.Unlock()
			return nil, err
		}
	}
	app.fetcherMutex.Unlock()

	// Initialize cacher with retry
	err = app.withRetry(func() error {
		var cacherErr error
		app.cacher, cacherErr = app.getCacher()
		return cacherErr
	})

	if err != nil {
		// Continue without cache if it fails - fallback strategy
		app.cacher = nil
	}

	params := []func(*scrapemate.ScrapeMate) error{
		scrapemate.WithContext(ctx, app.cancel),
		scrapemate.WithJobProvider(app.provider),
		scrapemate.WithHTTPFetcher(app.fetcher),
		scrapemate.WithHTMLParser(parser.New()),
		scrapemate.WithConcurrency(app.cfg.Concurrency),
		scrapemate.WithExitBecauseOfInactivity(app.cfg.ExitOnInactivityDuration),
	}

	if app.cacher != nil {
		params = append(params, scrapemate.WithCache(app.cacher))
	}

	if app.cfg.InitJob != nil {
		params = append(params, scrapemate.WithInitJob(app.cfg.InitJob))
	}

	return scrapemate.New(params...)
}

func (app *ScrapemateApp) getCacher() (scrapemate.Cacher, error) {
	var (
		cacher scrapemate.Cacher
		err    error
	)

	switch app.cfg.CacheType {
	case "file":
		cacher, err = filecache.NewFileCache(app.cfg.CachePath)
	case "leveldb":
		cacher, err = leveldbcache.NewLevelDBCache(app.cfg.CachePath)
	}

	return cacher, err
}

func (app *ScrapemateApp) getFetcher() (scrapemate.HTTPFetcher, error) {
	var (
		httpFetcher scrapemate.HTTPFetcher
		err         error
		rotator     scrapemate.ProxyRotator
	)

	if len(app.cfg.Proxies) > 0 {
		rotator = proxy.New(app.cfg.Proxies)
	}

	const timeout = 10 * time.Second

	switch app.cfg.UseJS {
	case true:
		jsParams := jsfetcher.JSFetcherOptions{
			Headless:          !app.cfg.JSOpts.Headfull,
			DisableImages:     app.cfg.JSOpts.DisableImages,
			Rotator:           rotator,
			PoolSize:          app.cfg.Concurrency,
			PageReuseLimit:    app.cfg.PageReuseLimit,
			BrowserReuseLimit: app.cfg.BrowserReuseLimit,
			UserAgent:         app.cfg.JSOpts.UA,
		}

		httpFetcher, err = jsfetcher.New(jsParams)
		if err != nil {
			return nil, err
		}
	default:
		if app.cfg.UseStealth {
			httpFetcher = stealth.New(app.cfg.StealthBrowser)
		} else {
			cookieJar, err := cookiejar.New(nil)
			if err != nil {
				return nil, err
			}

			netClient := &http.Client{
				Timeout: timeout,
				Jar:     cookieJar,
			}

			if rotator != nil {
				netClient.Transport = rotator
			}

			httpFetcher = fetcher.New(netClient)
		}
	}

	return httpFetcher, nil
}

//nolint:unparam // this function returns always nil error
func (app *ScrapemateApp) getProvider() (scrapemate.JobProvider, error) {
	var provider scrapemate.JobProvider

	switch app.cfg.Provider {
	case nil:
		provider = memprovider.New()
	default:
		provider = app.cfg.Provider
	}

	return provider, nil
}
