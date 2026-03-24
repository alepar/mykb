// Package ratelimit provides adaptive rate limiting for API clients.
package ratelimit

import (
	"sync"
	"sync/atomic"
	"time"
)

// AdaptiveLimiter implements a converging adaptive token bucket rate limiter.
// It learns the actual rate limit by tracking failure rates via EMA and
// operates at a safe margin below the learned ceiling. Periodically probes
// higher to discover if the limit has increased.
type AdaptiveLimiter struct {
	tokenCh      chan struct{}
	stopCh       chan struct{}
	rateChangeCh chan float64 // signals rate changes to the token generator goroutine

	// Configuration (immutable after creation)
	startingRate      float64
	floorRate         float64
	successesForProbe int
	safetyMargin      float64 // e.g., 0.9 = operate at 90% of ceiling
	probeMultiplier   float64 // e.g., 1.1 = probe at 110% of ceiling
	emaAlpha          float64 // EMA smoothing factor (higher = faster adaptation)

	// Mutable state protected by mu
	mu                   sync.Mutex
	currentRate          float64
	learnedCeiling       float64 // EMA of failure rates (0 = not yet learned)
	consecutiveSuccesses int

	// Stats (atomic for lock-free reads)
	totalSuccesses atomic.Int64
	totalFailures  atomic.Int64
}

// Config holds configuration for an AdaptiveLimiter.
type Config struct {
	// StartingRate is the initial token generation rate (tokens per second).
	// This also serves as the maximum rate ceiling before any failures are observed.
	StartingRate float64

	// FloorRate is the minimum rate (tokens per second). Rate will not
	// decrease below this value. If zero, defaults to StartingRate/16.
	FloorRate float64

	// BucketSize is the token bucket capacity. If zero, defaults to 5.
	BucketSize int

	// SuccessesForProbe is the number of consecutive successes required
	// before attempting to probe at a higher rate. If zero, defaults to 25.
	SuccessesForProbe int

	// SafetyMargin is the multiplier for the safe operating rate relative
	// to the learned ceiling. If zero, defaults to 0.9 (90% of ceiling).
	SafetyMargin float64

	// ProbeMultiplier is the multiplier for the probe rate relative to
	// the learned ceiling. If zero, defaults to 1.1 (110% of ceiling).
	ProbeMultiplier float64

	// EMAAlpha is the smoothing factor for the exponential moving average
	// of failure rates. Higher values adapt faster to changes.
	// If zero, defaults to 0.3.
	EMAAlpha float64
}

// Stats holds rate limiter statistics.
type Stats struct {
	TotalSuccesses int64
	TotalFailures  int64
	CurrentRate    float64
	LearnedCeiling float64 // The learned rate limit ceiling (0 if not yet learned)
}

func applyDefaults(cfg Config) Config {
	if cfg.FloorRate == 0 {
		cfg.FloorRate = cfg.StartingRate / 16.0
	}
	if cfg.BucketSize == 0 {
		cfg.BucketSize = 5
	}
	if cfg.SuccessesForProbe == 0 {
		cfg.SuccessesForProbe = 25
	}
	if cfg.SafetyMargin == 0 {
		cfg.SafetyMargin = 0.9
	}
	if cfg.ProbeMultiplier == 0 {
		cfg.ProbeMultiplier = 1.1
	}
	if cfg.EMAAlpha == 0 {
		cfg.EMAAlpha = 0.3
	}
	return cfg
}

// NewAdaptiveLimiter creates a new adaptive rate limiter with the given configuration.
func NewAdaptiveLimiter(cfg Config) *AdaptiveLimiter {
	cfg = applyDefaults(cfg)

	limiter := &AdaptiveLimiter{
		tokenCh:           make(chan struct{}, cfg.BucketSize),
		stopCh:            make(chan struct{}),
		rateChangeCh:      make(chan float64, 1),
		startingRate:      cfg.StartingRate,
		floorRate:         cfg.FloorRate,
		successesForProbe: cfg.SuccessesForProbe,
		safetyMargin:      cfg.SafetyMargin,
		probeMultiplier:   cfg.ProbeMultiplier,
		emaAlpha:          cfg.EMAAlpha,
		currentRate:       cfg.StartingRate,
		learnedCeiling:    0, // not learned yet
	}

	// Pre-fill the token bucket
	for range cfg.BucketSize {
		limiter.tokenCh <- struct{}{}
	}

	// Start the token generation goroutine (owns the ticker exclusively)
	go limiter.generateTokens(cfg.StartingRate)

	return limiter
}

// Acquire blocks until a token is available.
func (l *AdaptiveLimiter) Acquire() {
	<-l.tokenCh
}

// ReportSuccess signals that an operation succeeded. After enough consecutive
// successes, the limiter will probe at a higher rate to discover if the
// limit has increased.
func (l *AdaptiveLimiter) ReportSuccess() {
	l.totalSuccesses.Add(1)

	l.mu.Lock()
	defer l.mu.Unlock()

	// If we're probing (above safe rate) and succeeding, update ceiling upward
	ceiling := l.effectiveCeiling()
	safeRate := ceiling * l.safetyMargin
	if l.currentRate > safeRate*1.01 && l.learnedCeiling > 0 {
		// Only update if this would increase the ceiling
		if l.currentRate > l.learnedCeiling {
			l.learnedCeiling = l.emaAlpha*l.currentRate + (1-l.emaAlpha)*l.learnedCeiling
		}
	}

	l.consecutiveSuccesses++

	if l.consecutiveSuccesses >= l.successesForProbe {
		l.tryProbe()
		l.consecutiveSuccesses = 0
	}
}

// ReportFailure signals that an operation failed. The limiter updates
// its learned ceiling and drops to a safe rate.
func (l *AdaptiveLimiter) ReportFailure() {
	l.totalFailures.Add(1)

	l.mu.Lock()
	defer l.mu.Unlock()

	ceiling := l.effectiveCeiling()

	// Only update ceiling from failures at or below current ceiling.
	// Probe failures above the ceiling don't teach us anything new about
	// the actual limit - they just confirm it's below the probe rate.
	if l.currentRate <= ceiling {
		if l.learnedCeiling == 0 {
			l.learnedCeiling = l.currentRate
		} else {
			l.learnedCeiling = l.emaAlpha*l.currentRate + (1-l.emaAlpha)*l.learnedCeiling
		}
	}

	// Drop to safe rate
	l.currentRate = l.effectiveCeiling() * l.safetyMargin
	if l.currentRate < l.floorRate {
		l.currentRate = l.floorRate
	}
	l.consecutiveSuccesses = 0

	l.signalRateChange()
}

// Stats returns current statistics.
func (l *AdaptiveLimiter) Stats() Stats {
	l.mu.Lock()
	rate := l.currentRate
	ceiling := l.learnedCeiling
	l.mu.Unlock()

	return Stats{
		TotalSuccesses: l.totalSuccesses.Load(),
		TotalFailures:  l.totalFailures.Load(),
		CurrentRate:    rate,
		LearnedCeiling: ceiling,
	}
}

// Close stops the rate limiter and releases resources.
func (l *AdaptiveLimiter) Close() {
	close(l.stopCh)
}

func tickerInterval(rate float64) time.Duration {
	return time.Duration(float64(time.Second) / rate)
}

// generateTokens runs in its own goroutine and owns the ticker exclusively.
// Rate changes are signaled via rateChangeCh.
func (l *AdaptiveLimiter) generateTokens(initialRate float64) {
	ticker := time.NewTicker(tickerInterval(initialRate))
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case newRate := <-l.rateChangeCh:
			ticker.Reset(tickerInterval(newRate))
		case <-ticker.C:
			// Try to add a token (non-blocking)
			select {
			case l.tokenCh <- struct{}{}:
			default:
				// Bucket full, drop token
			}
		}
	}
}

// effectiveCeiling returns the current rate ceiling.
// If no failures have been observed yet, uses startingRate.
// Must be called with mu held.
func (l *AdaptiveLimiter) effectiveCeiling() float64 {
	if l.learnedCeiling == 0 {
		return l.startingRate
	}
	return l.learnedCeiling
}

// tryProbe attempts to increase the rate to probe for a higher limit.
// Must be called with mu held.
func (l *AdaptiveLimiter) tryProbe() {
	ceiling := l.effectiveCeiling()
	probeRate := ceiling * l.probeMultiplier

	// Cap at starting rate
	if probeRate > l.startingRate {
		probeRate = l.startingRate
	}

	// Only increase if probe rate is higher than current
	if probeRate > l.currentRate {
		l.currentRate = probeRate
		l.signalRateChange()
	}
}

// signalRateChange notifies the token generator of a rate change.
// Must be called with mu held.
func (l *AdaptiveLimiter) signalRateChange() {
	select {
	case l.rateChangeCh <- l.currentRate:
	default:
		// Channel full, a pending rate change will be applied
	}
}
