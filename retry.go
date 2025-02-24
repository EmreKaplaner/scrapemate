package scrapemate

import (
	"context"
	"math"
	"net/http"
	"time"
)

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxRetries      int
	InitialBackoff  time.Duration
	MaxBackoff      time.Duration
	BackoffFactor   float64
	RetryableStatus map[int]bool
	RetryableErrors map[string]bool
}

// DefaultRetryConfig returns the default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:     3,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second,
		BackoffFactor:  2.0,
		RetryableStatus: map[int]bool{
			http.StatusTooManyRequests:     true,
			http.StatusInternalServerError: true,
			http.StatusBadGateway:          true,
			http.StatusServiceUnavailable:  true,
			http.StatusGatewayTimeout:      true,
		},
		RetryableErrors: map[string]bool{
			"connection reset by peer": true,
			"timeout":                  true,
			"deadline exceeded":        true,
		},
	}
}

// WithRetry adds retry capabilities to an HTTPFetcher
func WithRetry(fetcher HTTPFetcher, config RetryConfig) HTTPFetcher {
	return &retryFetcher{
		fetcher: fetcher,
		config:  config,
	}
}

type retryFetcher struct {
	fetcher HTTPFetcher
	config  RetryConfig
}

func (r *retryFetcher) Fetch(ctx context.Context, job IJob) Response {
	var resp Response

	// Retry loop
	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Calculate backoff duration
			backoff := r.config.InitialBackoff * time.Duration(math.Pow(r.config.BackoffFactor, float64(attempt-1)))
			if backoff > r.config.MaxBackoff {
				backoff = r.config.MaxBackoff
			}

			// Wait for backoff period
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return Response{Error: ctx.Err()}
			}
		}

		// Make the request
		resp = r.fetcher.Fetch(ctx, job)

		// Check if we should retry
		if !r.shouldRetry(resp) {
			break
		}
	}

	return resp
}

func (r *retryFetcher) shouldRetry(resp Response) bool {
	if resp.Error != nil {
		// Check if error message contains retryable error
		errStr := resp.Error.Error()
		for retryableErr := range r.config.RetryableErrors {
			if contains(errStr, retryableErr) {
				return true
			}
		}
	}

	// Check status code
	if r.config.RetryableStatus[resp.StatusCode] {
		return true
	}

	return false
}

func (r *retryFetcher) Close() error {
	return r.fetcher.Close()
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[0:len(substr)] == substr
}
