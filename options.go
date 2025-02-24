package scrapemate

import (
	"time"
)

// WithStealthMode enables all anti-detection features
func WithStealthMode(s *ScrapeMate) error {
	// Configure random delays
	s.minDelay = 500 * time.Millisecond
	s.maxDelay = 3000 * time.Millisecond

	// Enable random user agent rotation
	s.useRandomUserAgent = true

	// Enable TLS fingerprinting
	// This requires implementation of TLS signature fetcher
	fetcher, err := tlsSignatureFromUserAgent(s.userAgent)
	if err == nil {
		s.httpFetcher = fetcher
	}

	// Configure retry with exponential backoff
	s.retryConfig = DefaultRetryConfig()
	s.httpFetcher = WithRetry(s.httpFetcher, s.retryConfig)

	// Set default timeout
	s.timeoutPerJob = 60 * time.Second

	// Configure proper request headers
	s.defaultHeaders = map[string]string{
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8",
		"Accept-Language":           "en-US,en;q=0.5",
		"Accept-Encoding":           "gzip, deflate, br",
		"Connection":                "keep-alive",
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
		"DNT":                       "1",
	}

	return nil
}

// WithNormalMode disables stealth features for better performance
func WithNormalMode(s *ScrapeMate) error {
	s.minDelay = 0
	s.maxDelay = 0
	s.useRandomUserAgent = false

	// Reset to default fetcher
	// ...

	return nil
}
