package browser

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// BrowserMock is a stand-in for an actual browser instance
type BrowserMock struct {
	ID         int
	healthy    bool
	usageCount int
}

// Close is a mock method for closing the browser instance
func (b *BrowserMock) Close() error {
	return nil
}

// Add IsHealthy and Reset methods
func (b *BrowserMock) IsHealthy() bool {
	return b.healthy
}

func (b *BrowserMock) Reset() {
	b.usageCount = 0
	b.healthy = true
	log.Printf("Browser %d reset; usageCount=%d", b.ID, b.usageCount)
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

const maxUsagePerBrowser = 5

// NewBrowserPool creates and initializes the browser pool
func NewBrowserPool(browserType string, size int, headless bool) (*BrowserPool, error) {
	bp := &BrowserPool{
		browserType: browserType,
		headless:    headless,
		size:        size,
		browsers:    make([]*BrowserMock, 0, size),
	}
	for i := 0; i < size; i++ {
		bp.browsers = append(bp.browsers, &BrowserMock{
			ID:      i,
			healthy: true,
		})
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
	br := bp.browsers[len(bp.browsers)-1]
	bp.browsers = bp.browsers[:len(bp.browsers)-1]

	// Increment usage and reset if threshold exceeded
	br.usageCount++
	if br.usageCount > maxUsagePerBrowser {
		log.Printf("Browser %d exceeded usage threshold, resetting", br.ID)
		br.Reset()
	}

	return br, nil
}

// Put returns a browser instance to the pool
func (bp *BrowserPool) Put(br *BrowserMock) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bp.browsers = append(bp.browsers, br)
}

// Periodic browser health check
func (bp *BrowserPool) CheckBrowserHealth() {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	for _, b := range bp.browsers {
		if !b.IsHealthy() {
			log.Printf("Browser %d unhealthy, resetting...", b.ID)
			b.Reset()
		}
	}
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
