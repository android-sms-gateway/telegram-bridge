package config

import (
	"fmt"
	"os"
	"time"

	"github.com/go-core-fx/config"
)

// Raw koanf fields for every configuration section. Module-owned values are
// mapped from here in module.go and reach their consumer as the bare value
// that consumer takes.
// Sections without a module yet (storage) stay raw until their wave-2 modules
// are created.
//
// Every section mirrors exactly one runtime owner: telegram carries only
// telegofx infrastructure and becomes telegofx.Config field by field; bridge
// carries only the forward target and becomes bridge.Config; cache carries the
// cachefx backend and becomes cachefx.Config; smsgate.webhook nests under the
// SMSGate integration it authenticates.
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
// telegofx.Config fields (see module.go); where a forwarded message is
// delivered is not a Telegram client setting and lives in bridgeConfig.
type telegramConfig struct {
	Token    string `koanf:"token"`
	ProxyURL string `koanf:"proxy_url"`
}

// bridgeConfig is the bridge-domain surface: the single forward target of the
// SMSGate -> Telegram bridge. It reaches internal/bridge as
// bridge.Config{ChatID} (see module.go).
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

// cacheConfig is the cachefx backend surface. Every module creates its own
// named cache through the application factory; the URL selects the backend
// (memory:// or redis://) and the sweep interval bounds how long expired
// entries stay resident.
type cacheConfig struct {
	URL           string        `koanf:"url"`
	SweepInterval time.Duration `koanf:"sweep_interval"`
}

type storageConfig struct {
	URL string `koanf:"url"`
}

type Config struct {
	HTTP     http           `koanf:"http"`
	Telegram telegramConfig `koanf:"telegram"`
	Bridge   bridgeConfig   `koanf:"bridge"`
	Cache    cacheConfig    `koanf:"cache"`

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
			Token:    "",
			ProxyURL: "",
		},
		Bridge: bridgeConfig{
			ChatID: 0,
		},
		Cache: cacheConfig{
			URL:           "memory://",
			SweepInterval: time.Minute,
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
