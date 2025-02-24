package http2

import (
	"context"
	"crypto/tls"
	"net/http"

	"golang.org/x/net/http2"

	"github.com/EmreKaplaner/scrapemate"
)

type HTTP2Fetcher struct {
	client *http.Client
}

func New() scrapemate.HTTPFetcher {
	// Configure transport with HTTP/2 support
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
	}

	// Enable HTTP/2
	http2.ConfigureTransport(transport)

	return &HTTP2Fetcher{
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// Allow up to 10 redirects
				if len(via) >= 10 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

func (h *HTTP2Fetcher) Fetch(ctx context.Context, job scrapemate.IJob) scrapemate.Response {
	// Minimal example returning an empty response
	return scrapemate.Response{
		StatusCode: 200,
	}
}

func (h *HTTP2Fetcher) Close() error {
	return nil
}
