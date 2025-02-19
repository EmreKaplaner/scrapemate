// File: adapters/fetchers/jshttp/jshttp.go
package jshttp

import (
	"context"

	"github.com/EmreKaplaner/scrapemate"
	"github.com/playwright-community/playwright-go"
)

// Ensure jsFetch implements the scrapemate.HTTPFetcher interface
var _ scrapemate.HTTPFetcher = (*jsFetch)(nil)

// JSFetcherOptions is the input config for the JS fetcher
type JSFetcherOptions struct {
	Headless          bool
	DisableImages     bool
	Rotator           scrapemate.ProxyRotator
	PoolSize          int
	PageReuseLimit    int
	BrowserReuseLimit int
	UserAgent         string
}

// New creates a JS-based fetcher using Playwright as the engine
func New(params JSFetcherOptions) (scrapemate.HTTPFetcher, error) {
	opts := []*playwright.RunOptions{
		{
			Browsers: []string{"chromium"},
			Verbose:  true,
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

// GetBrowser tries to take a browser from the pool, or creates a new one if needed
func (o *jsFetch) GetBrowser(ctx context.Context) (*browser, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case ans := <-o.pool:
		// If browser is connected and under usage limit, reuse it
		if ans.browser.IsConnected() && (o.browserReuseLimit <= 0 || ans.browserUsage < o.browserReuseLimit) {
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

// Fetch fetches the given job (URL) in a Playwright browser/page and returns the Response
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

// newBrowser creates a brand-new Browser with the proxy at launch time,
// then creates a single BrowserContext (no proxy at the context level).
func newBrowser(
	pw *playwright.Playwright,
	headless, disableImages bool,
	rotator scrapemate.ProxyRotator,
	ua string,
) (*browser, error) {

	// If we have a rotator, pick the next proxy for this launch
	var launchProxy *playwright.Proxy
	if rotator != nil {
		next := rotator.Next()
		launchProxy = &playwright.Proxy{
			Server:   next.URL, // e.g. "http://username:password@proxyhost:port"
			Username: playwright.String(next.Username),
			Password: playwright.String(next.Password),
		}
	}

	// Launch the Chromium browser with the proxy at the LAUNCH level
	opts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
		Proxy:    launchProxy, // <-- Instead of BrowserTypeLaunchOptionsProxy
		Args: []string{
			"--start-maximized",
			"--no-default-browser-check",
			"--disable-dev-shm-usage",
			"--no-sandbox",
			"--disable-setuid-sandbox",
			"--no-zygote",
			"--disable-gpu",
			"--mute-audio",
			"--disable-extensions",
			"--single-process",
			"--disable-breakpad",
			"--disable-features=TranslateUI,BlinkGenPropertyTrees",
			"--disable-ipc-flooding-protection",
			"--enable-features=NetworkService,NetworkServiceInProcess",
			"--enable-features=NetworkService",
			"--disable-default-apps",
			"--disable-notifications",
			"--disable-webgl",
			"--disable-blink-features=AutomationControlled",
			"--ignore-certificate-errors",
			"--ignore-certificate-errors-spki-list",
			"--disable-web-security",
		},
	}
	if disableImages {
		opts.Args = append(opts.Args, "--blink-settings=imagesEnabled=false")
	}

	br, err := pw.Chromium.Launch(opts)
	if err != nil {
		return nil, err
	}

	// Create a BrowserContext WITHOUT specifying a proxy
	const defaultWidth, defaultHeight = 1920, 1080

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
		Viewport: &playwright.Size{
			Width:  defaultWidth,
			Height: defaultHeight,
		},
		// No Proxy set at context level
	}

	bctx, err := br.NewContext(bctxOpts)
	if err != nil {
		_ = br.Close()
		return nil, err
	}

	return &browser{
		browser: br,
		ctx:     bctx,
	}, nil
}
