// File: adapters/fetchers/jshttp/jshttp.go

package jshttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

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
	// Install the required browsers, if needed
	opts := []*playwright.RunOptions{
		{
			Browsers: []string{"chromium"},
			Verbose:  true, // Keep verbose logging for diagnosing issues
		},
	}
	if err := playwright.Install(opts...); err != nil {
		return nil, err
	}

	// Launch the Playwright driver
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

	// Pre-launch a pool of browser contexts
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
		// If the browser is still connected and under usage limit, reuse it
		if ans.browser.IsConnected() &&
			(o.browserReuseLimit <= 0 || ans.browserUsage < o.browserReuseLimit) {
			return ans, nil
		}
		// Otherwise, close and create a fresh browser
		ans.Close()

	default:
	}
	// No browsers available, or usage limit exceeded => create a new one
	return newBrowser(
		o.pw,
		o.headless,
		o.disableImages,
		o.rotator,
		o.ua,
	)
}

// Close implements the scrapemate.HTTPFetcher interface
func (o *jsFetch) Close() error {
	// Close out the pool, then close each browser context
	close(o.pool)
	for b := range o.pool {
		b.Close()
	}
	// Finally stop Playwright
	return o.pw.Stop()
}

// PutBrowser returns a browser to the pool (or closes it if the pool is full or context canceled)
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
	// Acquire a browser context from the pool
	browser, err := o.GetBrowser(ctx)
	if err != nil {
		return scrapemate.Response{Error: err}
	}
	defer o.PutBrowser(ctx, browser)

	// Honor job's timeout if provided
	if job.GetTimeout() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, job.GetTimeout())
		defer cancel()
	}

	// Reuse the existing page if available, otherwise create a new one
	pages := browser.ctx.Pages()
	var page playwright.Page
	if len(pages) > 0 {
		page = pages[0]
		// Close any extra pages to avoid confusion
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

	// Increase usage counters
	browser.page0Usage++
	browser.browserUsage++

	// If we've exceeded the page reuse limit, close the page after use
	defer func() {
		if o.pageReuseLimit > 0 && browser.page0Usage >= o.pageReuseLimit {
			_ = page.Close()
			browser.page0Usage = 0
		}
	}()

	// 1) Actually let the job do its browser actions: navigation, scraping, etc.
	resp := job.BrowserActions(ctx, page)

	// 2) If there's an error from the job's actions, return immediately
	if resp.Error != nil {
		// If it's a typical navigation/timeout error, log it
		if strings.Contains(resp.Error.Error(), "Timeout") ||
			strings.Contains(resp.Error.Error(), "Navigation") {
			log.Printf("Navigation error during job.BrowserActions: %v", resp.Error)
		}
		return resp
	}

	// 3) Final content read to detect CAPTCHAs or readiness
	content, contentErr := page.Content()
	if contentErr != nil {
		return scrapemate.Response{Error: contentErr}
	}

	// 4) Detect CAPTCHAs in final HTML
	if detectCaptcha(content) {
		log.Println("CAPTCHA detected (no MarkBad call, just logging).")
		return scrapemate.Response{Error: errors.New("captcha detected")}
	}

	// 5) Check if page is fully loaded
	readyState, _ := page.Evaluate(`() => document.readyState`)
	if readyState != "complete" {
		return scrapemate.Response{Error: errors.New("page not fully loaded")}
	}

	// 6) If we see an HTTP 403 status in the job’s response, treat it as a potential block
	if resp.StatusCode == http.StatusForbidden {
		log.Println("Access forbidden, possible block or captcha. (no MarkBad call)")
		return scrapemate.Response{Error: errors.New("access forbidden")}
	}

	// 7) If the job hasn't filled Body, let's add final content
	if len(resp.Body) == 0 {
		resp.Body = []byte(content)
	}
	if resp.StatusCode == 0 {
		resp.StatusCode = http.StatusOK
	}

	return resp
}

// browser is a wrapper around one Playwright Browser + BrowserContext
type browser struct {
	browser      playwright.Browser
	ctx          playwright.BrowserContext
	page0Usage   int
	browserUsage int
	currentProxy *scrapemate.Proxy
}

// Close closes both the context and the underlying browser
func (o *browser) Close() {
	_ = o.ctx.Close()
	_ = o.browser.Close()
}

// newBrowser creates a brand-new Browser with the proxy at launch time
func newBrowser(
	pw *playwright.Playwright,
	headless bool,
	disableImages bool,
	rotator scrapemate.ProxyRotator,
	ua string,
) (*browser, error) {

	var proxy *playwright.Proxy
	var currentProxy *scrapemate.Proxy

	// If a rotator is present, pick the next proxy
	if rotator != nil {
		next := rotator.Next() // returns scrapemate.Proxy
		currentProxy = &next
		proxy = &playwright.Proxy{
			Server:   next.URL,
			Username: playwright.String(next.Username),
			Password: playwright.String(next.Password),
		}

		log.Printf("Launching browser with proxy: %s, user: %s", next.URL, next.Username)
	}

	args := []string{"--start-maximized"}
	if disableImages {
		args = append(args, "--blink-settings=imagesEnabled=false")
	}

	opts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(headless),
		Proxy:    proxy,
		Args:     args,
	}

	log.Printf("[DEBUG] Launching browser: headless=%t, proxy=%v, args=%v",
		headless, proxy, args)

	br, err := pw.Chromium.Launch(opts)
	if err != nil {
		return nil, err
	}

	bctxOpts := playwright.BrowserNewContextOptions{
		UserAgent: playwright.String(func() string {
			if ua == "" {
				return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
					"AppleWebKit/537.36 (KHTML, like Gecko) " +
					"Chrome/91.0.4472.124 Safari/537.36"
			}
			return ua
		}()),
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
		browser:      br,
		ctx:          ctx,
		currentProxy: currentProxy,
	}, nil
}

// detectCaptcha scans page content for common CAPTCHA triggers
func detectCaptcha(content string) bool {
	captchaIndicators := []string{"captcha", "i'm not a robot", "verify you're human"}
	lc := strings.ToLower(content)
	for _, indicator := range captchaIndicators {
		if strings.Contains(lc, indicator) {
			return true
		}
	}
	return false
}

// JSHTTPFetcher simulates or actually uses a JS-capable engine (like Playwright, Rod, etc.)
type JSHTTPFetcher struct {
	mu          sync.Mutex
	headless    bool
	initialized bool
	browser     interface{}            // represent your actual browser/driver
	settings    map[string]interface{} // any custom settings
}

// NewJSHTTPFetcher creates a new JSHTTPFetcher with optional settings.
func NewJSHTTPFetcher(headless bool) *JSHTTPFetcher {
	return &JSHTTPFetcher{
		headless: headless,
		settings: make(map[string]interface{}),
	}
}

// Fetch attempts to fetch using a JavaScript-capable environment (stub).
func (j *JSHTTPFetcher) Fetch(ctx context.Context, job scrapemate.IJob) scrapemate.Response {
	j.mu.Lock()
	if !j.initialized {
		if err := j.initBrowser(); err != nil {
			j.mu.Unlock()
			return scrapemate.Response{Error: fmt.Errorf("failed to init browser: %w", err)}
		}
		j.initialized = true
	}
	j.mu.Unlock()

	// Minimal example
	return scrapemate.Response{
		StatusCode: http.StatusOK,
		URL:        job.GetFullURL(),
		Headers:    http.Header{},
		Body:       []byte("<html><body>Hello from JSHTTP!</body></html>"),
	}
}

func (j *JSHTTPFetcher) initBrowser() error {
	if j.browser != nil {
		return nil
	}
	j.browser = struct{}{} // pretend to launch a headless engine
	return nil
}

func (j *JSHTTPFetcher) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.browser == nil {
		return errors.New("browser not initialized")
	}
	j.browser = nil
	j.initialized = false
	return nil
}
