// Package retry provides exponential backoff retry logic for transient failures.
package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// Config configures retry behavior.
type Config struct {
	// MaxRetries is the maximum number of retry attempts (default: 3).
	// Set to 0 for no retries (execute once).
	MaxRetries int

	// InitialBackoff is the delay before the first retry (default: 100ms).
	InitialBackoff time.Duration

	// MaxBackoff caps the backoff duration (default: 30s).
	MaxBackoff time.Duration

	// Multiplier increases backoff after each retry (default: 2.0).
	Multiplier float64

	// Jitter adds randomness to prevent thundering herd (default: 0.1 = 10%).
	// Value between 0 and 1 where 0 means no jitter and 1 means +/- 100%.
	Jitter float64

	// IsRetryable determines if an error should be retried.
	// If nil, defaults to DefaultIsRetryable.
	IsRetryable func(error) bool
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxRetries:     3,
		InitialBackoff: 100 * time.Millisecond,
		MaxBackoff:     30 * time.Second,
		Multiplier:     2.0,
		Jitter:         0.1,
		IsRetryable:    DefaultIsRetryable,
	}
}

// Sentinel errors.
var (
	// ErrNotRetryable wraps non-retryable errors to stop retry attempts.
	ErrNotRetryable = errors.New("retry: error is not retryable")

	// ErrMaxRetries is returned when all retry attempts are exhausted.
	ErrMaxRetries = errors.New("retry: max retries exceeded")

	// ErrContextCanceled wraps context cancellation errors.
	ErrContextCanceled = errors.New("retry: context canceled")
)

// RetryableFunc is the function type that can be retried.
type RetryableFunc func(ctx context.Context) error

// Do executes fn with retries according to cfg.
// Returns the last error if all retries fail.
func Do(ctx context.Context, cfg Config, fn RetryableFunc) error {
	cfg = applyDefaults(cfg)

	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		// Check context before each attempt
		if ctx.Err() != nil {
			if lastErr != nil {
				return &RetryError{
					Cause:    lastErr,
					Attempts: attempt,
					Err:      ErrContextCanceled,
				}
			}
			return ctx.Err()
		}

		// Execute the function
		err := fn(ctx)
		if err == nil {
			return nil
		}

		lastErr = err

		// Check if error is retryable
		if !cfg.IsRetryable(err) {
			return &RetryError{
				Cause:    err,
				Attempts: attempt + 1,
				Err:      ErrNotRetryable,
			}
		}

		// Don't sleep after the last attempt
		if attempt < cfg.MaxRetries {
			backoff := calculateBackoff(cfg, attempt)
			select {
			case <-ctx.Done():
				return &RetryError{
					Cause:    lastErr,
					Attempts: attempt + 1,
					Err:      ErrContextCanceled,
				}
			case <-time.After(backoff):
				// Continue to next attempt
			}
		}
	}

	return &RetryError{
		Cause:    lastErr,
		Attempts: cfg.MaxRetries + 1,
		Err:      ErrMaxRetries,
	}
}

// DoWithResult executes fn with retries and returns a result value.
func DoWithResult[T any](ctx context.Context, cfg Config, fn func(ctx context.Context) (T, error)) (T, error) {
	var result T
	err := Do(ctx, cfg, func(ctx context.Context) error {
		var fnErr error
		result, fnErr = fn(ctx)
		return fnErr
	})
	return result, err
}

// RetryError provides details about a failed retry operation.
type RetryError struct {
	// Cause is the last error returned by the function.
	Cause error

	// Attempts is the number of attempts made.
	Attempts int

	// Err is the sentinel error (ErrMaxRetries, ErrNotRetryable, or ErrContextCanceled).
	Err error
}

func (e *RetryError) Error() string {
	return fmt.Sprintf("retry failed after %d attempts (%s): %s", e.Attempts, e.Err, e.Cause)
}

func (e *RetryError) Unwrap() error {
	return e.Cause
}

func (e *RetryError) Is(target error) bool {
	return errors.Is(e.Err, target) || errors.Is(e.Cause, target)
}

// calculateBackoff computes the backoff duration for an attempt.
func calculateBackoff(cfg Config, attempt int) time.Duration {
	// Exponential backoff: initial * multiplier^attempt
	backoff := float64(cfg.InitialBackoff) * math.Pow(cfg.Multiplier, float64(attempt))

	// Apply max cap
	if backoff > float64(cfg.MaxBackoff) {
		backoff = float64(cfg.MaxBackoff)
	}

	// Apply jitter
	if cfg.Jitter > 0 {
		jitterRange := backoff * cfg.Jitter
		backoff = backoff - jitterRange + (rand.Float64() * 2 * jitterRange)
	}

	return time.Duration(backoff)
}

// applyDefaults fills in zero values with defaults.
func applyDefaults(cfg Config) Config {
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = 100 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.Multiplier <= 0 {
		cfg.Multiplier = 2.0
	}
	if cfg.Jitter < 0 {
		cfg.Jitter = 0
	}
	if cfg.Jitter > 1 {
		cfg.Jitter = 1
	}
	if cfg.IsRetryable == nil {
		cfg.IsRetryable = DefaultIsRetryable
	}
	return cfg
}

// DefaultIsRetryable returns true for errors that are typically transient.
// Override with Config.IsRetryable for custom behavior.
func DefaultIsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Check for explicit non-retryable marker
	if errors.Is(err, ErrNotRetryable) {
		return false
	}

	// Check for known retryable errors
	var retryable interface{ Retryable() bool }
	if errors.As(err, &retryable) {
		return retryable.Retryable()
	}

	// By default, treat unknown errors as retryable (conservative approach)
	// Applications should use Config.IsRetryable for fine-grained control
	return true
}

// MarkNotRetryable wraps an error to indicate it should not be retried.
func MarkNotRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &notRetryableError{cause: err}
}

type notRetryableError struct {
	cause error
}

func (e *notRetryableError) Error() string {
	return e.cause.Error()
}

func (e *notRetryableError) Unwrap() error {
	return e.cause
}

func (e *notRetryableError) Retryable() bool {
	return false
}

// MarkRetryable wraps an error to explicitly indicate it can be retried.
func MarkRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &retryableError{cause: err}
}

type retryableError struct {
	cause error
}

func (e *retryableError) Error() string {
	return e.cause.Error()
}

func (e *retryableError) Unwrap() error {
	return e.cause
}

func (e *retryableError) Retryable() bool {
	return true
}
