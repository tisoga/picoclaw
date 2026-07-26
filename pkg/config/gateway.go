package config

import (
	"encoding/json"
	"net"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/netbind"
)

const DefaultGatewayLogLevel = "warn"

type GatewayConfig struct {
	Host      string               `json:"host"                yaml:"-" env:"PICOCLAW_GATEWAY_HOST"`
	Port      int                  `json:"port"                yaml:"-" env:"PICOCLAW_GATEWAY_PORT"`
	HotReload bool                 `json:"hot_reload"          yaml:"-" env:"PICOCLAW_GATEWAY_HOT_RELOAD"`
	LogLevel  string               `json:"log_level,omitempty" yaml:"-" env:"PICOCLAW_LOG_LEVEL"`
	Webhook   GatewayWebhookConfig `json:"webhook,omitempty"   yaml:"webhook,omitempty"`
}

// GatewayWebhookConfig configures an inbound HTTP webhook endpoint on the gateway.
// External services POST to the configured path with a Bearer token to send
// messages to any of the allowed channels.
type GatewayWebhookConfig struct {
	Enabled         bool         `json:"enabled"                    yaml:"-"`
	Token           SecureString `json:"token,omitzero"             yaml:"token,omitempty"`
	Path            string       `json:"path,omitempty"             yaml:"-"`
	AllowedChannels []string     `json:"allowed_channels,omitempty" yaml:"-"`
	AllowedIPs      []string     `json:"allowed_ips,omitempty"      yaml:"-"`
	AllowAllIPs     bool         `json:"allow_all_ips,omitempty"    yaml:"-"`
	MediaMaxMB      int          `json:"media_max_mb,omitempty"     yaml:"-"`
}

// EffectiveWebhookPath returns the configured webhook path or the default.
func (c GatewayWebhookConfig) EffectiveWebhookPath() string {
	if c.Path != "" {
		return c.Path
	}
	return "/webhook/send"
}

// EffectiveMediaMaxBytes returns the webhook attachment limit. Zero uses 10 MiB.
func (c GatewayWebhookConfig) EffectiveMediaMaxBytes() int64 {
	maxMB := c.MediaMaxMB
	if maxMB <= 0 {
		maxMB = 10
	}
	return int64(maxMB) * 1024 * 1024
}

// IsChannelAllowed returns true if the channel is in the allowed list.
// An empty AllowedChannels list means all channels are allowed.
func (c GatewayWebhookConfig) IsChannelAllowed(channel string) bool {
	if len(c.AllowedChannels) == 0 {
		return true
	}
	for _, allowed := range c.AllowedChannels {
		if strings.EqualFold(allowed, channel) {
			return true
		}
	}
	return false
}

// IsIPAllowed reports whether remoteAddr is authorized by the webhook IP
// policy. Entries may be individual IPv4/IPv6 addresses or CIDR ranges.
// Only the socket peer address is used; proxy headers are intentionally not
// trusted for authentication.
func (c GatewayWebhookConfig) IsIPAllowed(remoteAddr string) bool {
	if c.AllowAllIPs {
		return true
	}

	host := strings.TrimSpace(remoteAddr)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	remoteIP := net.ParseIP(host)
	if remoteIP == nil {
		return false
	}

	for _, entry := range c.AllowedIPs {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if allowedIP := net.ParseIP(strings.Trim(entry, "[]")); allowedIP != nil {
			if allowedIP.Equal(remoteIP) {
				return true
			}
			continue
		}
		if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(remoteIP) {
			return true
		}
	}
	return false
}

func canonicalGatewayLogLevel(level logger.LogLevel) string {
	switch level {
	case logger.DEBUG:
		return "debug"
	case logger.INFO:
		return "info"
	case logger.WARN:
		return "warn"
	case logger.ERROR:
		return "error"
	case logger.FATAL:
		return "fatal"
	default:
		return DefaultGatewayLogLevel
	}
}

func normalizeGatewayLogLevel(logLevel string) string {
	if level, ok := logger.ParseLevel(logLevel); ok {
		return canonicalGatewayLogLevel(level)
	}
	return DefaultGatewayLogLevel
}

// EffectiveGatewayLogLevel returns the normalized runtime log level from a loaded config.
// Invalid or empty values fall back to the package default.
func EffectiveGatewayLogLevel(cfg *Config) string {
	if cfg == nil {
		return DefaultGatewayLogLevel
	}
	return normalizeGatewayLogLevel(cfg.Gateway.LogLevel)
}

func resolveGatewayHostFromEnv(baseHost string) (string, error) {
	envHost, ok := os.LookupEnv(EnvGatewayHost)
	if !ok {
		return normalizeGatewayHostInput(baseHost)
	}

	envHost = strings.TrimSpace(envHost)
	if envHost == "" {
		return normalizeGatewayHostInput(baseHost)
	}

	return normalizeGatewayHostInput(envHost)
}

func normalizeGatewayHostInput(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = strings.TrimSpace(DefaultConfig().Gateway.Host)
	}
	if host == "" {
		host = "localhost"
	}
	return netbind.NormalizeHostInput(host)
}

// ResolveGatewayLogLevel reads the configured gateway log level without triggering
// the full config loader, so startup code can apply logging before config load logs run.
// The PICOCLAW_LOG_LEVEL environment variable overrides the file value.
func ResolveGatewayLogLevel(path string) string {
	cfg := struct {
		Gateway GatewayConfig `json:"gateway"`
	}{
		Gateway: GatewayConfig{LogLevel: DefaultGatewayLogLevel},
	}

	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			logger.WarnCF("config", "failed to parse gateway config, using defaults", map[string]any{
				"path":  path,
				"error": err.Error(),
			})
		}
	}

	if envLevel := os.Getenv("PICOCLAW_LOG_LEVEL"); envLevel != "" {
		cfg.Gateway.LogLevel = envLevel
	}

	return normalizeGatewayLogLevel(cfg.Gateway.LogLevel)
}
