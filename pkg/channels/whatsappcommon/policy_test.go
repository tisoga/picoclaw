package whatsappcommon

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestAllowedSeparatesGroupMembershipAndSenderPolicy(t *testing.T) {
	cfg := &config.WhatsAppSettings{DMPolicy: "allowlist", GroupPolicy: "allowlist", GroupAllowFrom: config.FlexibleStringSlice{"100"}, Groups: map[string]config.WhatsAppGroupSettings{"group@g.us": {}}}
	if !Allowed(cfg, true, "100@s.whatsapp.net", "group@g.us", true) {
		t.Fatal("expected allowed group sender")
	}
	if Allowed(cfg, true, "200@s.whatsapp.net", "group@g.us", true) {
		t.Fatal("unexpected unauthorized group sender")
	}
	if Allowed(cfg, true, "100@s.whatsapp.net", "other@g.us", true) {
		t.Fatal("unexpected group outside membership allowlist")
	}
	if Allowed(cfg, false, "100@s.whatsapp.net", "100@s.whatsapp.net", false) {
		t.Fatal("allowlist DM should still use base sender policy")
	}
}
