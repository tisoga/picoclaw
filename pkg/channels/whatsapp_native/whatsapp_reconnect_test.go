//go:build whatsapp_native

package whatsapp

import (
	"errors"
	"testing"
)

type fakeReconnectClient struct {
	connected       bool
	connectErr      error
	connectCalls    int
	connectRestores bool
}

func (c *fakeReconnectClient) IsConnected() bool { return c.connected }

func (c *fakeReconnectClient) Connect() error {
	c.connectCalls++
	if c.connectRestores {
		c.connected = true
	}
	return c.connectErr
}

func TestEnsureWhatsAppConnectedSkipsConnectWhenAlreadyConnected(t *testing.T) {
	client := &fakeReconnectClient{connected: true}
	if err := ensureWhatsAppConnected(client); err != nil {
		t.Fatalf("ensureWhatsAppConnected() error = %v", err)
	}
	if client.connectCalls != 0 {
		t.Fatalf("Connect() calls = %d, want 0", client.connectCalls)
	}
}

func TestEnsureWhatsAppConnectedAcceptsConcurrentRecovery(t *testing.T) {
	client := &fakeReconnectClient{
		connectErr:      errors.New("websocket is already connected"),
		connectRestores: true,
	}
	if err := ensureWhatsAppConnected(client); err != nil {
		t.Fatalf("ensureWhatsAppConnected() error = %v", err)
	}
	if client.connectCalls != 1 {
		t.Fatalf("Connect() calls = %d, want 1", client.connectCalls)
	}
}

func TestEnsureWhatsAppConnectedReturnsRealFailure(t *testing.T) {
	want := errors.New("network unavailable")
	client := &fakeReconnectClient{connectErr: want}
	if err := ensureWhatsAppConnected(client); !errors.Is(err, want) {
		t.Fatalf("ensureWhatsAppConnected() error = %v, want %v", err, want)
	}
	if client.connectCalls != 1 {
		t.Fatalf("Connect() calls = %d, want 1", client.connectCalls)
	}
}
