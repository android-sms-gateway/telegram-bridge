package smsgate

import "time"

// DefaultTimeout bounds a single send attempt.
//
// It covers the whole HTTP round trip to the SMSGate server. A reply is handled
// inside a Telegram update, so an attempt that hangs must not hold the update
// pipeline: the attempt is abandoned and the retry loop decides what happens
// next.
const DefaultTimeout = 10 * time.Second

// Config holds the configuration for the smsgate module.
//
// The values are mapped from the raw koanf config in internal/config/module.go
// (smsgate.base_url, smsgate.username, smsgate.password) and consumed directly
// by this module's constructor. An empty BaseURL falls back to the public
// SMSGate API address that client-go carries.
//
// Example:
//
//	cfg := smsgate.Config{
//	    BaseURL:  "https://api.example.com/3rdparty/v1",
//	    Username: "user",
//	    Password: "password",
//	    Timeout:  smsgate.DefaultTimeout,
//	}
//
//	service := smsgate.New(cfg, logger)
type Config struct {
	// BaseURL is the address of the SMSGate server REST API. Empty means the
	// public API default built into client-go.
	BaseURL string

	// Username is the basic-auth user of the SMSGate API account.
	Username string

	// Password is the basic-auth password of the SMSGate API account. It is
	// never logged.
	Password string

	// Timeout bounds one send attempt. A zero or negative value means
	// DefaultTimeout.
	Timeout time.Duration
}
