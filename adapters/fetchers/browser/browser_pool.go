package browser

import (
	"context"
	"fmt"
	"sync"
)

// BrowserMock is a stand-in for an actual browser instance
type BrowserMock struct {
	ID int
}

// Close is a mock method for closing the browser instance
func (b *BrowserMock) Close() error {
	return nil
}

// BrowserPool manages a pool of browser instances
type BrowserPool struct {
	browserType string
	headless    bool
	size        int

	mu       sync.Mutex
	browsers []*BrowserMock
	counter  int
}

// NewBrowserPool creates and initializes the browser pool
func NewBrowserPool(browserType string, size int, headless bool) (*BrowserPool, error) {
	bp := &BrowserPool{
		browserType: browserType,
		headless:    headless,
		size:        size,
		browsers:    make([]*BrowserMock, 0, size),
	}

	// In a real implementation, you'd launch actual browsers here.
	// For now, just create some mock browser objects.
	for i := 0; i < size; i++ {
		bp.browsers = append(bp.browsers, &BrowserMock{ID: i})
	}
	return bp, nil
}

// Get returns a browser instance from the pool
func (bp *BrowserPool) Get(ctx context.Context) (*BrowserMock, error) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	if len(bp.browsers) == 0 {
		return nil, fmt.Errorf("no browser available")
	}

	// Just grab the last one in the slice for simplicity
	br := bp.browsers[len(bp.browsers)-1]
	bp.browsers = bp.browsers[:len(bp.browsers)-1]
	return br, nil
}

// Put returns a browser instance to the pool
func (bp *BrowserPool) Put(br *BrowserMock) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bp.browsers = append(bp.browsers, br)
}

// Close closes all browsers in the pool
func (bp *BrowserPool) Close() error {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	for _, b := range bp.browsers {
		_ = b.Close()
	}
	bp.browsers = nil
	return nil
}
