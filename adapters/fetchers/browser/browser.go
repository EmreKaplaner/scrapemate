package browser

import (
	"context"
	"time"

	"github.com/EmreKaplaner/scrapemate"
)

// BrowserFetcher implements a fetcher using headless browsers
type BrowserFetcher struct {
	browserType string
	headless    bool
	timeout     time.Duration
	browserPool *BrowserPool
}

// BrowserOptions represents options for the browser fetcher
type BrowserOptions struct {
	BrowserType string
	Headless    bool
	Timeout     time.Duration
	PoolSize    int
}

// New creates a new browser fetcher
func New(options BrowserOptions) (scrapemate.HTTPFetcher, error) {
	if options.Timeout == 0 {
		options.Timeout = 30 * time.Second
	}
	if options.PoolSize == 0 {
		options.PoolSize = 1
	}
	if options.BrowserType == "" {
		options.BrowserType = "chrome"
	}

	pool, err := NewBrowserPool(options.BrowserType, options.PoolSize, options.Headless)
	if err != nil {
		return nil, err
	}

	return &BrowserFetcher{
		browserType: options.BrowserType,
		headless:    options.Headless,
		timeout:     options.Timeout,
		browserPool: pool,
	}, nil
}

func (b *BrowserFetcher) Fetch(ctx context.Context, job scrapemate.IJob) scrapemate.Response {
	browser, err := b.browserPool.Get(ctx)
	if err != nil {
		return scrapemate.Response{
			Error: err,
		}
	}
	defer b.browserPool.Put(browser)

	// Minimal example
	return scrapemate.Response{
		StatusCode: 200,
	}
}

func (b *BrowserFetcher) Close() error {
	return b.browserPool.Close()
}
