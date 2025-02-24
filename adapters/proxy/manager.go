package proxy

import (
	"net/url"
	"sync"
	"time"
)

// ProxyManager handles proxy rotation, health checking and fallback
type ProxyManager struct {
	proxies       []*Proxy
	currentIndex  int
	mutex         sync.Mutex
	checkInterval time.Duration
}

// Proxy represents a proxy with health status
type Proxy struct {
	URL           *url.URL
	IsHealthy     bool
	LastChecked   time.Time
	FailureCount  int
	ResponseTimes []time.Duration
}

// NewManager creates a proxy manager
func NewManager(proxyURLs []string, checkInterval time.Duration) (*ProxyManager, error) {
	manager := &ProxyManager{
		proxies:       make([]*Proxy, 0, len(proxyURLs)),
		checkInterval: checkInterval,
	}

	for _, p := range proxyURLs {
		u, err := url.Parse(p)
		if err != nil {
			return nil, err
		}
		manager.proxies = append(manager.proxies, &Proxy{
			URL:       u,
			IsHealthy: true,
		})
	}

	go manager.healthCheckLoop()

	return manager, nil
}

func (p *ProxyManager) GetNextHealthy() *url.URL {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	startIdx := p.currentIndex
	for i := 0; i < len(p.proxies); i++ {
		idx := (startIdx + i) % len(p.proxies)
		if p.proxies[idx].IsHealthy {
			p.currentIndex = (idx + 1) % len(p.proxies)
			return p.proxies[idx].URL
		}
	}

	if len(p.proxies) > 0 {
		p.proxies[0].IsHealthy = true
		return p.proxies[0].URL
	}

	return nil
}

func (p *ProxyManager) healthCheckLoop() {
	ticker := time.NewTicker(p.checkInterval)
	defer ticker.Stop()

	for range ticker.C {
		p.checkAllProxies()
	}
}

// checkAllProxies performs health check on all proxies
func (p *ProxyManager) checkAllProxies() {
	// Implementation details...
}
