package bridge

// Config holds the configuration of the bridge: where a forwarded message is
// delivered.
//
// The values are mapped from the raw koanf config in internal/config/module.go
// and consumed directly by this module's constructor. The raw section mirrors
// the single runtime owner: the bridge is the only thing that knows which chat
// a forwarded SMS goes to. Who may answer such a message is not a bridge
// setting - it is a property of the bot, which filters the updates it routes
// before any of them reaches this service.
//
// Example:
//
//	cfg := bridge.Config{
//	    ChatID: 123456789,
//	}
//
//	service := bridge.New(cfg, bot, repo, sms, logger)
type Config struct {
	// ChatID is the numeric Telegram chat that receives forwarded messages.
	// It is the forward target of the bridge section (bridge.chat_id, BRIDGE__CHAT_ID).
	//
	// Zero means "not configured" and makes every Forward call fail with
	// ErrNoChatID.
	ChatID int64
}
