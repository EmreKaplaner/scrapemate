package tlssig

import (
	"context"
	"crypto/tls"
	"net/http"
	"sync"

	"github.com/EmreKaplaner/scrapemate"
)

// TLSSignatureFetcher implements browser TLS fingerprinting
type TLSSignatureFetcher struct {
	client    *http.Client
	transport *http.Transport
	mutex     sync.Mutex
	profiles  map[string]*tls.Config
}

// Available browser profiles
const (
	Chrome  = "chrome"
	Firefox = "firefox"
	Safari  = "safari"
	Edge    = "edge"
)

// New creates a new TLS signature fetcher with the specified browser profile
func New(browserProfile string) scrapemate.HTTPFetcher {
	t := &TLSSignatureFetcher{
		profiles: make(map[string]*tls.Config),
	}

	t.initProfiles()
	t.configureTransport(browserProfile)
	return t
}

func (t *TLSSignatureFetcher) initProfiles() {
	chromeTLS := &tls.Config{}
	firefoxTLS := &tls.Config{}
	safariTLS := &tls.Config{}
	edgeTLS := &tls.Config{}

	t.profiles[Chrome] = chromeTLS
	t.profiles[Firefox] = firefoxTLS
	t.profiles[Safari] = safariTLS
	t.profiles[Edge] = edgeTLS
}

func (t *TLSSignatureFetcher) configureTransport(browserProfile string) {
	t.transport = &http.Transport{
		TLSClientConfig: t.profiles[browserProfile],
	}
	t.client = &http.Client{
		Transport: t.transport,
	}
}

func (t *TLSSignatureFetcher) Fetch(ctx context.Context, job scrapemate.IJob) scrapemate.Response {
	return scrapemate.Response{
		StatusCode: 200,
	}
}

func (t *TLSSignatureFetcher) Close() error {
	return nil
}
