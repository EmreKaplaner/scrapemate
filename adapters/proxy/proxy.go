
package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/gosom/scrapemate"
)

// Rotator holds a list of proxies and rotates through them.
type Rotator struct {
	proxies []scrapemate.Proxy
	current uint32
	cache   sync.Map
}

// New creates a new Rotator from a list of proxy strings.
func New(proxies []string) *Rotator {
	if len(proxies) == 0 {
		panic("no proxies provided")
	}

	plist := make([]scrapemate.Proxy, len(proxies))
	for i, pstr := range proxies {
		// NewProxy is expected to return a Proxy that contains the full URL
		// (including any credentials if provided) or separately stored credentials.
		p, err := scrapemate.NewProxy(pstr)
		if err != nil {
			panic(err)
		}
		plist[i] = p
	}

	return &Rotator{
		proxies: plist,
		current: 0,
	}
}

// Next returns the next proxy in a round-robin fashion.
func (pr *Rotator) Next() scrapemate.Proxy {
	current := atomic.AddUint32(&pr.current, 1) - 1
	return pr.proxies[current%uint32(len(pr.proxies))]
}

// RoundTrip implements the http.RoundTripper interface.
// It creates (or reuses from cache) an http.Transport that uses the proxy settings.
func (pr *Rotator) RoundTrip(req *http.Request) (*http.Response, error) {
	// Get the next proxy from the rotator.
	next := pr.Next()

	// Parse the proxy URL.
	proxyURL, err := url.Parse(next.URL)
	if err != nil {
		return nil, fmt.Errorf("error parsing proxy URL for %s: %v", next.URL, err)
	}

	// If the proxy URL does not include credentials, but they are provided separately,
	// then set them.
	if proxyURL.User == nil && next.Username != "" && next.Password != "" {
		proxyURL.User = url.UserPassword(next.Username, next.Password)
	}

	// Use a cache key based on the final proxy URL string.
	cacheKey := proxyURL.String()
	transportIface, ok := pr.cache.Load(cacheKey)
	if !ok {
		transportIface = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
		pr.cache.Store(cacheKey, transportIface)
	}

	transport := transportIface.(*http.Transport)
	return transport.RoundTrip(req)
}
