package webhook

import "errors"

var (
	// ErrInvalidSignature is returned when the request has no signature, no
	// timestamp, or the signature does not match the configured signing key.
	ErrInvalidSignature = errors.New("missing or invalid signature")
)
