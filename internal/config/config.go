package config

import (
	"fmt"
	"os"

	"github.com/go-core-fx/config"
)

// Raw koanf fields for every configuration section. Module-owned values are
// mapped from here in module.go and reach their consumer as the bare value
// that consumer takes.
//
// Every section mirrors exactly one runtime owner: telegram carries only
// telegofx infrastructure and becomes telegofx.Config field by field; bot
// carries who may drive the routes the bot registers and becomes
// internal/bot/config.Config; bridge carries only the forward target and
// becomes bridge.Config; smsgate.webhook nests under the SMSGate integration
// it authenticates; storage carries only the embedded database location and
// becomes boltfx.Config, which opens the file for internal/store.
type http struct {
	Address     string   `koanf:"address"`
	ProxyHeader string   `koanf:"proxy_header"`
	Proxies     []string `koanf:"proxies"`

	OpenAPI openAPIConfig `koanf:"openapi"`
}

type openAPIConfig struct {
	Enabled    bool   `koanf:"enabled"`
	PublicHost string `koanf:"public_host"`
	PublicPath string `koanf:"public_path"`
}

// telegramConfig is the telegofx infrastructure surface only. The token and the
// SOCKS5 proxy dialer are both owned by telegofx and reach it unchanged as
// telegofx.Config fields (see module.go); who may drive the bot is not a
// Telegram client setting and lives in botConfig, next to the module that
// enforces it.
type telegramConfig struct {
	Token string `koanf:"token"`
	Proxy string `koanf:"proxy"`
}

// botConfig is the bot-module surface: who may talk to this bot. The allowlist
// authorizes a Telegram actor, and the module that turns an update into a call
// is the bot itself, so the section follows the setting to the routes that
// enforce it (see module.go).
type botConfig struct {
	AllowedUserIDs []int64 `koanf:"allowed_user_ids"`
}

// bridgeConfig is the bridge-domain surface: the single forward target of the
// SMSGate -> Telegram bridge. It reaches internal/bridge as
// bridge.Config.ChatID (see module.go).
type bridgeConfig struct {
	ChatID int64 `koanf:"chat_id"`
}

// webhookConfig is the inbound webhook surface of the SMSGate integration, so
// it nests under smsgate. Only its signing key is read, and it reaches the
// webhook service as a plain string (see module.go).
type webhookConfig struct {
	SigningKey string `koanf:"signing_key"`
}

type smsgateConfig struct {
	BaseURL  string `koanf:"base_url"`
	Username string `koanf:"username"`
	Password string `koanf:"password"`

	Webhook webhookConfig `koanf:"webhook"`
}

// storageConfig is the embedded database surface: the single bbolt file that
// holds the bridge state. It reaches pkg/boltfx unchanged as
// boltfx.Config{URL} (see module.go); internal/store owns the bucket schema.
type storageConfig struct {
	URL string `koanf:"url"`
}

type Config struct {
	HTTP     http           `koanf:"http"`
	Telegram telegramConfig `koanf:"telegram"`
	Bot      botConfig      `koanf:"bot"`
	Bridge   bridgeConfig   `koanf:"bridge"`

	SMSGate smsgateConfig `koanf:"smsgate"`
	Storage storageConfig `koanf:"storage"`
}

func Default() Config {
	return Config{
		HTTP: http{
			Address:     "127.0.0.1:3000",
			ProxyHeader: "X-Forwarded-For",
			Proxies:     []string{},
			OpenAPI: openAPIConfig{
				Enabled:    true,
				PublicHost: "",
				PublicPath: "",
			},
		},
		Telegram: telegramConfig{
			Token: "",
			Proxy: "",
		},
		Bot: botConfig{
			// An empty allowlist closes every filtered route: replies stay off
			// until an operator names the Telegram ids that may use them.
			AllowedUserIDs: []int64{},
		},
		Bridge: bridgeConfig{
			ChatID: 0,
		},

		SMSGate: smsgateConfig{
			BaseURL:  "",
			Username: "",
			Password: "",
			Webhook: webhookConfig{
				SigningKey: "",
			},
		},
		Storage: storageConfig{
			URL: "bolt://data/telegram-bridge.db",
		},
	}
}

func New() (Config, error) {
	cfg := Default()

	options := []config.Option{}
	if yamlPath := os.Getenv("CONFIG_PATH"); yamlPath != "" {
		options = append(options, config.WithLocalYAML(yamlPath))
	}

	if err := config.Load(&cfg, options...); err != nil {
		return Config{}, fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, nil
}
