package ratelimit

import (
	"context"
	"gw2lfgserver/clientinfo"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ClientIDKey = "client-id"

// RateLimiter manages rate limits for different clients
type RateLimiter struct {
	limiters   map[string]*rate.Limiter
	mu         sync.RWMutex
	rate       rate.Limit
	burst      int
	cleanupInt time.Duration
}

// Config holds configuration for the rate limiter
type Config struct {
	RequestsPerSecond float64
	Burst             int
	CleanupInterval   time.Duration
}

// NewRateLimiter creates a new rate limiter with the given configuration
func NewRateLimiter(cfg Config) *RateLimiter {
	rl := &RateLimiter{
		limiters:   make(map[string]*rate.Limiter),
		rate:       rate.Limit(cfg.RequestsPerSecond),
		burst:      cfg.Burst,
		cleanupInt: cfg.CleanupInterval,
	}

	if cfg.CleanupInterval > 0 {
		go rl.cleanup()
	}

	return rl
}

func (rl *RateLimiter) Limit(ctx context.Context) error {
	clientInfo := clientinfo.FromContext(ctx)
	if clientInfo == nil {
		return status.Errorf(codes.InvalidArgument, "missing client ID")
	}

	limiter := rl.getLimiter(clientInfo.AccountID)
	if !limiter.Allow() {
		return status.Errorf(codes.ResourceExhausted, "rate limit exceeded for client %s", clientInfo.AccountID)
	}

	return nil
}

// Helper methods remain the same as before...
func (rl *RateLimiter) getLimiter(clientID string) *rate.Limiter {
	rl.mu.RLock()
	limiter, exists := rl.limiters[clientID]
	rl.mu.RUnlock()

	if exists {
		return limiter
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	if limiter, exists = rl.limiters[clientID]; exists {
		return limiter
	}

	limiter = rate.NewLimiter(rl.rate, rl.burst)
	rl.limiters[clientID] = limiter
	return limiter
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.cleanupInt)
	defer ticker.Stop()

	// for range ticker.C {
	// 	rl.mu.Lock()
	// 	for clientID, limiter := range rl.limiters {
	// 		if time.Since(limiter.LastTime()) > rl.cleanupInt {
	// 			delete(rl.limiters, clientID)
	// 		}
	// 	}
	// 	rl.mu.Unlock()
	// }
}
