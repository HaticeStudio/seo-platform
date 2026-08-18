package core

import "fmt"

// ErrorCode classifies why a sync or provider call failed. The classification
// drives retry policy and what the Console shows; raw provider bodies never
// travel with it.
type ErrorCode string

const (
	ErrNone          ErrorCode = ""
	ErrNotConfigured ErrorCode = "NOT_CONFIGURED"
	ErrUnauthorized  ErrorCode = "UNAUTHORIZED"
	ErrRateLimited   ErrorCode = "RATE_LIMITED"
	ErrTransient     ErrorCode = "TRANSIENT"
	ErrPartial       ErrorCode = "PARTIAL"
	ErrNoData        ErrorCode = "NO_DATA"
	ErrUnsupported   ErrorCode = "UNSUPPORTED"
	ErrInternal      ErrorCode = "INTERNAL"
)

// Retryable reports whether the runtime may retry without an administrator
// acting first. Unauthorized never retries: it pauses scheduling and asks for
// re-authorization instead of hammering the provider.
func (c ErrorCode) Retryable() bool {
	switch c {
	case ErrRateLimited, ErrTransient, ErrPartial:
		return true
	default:
		return false
	}
}

// SyncError is the classified failure a provider returns. Message must already
// be safe to show an administrator: no tokens, no raw response bodies.
type SyncError struct {
	Code    ErrorCode
	Message string
	// RetryAfter carries a provider-supplied backoff hint in seconds; zero
	// means the runtime picks its own backoff.
	RetryAfter int
}

func (e *SyncError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
