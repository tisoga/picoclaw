package channels

import (
	"encoding/json"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func whatsappChannelConfig(t *testing.T, useNative bool) *config.Channel {
	t.Helper()
	settings, err := json.Marshal(config.WhatsAppSettings{
		UseNative: useNative,
		BridgeURL: "ws://localhost:3001",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &config.Channel{
		Enabled:  true,
		Type:     config.ChannelWhatsApp,
		Settings: settings,
	}
}

func TestEffectiveChannelTypeUsesNativeWhatsAppFactory(t *testing.T) {
	channel := whatsappChannelConfig(t, true)
	if got := effectiveChannelType("whatsapp", channel); got != config.ChannelWhatsAppNative {
		t.Fatalf("effectiveChannelType() = %q, want %q", got, config.ChannelWhatsAppNative)
	}
}

func TestEffectiveChannelTypeUsesWhatsAppBridgeWhenNativeDisabled(t *testing.T) {
	channel := whatsappChannelConfig(t, false)
	if got := effectiveChannelType("whatsapp", channel); got != config.ChannelWhatsApp {
		t.Fatalf("effectiveChannelType() = %q, want %q", got, config.ChannelWhatsApp)
	}
}

func TestWhatsAppNativeKeepsRuntimeChannelName(t *testing.T) {
	channel := whatsappChannelConfig(t, true)
	cfg := &config.Config{
		Channels: config.ChannelsConfig{"whatsapp": channel},
	}
	manager := &Manager{config: cfg}
	if _, ready := manager.getChannelConfigAndEnabled("whatsapp"); !ready {
		t.Fatal("native WhatsApp configuration was not considered ready")
	}
	if _, exists := cfg.Channels["whatsapp_native"]; exists {
		t.Fatal("native selection created a separate runtime channel name")
	}
}
