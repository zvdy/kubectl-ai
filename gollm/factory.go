// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gollm

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

var globalRegistry registry

type registry struct {
	mutex     sync.Mutex
	providers map[string]FactoryFunc
}

type FactoryFunc func(ctx context.Context, uri *url.URL) (Client, error)

func RegisterProvider(id string, factoryFunc FactoryFunc) error {
	return globalRegistry.RegisterProvider(id, factoryFunc)
}

func (r *registry) RegisterProvider(id string, factoryFunc FactoryFunc) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.providers == nil {
		r.providers = make(map[string]FactoryFunc)
	}
	_, exists := r.providers[id]
	if exists {
		return fmt.Errorf("provider %q is already registered", id)
	}
	r.providers[id] = factoryFunc
	return nil
}

func (r *registry) NewClient(ctx context.Context, providerID string) (Client, error) {
	// providerID can be just an ID, for example "gemini" instead of "gemini://"
	if !strings.Contains(providerID, "/") && !strings.Contains(providerID, ":") {
		providerID = providerID + "://"
	}

	u, err := url.Parse(providerID)
	if err != nil {
		return nil, fmt.Errorf("parsing provider id %q: %w", providerID, err)
	}

	factoryFunc := r.providers[u.Scheme]
	if factoryFunc == nil {
		return nil, fmt.Errorf("provider %q not registered", u.Scheme)
	}

	return factoryFunc(ctx, u)
}

// NewClient builds an Client based on the LLM_CLIENT env var or the provided providerID. ProviderID (if not empty) overrides the provider from LLM_CLIENT env var.
func NewClient(ctx context.Context, providerID string) (Client, error) {
	if providerID == "" {
		s := os.Getenv("LLM_CLIENT")
		if s == "" {
			return nil, fmt.Errorf("LLM_CLIENT is not set")
		}
		providerID = s
	}

	return globalRegistry.NewClient(ctx, providerID)
}

// APIError represents an error returned by the LLM client.
type APIError struct {
	StatusCode int
	Message    string
	Err        error
}

func (e *APIError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("API Error: Status=%d, Message='%s', OriginalErr=%v", e.StatusCode, e.Message, e.Err)
	}
	return fmt.Sprintf("API Error: Status=%d, Message='%s'", e.StatusCode, e.Message)
}

func (e *APIError) Unwrap() error {
	return e.Err
}

// IsRetryableFunc defines the signature for functions that check if an error is retryable.
// TODO (droot): Adjust the signature to allow underlying client to relay the backoff
// delay etc. for example, Gemini's error codes contain retryDelay information.
type IsRetryableFunc func(error) bool

// DefaultIsRetryableError provides a default implementation based on common HTTP codes and network errors.
func DefaultIsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusConflict, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// Add other error checks specific to LLM clients if needed
	// e.g., if errors.Is(err, specificLLMRateLimitError) { return true }

	return false
}

// RetryConfig holds the configuration for the retry mechanism (same as before)
type RetryConfig struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	BackoffFactor  float64
	Jitter         bool
}

// DefaultRetryConfig provides sensible defaults (same as before)
var DefaultRetryConfig = RetryConfig{
	MaxAttempts:    5,
	InitialBackoff: 200 * time.Millisecond, // Slightly increased default
	MaxBackoff:     10 * time.Second,
	BackoffFactor:  2.0,
	Jitter:         true,
}

// Retry executes the provided operation with retries, returning the result and error.
// It's now generic to handle any return type T.
func Retry[T any](
	ctx context.Context,
	config RetryConfig,
	isRetryable IsRetryableFunc,
	operation func(ctx context.Context) (T, error),
) (T, error) {
	var lastErr error
	var zero T // Zero value of the return type T

	log := klog.FromContext(ctx)

	backoff := config.InitialBackoff

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		// log.Printf("Executing operation, attempt %d of %d", attempt, config.MaxAttempts) // Optional verbose log
		result, err := operation(ctx)

		if err == nil {
			// Success
			return result, nil
		}
		lastErr = err // Store the last error encountered

		// Check if context was cancelled *after* the operation
		select {
		case <-ctx.Done():
			log.Info("Context cancelled after attempt %d failed.", "attempt", attempt)
			return zero, ctx.Err() // Return context error preferentially
		default:
			// Context not cancelled, proceed with error checking
		}

		if !isRetryable(lastErr) {
			log.Info("Attempt failed with non-retryable error", "attempt", attempt, "error", lastErr)
			return zero, lastErr // Return the non-retryable error immediately
		}

		log.Info("Attempt failed with retryable error", "attempt", attempt, "error", lastErr)

		if attempt == config.MaxAttempts {
			// Max attempts reached
			break
		}

		// Calculate wait time
		waitTime := backoff
		if config.Jitter {
			waitTime += time.Duration(rand.Float64() * float64(backoff) / 2)
		}

		log.Info("Waiting before next attempt", "waitTime", waitTime, "attempt", attempt+1, "maxAttempts", config.MaxAttempts)

		// Wait or react to context cancellation
		select {
		case <-time.After(waitTime):
			// Wait finished
		case <-ctx.Done():
			log.Info("Context cancelled while waiting for retry after attempt %d.", "attempt", attempt)
			return zero, ctx.Err()
		}

		// Increase backoff
		backoff = time.Duration(float64(backoff) * config.BackoffFactor)
		if backoff > config.MaxBackoff {
			backoff = config.MaxBackoff
		}
	}

	// If the loop finished, it means all attempts failed
	errFinal := fmt.Errorf("operation failed after %d attempts: %w", config.MaxAttempts, lastErr)
	return zero, errFinal
}

// retryChat is a generic decorator that adds retry logic to any Chat implementation.
type retryChat[C Chat] struct {
	underlying  Chat // The actual client implementation being wrapped
	config      RetryConfig
	isRetryable IsRetryableFunc
}

// NewRetryChat creates a new Chat that wraps the given underlying client
// with retry logic using the provided configuration.
// It returns the Chat interface type, hiding the generic implementation detail.
func NewRetryChat[C Chat](
	underlying C,
	config RetryConfig,
) Chat {

	return &retryChat[C]{
		underlying: underlying,
		config:     config,
	}
}

// Embed implements the Client interface for the retryClient decorator.
func (rc *retryChat[C]) Send(ctx context.Context, contents ...any) (ChatResponse, error) {
	// Define the operation
	operation := func(ctx context.Context) (ChatResponse, error) {
		return rc.underlying.Send(ctx, contents...)
	}

	// Execute with retry
	return Retry[ChatResponse](ctx, rc.config, rc.underlying.IsRetryableError, operation)
}

// Embed implements the Client interface for the retryClient decorator.
func (rc *retryChat[C]) SendStreaming(ctx context.Context, contents ...any) (ChatResponseIterator, error) {
	// Define a retryable operation for streaming
	var iterator ChatResponseIterator
	var streamErr error

	// First try to get a streaming connection
	operation := func(ctx context.Context) (bool, error) {
		iterator, streamErr = rc.underlying.SendStreaming(ctx, contents...)
		return streamErr == nil, streamErr
	}

	// Attempt streaming first
	success, err := RetryOperation(ctx, rc.config, rc.underlying.IsRetryableError, operation)

	// If streaming failed with a retryable error (likely streaming-related),
	// fall back to non-streaming Send
	if !success && err != nil && rc.underlying.IsRetryableError(err) {
		klog.InfoS("Streaming failed after retries, falling back to non-streaming Send", "error", err)

		// Try the non-streaming approach
		resp, sendErr := rc.underlying.Send(ctx, contents...)
		if sendErr != nil {
			return nil, fmt.Errorf("both streaming and non-streaming attempts failed: streaming: %w, non-streaming: %v", err, sendErr)
		}

		// Wrap the single response in an iterator
		return singletonChatResponseIterator(resp), nil
	}

	// If there was a non-retryable error or all retries failed, return the error
	if err != nil {
		return nil, err
	}

	// Return a wrapped iterator that handles retries on stream errors
	return func(yield func(ChatResponse, error) bool) {
		// Create a wrapper around the original yield function that handles retries
		wrappedYield := func(resp ChatResponse, err error) bool {
			// If there's no error, just pass through
			if err == nil {
				return yield(resp, nil)
			}

			// If there's an error and it's not retryable, pass it through
			if !rc.underlying.IsRetryableError(err) {
				return yield(resp, err)
			}

			// It's a retryable error, attempt to reconnect the stream
			klog.InfoS("Retryable error in stream", "error", err)

			// Try falling back to non-streaming approach
			fallbackResp, fallbackErr := rc.underlying.Send(ctx, contents...)
			if fallbackErr == nil {
				// Successfully got a non-streaming response
				klog.InfoS("Successfully fell back to non-streaming after streaming error")
				// This will be the last response in the stream
				return yield(fallbackResp, nil)
			}

			klog.InfoS("Non-streaming fallback also failed", "error", fallbackErr)

			var retrySucceeded bool
			var retryErr error

			// If fallback failed, try reconnecting the stream again
			retryOperation := func(ctx context.Context) (bool, error) {
				iterator, retryErr = rc.underlying.SendStreaming(ctx, contents...)
				return retryErr == nil, retryErr
			}

			retrySucceeded, retryErr = RetryOperation(ctx, rc.config, rc.underlying.IsRetryableError, retryOperation)

			if !retrySucceeded {
				// If retry failed, pass the original error through
				return yield(resp, fmt.Errorf("stream error, retry and fallback failed: %w", err))
			}

			// Successfully reconnected, the next iteration of the outer iterator will use the new stream
			klog.InfoS("Successfully reconnected stream after error")
			return true // Continue the iteration
		}

		// Use the original iterator with our wrapped yield function
		iterator(wrappedYield)
	}, nil
}

// RetryOperation is a helper for retrying operations that return a boolean success indicator and an error
func RetryOperation(
	ctx context.Context,
	config RetryConfig,
	isRetryable IsRetryableFunc,
	operation func(ctx context.Context) (bool, error),
) (bool, error) {
	var lastErr error
	log := klog.FromContext(ctx)
	backoff := config.InitialBackoff

	for attempt := 1; attempt <= config.MaxAttempts; attempt++ {
		success, err := operation(ctx)

		if err == nil && success {
			// Operation succeeded
			return true, nil
		}

		lastErr = err // Store the last error encountered

		// Check if context was cancelled
		select {
		case <-ctx.Done():
			log.Info("Context cancelled after attempt", "attempt", attempt)
			return false, ctx.Err()
		default:
			// Context not cancelled, proceed with retry logic
		}

		if !isRetryable(lastErr) {
			log.Info("Attempt failed with non-retryable error", "attempt", attempt, "error", lastErr)
			return false, lastErr
		}

		log.Info("Attempt failed with retryable error", "attempt", attempt, "error", lastErr)

		if attempt == config.MaxAttempts {
			// Max attempts reached
			break
		}

		// Calculate wait time
		waitTime := backoff
		if config.Jitter {
			waitTime += time.Duration(rand.Float64() * float64(backoff) / 2)
		}

		log.Info("Waiting before next attempt", "waitTime", waitTime, "attempt", attempt+1, "maxAttempts", config.MaxAttempts)

		// Wait or react to context cancellation
		select {
		case <-time.After(waitTime):
			// Wait finished
		case <-ctx.Done():
			log.Info("Context cancelled while waiting for retry after attempt", "attempt")
			return false, ctx.Err()
		}

		// Increase backoff
		backoff = time.Duration(float64(backoff) * config.BackoffFactor)
		if backoff > config.MaxBackoff {
			backoff = config.MaxBackoff
		}
	}

	// If the loop finished, it means all attempts failed
	errFinal := fmt.Errorf("operation failed after %d attempts: %w", config.MaxAttempts, lastErr)
	return false, errFinal
}

func (rc *retryChat[C]) SetFunctionDefinitions(functionDefinitions []*FunctionDefinition) error {
	return rc.underlying.SetFunctionDefinitions(functionDefinitions)
}

func (rc *retryChat[C]) IsRetryableError(err error) bool {
	return rc.underlying.IsRetryableError(err)
}
