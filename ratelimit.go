package scrapemate

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// RateLimiter manages request rates to avoid detection
type RateLimiter struct {
	// Rate limits by domain
	domainLimiters map[string]*domainLimiter
	mutex          sync.Mutex
	defaultRate    float64 // requests per second
}

type domainLimiter struct {
	rate           float64 // requests per second
	lastRequest    time.Time
	backoffFactor  float64
	currentBackoff float64
	mutex          sync.Mutex
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(defaultRate float64) *RateLimiter {
	return &RateLimiter{
		domainLimiters: make(map[string]*domainLimiter),
		defaultRate:    defaultRate,
	}
}

// Wait waits the appropriate amount of time before making a request
func (r *RateLimiter) Wait(ctx context.Context, domain string) error {
	r.mutex.Lock()
	limiter, exists := r.domainLimiters[domain]
	if !exists {
		limiter = &domainLimiter{
			rate:           r.defaultRate,
			backoffFactor:  1.5,
			currentBackoff: 1.0,
		}
		r.domainLimiters[domain] = limiter
	}
	r.mutex.Unlock()

	return limiter.wait(ctx)
}

// RegisterSuccess indicates a successful request
func (r *RateLimiter) RegisterSuccess(domain string) {
	r.mutex.Lock()
	limiter, exists := r.domainLimiters[domain]
	r.mutex.Unlock()

	if exists {
		limiter.mutex.Lock()
		defer limiter.mutex.Unlock()

		// Gradually reduce backoff on success
		if limiter.currentBackoff > 1.0 {
			limiter.currentBackoff = limiter.currentBackoff / 1.2
			if limiter.currentBackoff < 1.0 {
				limiter.currentBackoff = 1.0
			}
		}
	}
}

// RegisterFailure indicates a failed request (e.g., 429 Too Many Requests)
func (r *RateLimiter) RegisterFailure(domain string) {
	r.mutex.Lock()
	limiter, exists := r.domainLimiters[domain]
	r.mutex.Unlock()

	if exists {
		limiter.mutex.Lock()
		defer limiter.mutex.Unlock()

		// Increase backoff on failure
		limiter.currentBackoff *= limiter.backoffFactor
	}
}

// wait waits for the appropriate time between requests
func (l *domainLimiter) wait(ctx context.Context) error {
	l.mutex.Lock()

	// Calculate time to wait
	interval := time.Duration(1000/l.rate*float64(l.currentBackoff)) * time.Millisecond

	// Add jitter (±20%) to avoid detection patterns
	jitter := rand.Float64()*0.4 - 0.2 // -20% to +20%
	interval = time.Duration(float64(interval) * (1 + jitter))

	// Calculate wait time based on last request
	var waitTime time.Duration
	if !l.lastRequest.IsZero() {
		elapsed := time.Since(l.lastRequest)
		if elapsed < interval {
			waitTime = interval - elapsed
		}
	}

	// Update last request time
	l.lastRequest = time.Now().Add(waitTime)
	l.mutex.Unlock()

	// Wait for the calculated time
	if waitTime > 0 {
		select {
		case <-time.After(waitTime):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}
