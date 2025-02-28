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

// Global variables for shared Playwright initialization.
var (
	pwInitOnce  sync.Once
	globalPW    *playwright.Playwright
	globalPWErr error
)

// getGlobalPlaywright initializes and returns the global Playwright instance once.
func getGlobalPlaywright() (*playwright.Playwright, error) {
	pwInitOnce.Do(func() {
		// Perform the install ONCE
		if err := playwright.Install(); err != nil {
			globalPWErr = fmt.Errorf("failed to install playwright: %w", err)
			return
		}

		// Launch Playwright ONCE
		pw, err := playwright.Run()
		if err != nil {
			globalPWErr = fmt.Errorf("failed to run playwright: %w", err)
			return
		}
		globalPW = pw
	})

	return globalPW, globalPWErr
}

// New creates a JS-based fetcher using a globally shared Playwright instance.
// It doesn't repeatedly install or launch Playwright—those happen exactly once.
func New(params JSFetcherOptions) (scrapemate.HTTPFetcher, error) {
	pw, err := getGlobalPlaywright()
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

// GetBrowser retrieves or creates a browser from the pool.
// If a browser from the pool exceeds usage limits or is disconnected,
// it closes that one and spawns a fresh browser.
func (o *jsFetch) GetBrowser(ctx context.Context) (*browser, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()

	case br := <-o.pool:
		if br.browser.IsConnected() &&
			(o.browserReuseLimit <= 0 || br.browserUsage < o.browserReuseLimit) {
			return br, nil
		}
		// Otherwise, close and create a fresh browser
		br.Close()

	default:
	}
	// No browsers in pool or usage limit exceeded => create a new one
	return newBrowser(o.pw, o.headless, o.disableImages, o.rotator, o.ua)
}

// Close implements the scrapemate.HTTPFetcher interface.
// It closes all browsers in the pool but does not stop the global Playwright instance.
func (o *jsFetch) Close() error {
	close(o.pool)
	for b := range o.pool {
		b.Close()
	}
	// Note: We do NOT stop the global Playwright instance here,
	// because it's meant to be used globally and typically closed at app shutdown.
	return nil
}

// PutBrowser returns a browser to the pool (or closes it if the pool is full or the context is done).
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
		// If the pool is full, close this browser
		b.Close()
	}
}

// Fetch acquires a browser context from the pool, does the job's BrowserActions, then returns the result.
func (o *jsFetch) Fetch(ctx context.Context, job scrapemate.IJob) scrapemate.Response {
	br, err := o.GetBrowser(ctx)
	if err != nil {
		return scrapemate.Response{Error: err}
	}
	defer o.PutBrowser(ctx, br)

	// If the job has a timeout, apply it
	if job.GetTimeout() > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, job.GetTimeout())
		defer cancel()
	}

	// Reuse the existing page if available; else create one
	pages := br.ctx.Pages()
	var page playwright.Page
	if len(pages) > 0 {
		page = pages[0]
		// Close extra pages
		for i := 1; i < len(pages); i++ {
			_ = pages[i].Close()
		}
	} else {
		page, err = br.ctx.NewPage()
		if err != nil {
			return scrapemate.Response{Error: err}
		}
	}

	// If job has a page-level timeout
	if job.GetTimeout() > 0 {
		page.SetDefaultTimeout(float64(job.GetTimeout().Milliseconds()))
	}

	// Track usage
	br.page0Usage++
	br.browserUsage++

	// If we've exceeded page reuse limit, close after use
	defer func() {
		if o.pageReuseLimit > 0 && br.page0Usage >= o.pageReuseLimit {
			_ = page.Close()
			br.page0Usage = 0
		}
	}()

	// 1) Let the job run its browser actions
	resp := job.BrowserActions(ctx, page)

	// 2) If there's an immediate error, handle it
	if resp.Error != nil {
		if strings.Contains(resp.Error.Error(), "Timeout") ||
			strings.Contains(resp.Error.Error(), "Navigation") {
			log.Printf("Navigation error during job.BrowserActions: %v", resp.Error)
		}
		return resp
	}

	// 3) Optionally read final content
	content, contentErr := page.Content()
	if contentErr != nil {
		return scrapemate.Response{Error: contentErr}
	}

	// 4) Look for CAPTCHAs in the final HTML
	if detectCaptcha(content) {
		log.Println("CAPTCHA detected (no MarkBad call, just logging).")
		return scrapemate.Response{Error: errors.New("captcha detected")}
	}

	// 5) Check if document is fully loaded
	readyState, _ := page.Evaluate(`() => document.readyState`)
	if readyState != "complete" {
		return scrapemate.Response{Error: errors.New("page not fully loaded")}
	}

	// 6) If the job's response is a 403, it might be a block
	if resp.StatusCode == http.StatusForbidden {
		log.Println("Access forbidden, possible block or captcha. (no MarkBad call)")
		return scrapemate.Response{Error: errors.New("access forbidden")}
	}

	// 7) Final response adjustments
	if len(resp.Body) == 0 {
		resp.Body = []byte(content)
	}
	if resp.StatusCode == 0 {
		resp.StatusCode = http.StatusOK
	}
	return resp
}

// browser wraps a single Browser + BrowserContext
type browser struct {
	browser      playwright.Browser
	ctx          playwright.BrowserContext
	page0Usage   int
	browserUsage int
	currentProxy *scrapemate.Proxy
}

// Close closes both the context and the underlying browser.
func (o *browser) Close() {
	_ = o.ctx.Close()
	_ = o.browser.Close()
}

// newBrowser creates a new Browser+Context pair with optional proxy usage.
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
		next := rotator.Next()
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

// detectCaptcha scans page content for common CAPTCHA triggers.
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

// JSHTTPFetcher is a stub that simulates a JS-based environment (Rod, etc).
// Not typically used in your final code but left for completeness.
type JSHTTPFetcher struct {
	mu          sync.Mutex
	headless    bool
	initialized bool
	browser     interface{}
	settings    map[string]interface{}
}

// NewJSHTTPFetcher creates a new stub JSHTTPFetcher.
func NewJSHTTPFetcher(headless bool) *JSHTTPFetcher {
	return &JSHTTPFetcher{
		headless: headless,
		settings: make(map[string]interface{}),
	}
}

// Fetch is a stub fetch example.
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
	// Pretend to launch a headless engine here
	j.browser = struct{}{}
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
