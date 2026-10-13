// Package smsgate sends SMS through the SMSGate server REST API.
//
// It is the outbound half of the reply path: an answer typed in Telegram
// becomes an SMS to the original sender through the SMSGate server API
// (client-go, basic auth), never directly to the Android device. The server owns
// device selection, queuing and delivery state; this package only enqueues.
//
// Credentials and the base URL come from the raw smsgate config section
// (internal/config/config.go) through Config, and the client is built once at
// startup by New.
package smsgate

import "errors"

var (
	// ErrNoCredentials is returned when the service was configured without an
	// API username and password. The send fails closed instead of issuing a
	// request that the SMSGate server can only answer with 401.
	ErrNoCredentials = errors.New("smsgate: api credentials are not configured")
)
