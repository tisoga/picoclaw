package whatsappcommon

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

const DefaultMediaMaxMB = 50

func MediaMaxBytes(cfg *config.WhatsAppSettings) int64 {
	if cfg != nil && cfg.MediaMaxMB > 0 {
		return int64(cfg.MediaMaxMB) * 1024 * 1024
	}
	return DefaultMediaMaxMB * 1024 * 1024
}

// Allowed applies WhatsApp-specific DM/group policy after the channel's base
// allowlist decision. Empty policies preserve the legacy PicoClaw behavior.
func Allowed(cfg *config.WhatsAppSettings, baseAllowed bool, senderID, chatID string, group bool) bool {
	if cfg == nil {
		return baseAllowed
	}
	if !group {
		switch strings.ToLower(strings.TrimSpace(cfg.DMPolicy)) {
		case "disabled":
			return false
		case "open":
			return true
		default:
			return baseAllowed
		}
	}

	groupCfg, configured := cfg.Groups[chatID]
	if !configured {
		groupCfg, configured = cfg.Groups["*"]
	}
	if len(cfg.Groups) > 0 && !configured {
		return false
	}
	if configured && groupCfg.Enabled != nil && !*groupCfg.Enabled {
		return false
	}

	policy := strings.ToLower(strings.TrimSpace(cfg.GroupPolicy))
	if policy == "disabled" {
		return false
	}
	allowed := baseAllowed
	if policy == "open" {
		allowed = true
	}
	list := cfg.GroupAllowFrom
	if configured && len(groupCfg.AllowFrom) > 0 {
		list = groupCfg.AllowFrom
	}
	if len(list) > 0 {
		allowed = matches(list, senderID)
	}
	if policy == "allowlist" && len(list) == 0 {
		return false
	}
	return allowed
}

func RequireMention(cfg *config.WhatsAppSettings, chatID string) *bool {
	if cfg == nil {
		return nil
	}
	groupCfg, ok := cfg.Groups[chatID]
	if !ok {
		groupCfg, ok = cfg.Groups["*"]
	}
	if !ok {
		return nil
	}
	return groupCfg.RequireMention
}

func matches(list []string, senderID string) bool {
	normalizedSender := normalizeSender(senderID)
	for _, entry := range list {
		if entry == "*" || normalizeSender(entry) == normalizedSender {
			return true
		}
	}
	return false
}

func normalizeSender(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(value, "+"))
	if at := strings.IndexByte(value, '@'); at >= 0 {
		value = value[:at]
	}
	return value
}
