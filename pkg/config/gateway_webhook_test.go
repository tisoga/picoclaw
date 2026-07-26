package config

import "testing"

func TestGatewayWebhookConfigIsIPAllowed(t *testing.T) {
	cfg := GatewayWebhookConfig{
		AllowedIPs: []string{"192.168.1.198", "10.20.0.0/16", "2001:db8::/32"},
	}

	tests := []struct {
		remote string
		want   bool
	}{
		{remote: "192.168.1.198:62340", want: true},
		{remote: "192.168.1.199:62340", want: false},
		{remote: "10.20.4.9:1234", want: true},
		{remote: "[2001:db8::10]:443", want: true},
		{remote: "invalid", want: false},
	}
	for _, test := range tests {
		if got := cfg.IsIPAllowed(test.remote); got != test.want {
			t.Errorf("IsIPAllowed(%q) = %v, want %v", test.remote, got, test.want)
		}
	}
}

func TestGatewayWebhookConfigAllowAllIPs(t *testing.T) {
	cfg := GatewayWebhookConfig{AllowAllIPs: true}
	if !cfg.IsIPAllowed("203.0.113.10:1234") {
		t.Fatal("AllowAllIPs did not authorize remote address")
	}
}
