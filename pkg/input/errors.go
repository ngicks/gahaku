package input

import (
	"errors"
	"fmt"
	"net/netip"
)

// ErrLocalInputDisabled reports a local path arriving at a resolver with no
// allowlisted root: local input is deny-by-default (D-04). Maps to
// PERMISSION_DENIED.
var ErrLocalInputDisabled = errors.New("input: local path input is disabled")

// LimitExceededError reports input outgrowing its byte cap. Maps to
// RESOURCE_EXHAUSTED with an InputSizeLimitExceeded detail.
type LimitExceededError struct {
	Limit int64
	// Received is the byte count reached when the limit tripped, which for a
	// fetch is a lower bound on the source's real size.
	Received int64
}

func (e *LimitExceededError) Error() string {
	return fmt.Sprintf("input: size limit of %d bytes exceeded (received %d)", e.Limit, e.Received)
}

// PathNotAllowedError reports a local path that is not readable inside any
// allowlisted root — outside every root, escaping one through "..", a symlink
// pointing out of one, or not a regular file. Maps to PERMISSION_DENIED.
type PathNotAllowedError struct {
	Path string
	Err  error
}

func (e *PathNotAllowedError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("input: path %q is outside every allowed root", e.Path)
	}
	return fmt.Sprintf("input: path %q is not allowed: %s", e.Path, e.Err)
}

func (e *PathNotAllowedError) Unwrap() error { return e.Err }

// SchemeNotAllowedError reports a source URL whose scheme the resolver refuses:
// anything but https, or http when it is not opted in. Maps to
// INVALID_ARGUMENT.
type SchemeNotAllowedError struct {
	Scheme string
}

func (e *SchemeNotAllowedError) Error() string {
	return fmt.Sprintf("input: url scheme %q is not allowed", e.Scheme)
}

// AddressBlockedError reports a connection refused by the address policy.
// Maps to INVALID_ARGUMENT.
type AddressBlockedError struct {
	Address netip.Addr
}

func (e *AddressBlockedError) Error() string {
	return fmt.Sprintf("input: address %s is in a blocked range", e.Address)
}

// FetchStatusError reports a non-2xx answer from the presigned GET URL. 403
// most often means the presign expired, which pkg/server maps to
// FAILED_PRECONDITION.
type FetchStatusError struct {
	StatusCode int
}

func (e *FetchStatusError) Error() string {
	return fmt.Sprintf("input: fetching source returned status %d", e.StatusCode)
}
