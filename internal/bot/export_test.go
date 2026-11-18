package bot

import (
	botconfig "github.com/android-sms-gateway/telegram-bridge/internal/bot/config"
	"go.uber.org/zap"
)

// WarnEmptyAllowlist exposes the startup report to the external test package.
func WarnEmptyAllowlist(cfg botconfig.Config, log *zap.Logger) {
	warnEmptyAllowlist(cfg, log)
}
