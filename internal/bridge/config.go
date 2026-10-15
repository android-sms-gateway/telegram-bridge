package bridge

// Config holds the configuration for the bridge module.
//
// The values are mapped from the raw koanf config in internal/config/module.go
// (bridge.chat_id) and consumed directly by this module's constructor.
//
// Example:
//
//	cfg := bridge.Config{
//	    ChatID: 123456789,
//	}
//
//	service := bridge.New(cfg, bot, logger)
type Config struct {
	// ChatID is the numeric Telegram chat that receives forwarded messages.
	// Zero means "not configured" and makes every Forward call fail with
	// ErrNoChatID.
	ChatID int64
}
