// File: adapters/fetchers/jshttp/jshttp.go

package jshttp

import (
	"context"
	"log"
	"strings"

	"github.com/EmreKaplaner/scrapemate"
	"github.com/playwright-community/playwright-go"
)

// Ensure jsFetch implements the scrapemate.HTTPFetcher interface
var _ scrapemate.HTTPFetcher = (*jsFetch)(nil)

// JSFetcherOptions is the input config for the JS fetcher
type JSFetcherOptions struct {
	// Headless indicates whether the browser should run without a visible UI
	Headless bool

	// DisableImages indicates whether to disable loading images for performance
	DisableImages bool

	// Rotator provides a mechanism to rotate proxies for each new browser
	Rotator scrapemate.ProxyRotator

	// PoolSize is how many browser instances to launch in parallel
	PoolSize int

	// PageReuseLimit is how many times the same page is reused before closing
	PageReuseLimit int

	// BrowserReuseLimit is how many times the same browser is reused before closing
	BrowserReuseLimit int

	// UserAgent if set, overrides the default browser UA string
	UserAgent string
}

// New creates a JS-based fetcher using Playwright as the engine
func New(params JSFetcherOptions) (scrapemate.HTTPFetcher, error) {
	// You can optionally specify a custom RunOptions or channel:
	opts := []*playwright.RunOptions{
		{
			Browsers: []string{"chromium"},
			Verbose:  true, // We'll keep verbose logging for diagnosing issues
		},
	}

	// Install browsers if needed
	if err := playwright.Install(opts...); err != nil {
		return nil, err
	}

	pw, err := playwright.Run()
	if err != nil {
		return nil, err
	}

	// Construct the main jsFetch instance
	ans := jsFetch{
		pw:                pw,
		headless:          params.Headless,
		disableImages:     params.DisableImages,
		pool:              make(chan *browser, params.PoolSize),
		rotator:           params.Rotator,
		pageReuseLimit:    params.PageReuseLimit,
		browserReuseLimit: params.BrowserReuseLimit,
		ua:                params.UserAgent,
	}

	// Fill the pool with pre-launched browsers
	for i := 0; i < params.PoolSize; i++ {
		b, err := newBrowser(
			pw,
			params.Headless,
			params.DisableImages,
			params.Rotator,
			params.UserAgent,
		)
		if err != nil {
			_ = ans.Close()
			return nil, err
		}
		ans.pool <- b
	}

	return &ans, nil
}

// jsFetch is the main struct implementing HTTPFetcher
type jsFetch struct {
	pw                *playwright.Playwright
	headless          bool
	disableImages     bool
	pool              chan *browser
	rotator           scrapemate.ProxyRotator
	pageReuseLimit    int
	browserReuseLimit int
	ua                string
}

// GetBrowser retrieves or creates a browser from the pool
func (o *jsFetch) GetBrowser(ctx context.Context) (*browser, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case ans := <-o.pool:
		// If browser is connected and under usage limit, reuse it
		if ans.browser.IsConnected() &&
			(o.browserReuseLimit <= 0 || ans.browserUsage < o.browserReuseLimit) {
			return ans, nil
		}
		// Otherwise close and create a new one
		ans.browser.Close()
	default:
	}

	// No browsers available in the pool, or they exceeded usage => create new
	return newBrowser(o.pw, o.headless, o.disableImages, o.rotator, o.ua)
}

// Close implements the scrapemate.HTTPFetcher interface
func (o *jsFetch) Close() error {
	// Close out the pool
	close(o.pool)
	for b := range o.pool {
		b.Close()
	}
	// Stop Playwright
	_ = o.pw.Stop()
	return nil
}

// PutBrowser returns a browser to the pool (or closes it if the pool is full)
func (o *jsFetch) PutBrowser(ctx context.Context, b *browser) {
	if !b.browser.IsConnected() {
		b.Close()
		return
	}
	select {
	case <-ctx.Done():
		b.Close()
	case o.pool <- b:
	default:
		b.Close()
	}
}

// Fetch fetches the given job (URL) in a Playwright browser/page
func (o *jsFetch) Fetch(ctx context.Context, job scrapemate.IJob) scrapemate.Response {
	browser, err := o.GetBrowser(ctx)
	if err != nil {
		return scrapemate.Response{Error: err}
	}
	defer o.PutBrowser(ctx, browser)

	// Apply job's custom timeout if set
	if job.GetTimeout() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, job.GetTimeout())
		defer cancel()
	}

	// Reuse or create a new page in the existing context
	var page playwright.Page
	pages := browser.ctx.Pages()
	if len(pages) > 0 {
		page = pages[0]
		// Close any extra pages
		for i := 1; i < len(pages); i++ {
			_ = pages[i].Close()
		}
	} else {
		page, err = browser.ctx.NewPage()
		if err != nil {
			return scrapemate.Response{Error: err}
		}
	}

	// Match page timeout to job
	if job.GetTimeout() > 0 {
		page.SetDefaultTimeout(float64(job.GetTimeout().Milliseconds()))
	}

	// Track usage
	browser.page0Usage++
	browser.browserUsage++

	// If we've exceeded the reuse limit, close the page after use
	defer func() {
		if o.pageReuseLimit == 0 || browser.page0Usage >= o.pageReuseLimit {
			_ = page.Close()
			browser.page0Usage = 0
		}
	}()

	// Actually navigate/process via the job's BrowserActions
	return job.BrowserActions(ctx, page)
}

// browser is a wrapper around one Playwright Browser + BrowserContext
type browser struct {
	browser      playwright.Browser
	ctx          playwright.BrowserContext
	page0Usage   int
	browserUsage int
}

// Close closes both the context and the underlying browser
func (o *browser) Close() {
	_ = o.ctx.Close()
	_ = o.browser.Close()
}

// newBrowser creates a brand-new Browser with the proxy at launch time
func newBrowser(
	pw *playwright.Playwright,
	headless, disableImages bool,
	rotator scrapemate.ProxyRotator,
	ua string,
) (*browser, error) {

	// If we have a rotator, pick the next proxy for this launch
	var launchProxy *playwright.Proxy
	var argProxyServer string
	if rotator != nil {
		next := rotator.Next()
		launchProxy = &playwright.Proxy{
			Server:   next.URL, // e.g. "http://user:pass@host:port"
			Username: playwright.String(next.Username),
			Password: playwright.String(next.Password),
		}

		// Fallback approach: also pass --proxy-server in command-line arg
		// in case the built-in Proxy field fails
		// If next.URL includes user:pass, might need to parse/clean it.
		if strings.HasPrefix(next.URL, "http") {
			argProxyServer = "--proxy-server=" + next.URL
		}
	}

	// Build launch options
	args := []string{
		"--start-maximized",
		// Minimal flags—remove advanced ones that might break proxy handshake
		// or re-add them one by one as needed.
	}

	if argProxyServer != "" {
		args = append(args, argProxyServer)
		log.Printf("[DEBUG] Using fallback CLI proxy arg: %s", argProxyServer)
	}

	if disableImages {
		args = append(args, "--blink-settings=imagesEnabled=false")
	}

	// Headful or headless
	opts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
		Proxy:    launchProxy,
		Args:     args,
	}

	log.Printf("[DEBUG] Launching browser: headless=%t, proxy=%v, args=%v",
		headless, launchProxy, args,
	)

	br, err := pw.Chromium.Launch(opts)
	if err != nil {
		return nil, err
	}

	// Create a BrowserContext WITHOUT specifying a proxy
	// (Playwright should use the proxy from the Browser-level config)
	bctxOpts := playwright.BrowserNewContextOptions{
		UserAgent: func() *string {
			if ua == "" {
				defUA := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
					"AppleWebKit/537.36 (KHTML, like Gecko) " +
					"Chrome/91.0.4472.124 Safari/537.36"
				return &defUA
			}
			return &ua
		}(),
		// Example: headful mode can have a big window
		Viewport: &playwright.Size{
			Width:  1280,
			Height: 800,
		},
	}

	ctx, err := br.NewContext(bctxOpts)
	if err != nil {
		_ = br.Close()
		return nil, err
	}

	return &browser{
		browser: br,
		ctx:     ctx,
	}, nil
}
