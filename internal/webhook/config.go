package webhook

// Config holds the configuration for the webhook module.
//
// The values are mapped from the raw koanf config in internal/config/module.go
// (smsgate.webhook.signing_key) and consumed directly by this module's
// constructor.
//
// Example:
//
//	cfg := webhook.Config{
//	    SigningKey: "signing-key",
//	}
//
//	service := webhook.New(cfg, bridge, metrics, logger)
type Config struct {
	// SigningKey is the SMSGate payload signing key. It must match the signing
	// key configured on the device (Settings -> Webhooks -> Signing Key); an
	// empty key is fail-closed and rejects every request.
	SigningKey string
}
