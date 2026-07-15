package telegram

import (
	"path/filepath"
	"testing"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestTelegramSpoolPathIsolatesNamedChannels(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	defaultPath := telegramSpoolPath("telegram")
	firstPath := telegramSpoolPath("work")
	secondPath := telegramSpoolPath("personal")

	if defaultPath != filepath.Join(home, "telegram_ingress.db") {
		t.Fatalf("default spool path = %q", defaultPath)
	}
	if firstPath == secondPath || firstPath == defaultPath || secondPath == defaultPath {
		t.Fatalf("named Telegram channels must have isolated spool paths")
	}
}

func TestSpoolDeduplicatesUpdateIDsAndPreservesInsertionOrder(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	spool, err := NewSpool("telegram")
	if err != nil {
		t.Fatalf("NewSpool error: %v", err)
	}
	defer spool.Close()

	first := &telego.Message{MessageID: 20, Chat: telego.Chat{ID: 100}}
	duplicate := &telego.Message{MessageID: 999, Chat: telego.Chat{ID: 999}}
	second := &telego.Message{MessageID: 10, Chat: telego.Chat{ID: 100}}

	if err := spool.Enqueue(7, first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := spool.Enqueue(7, duplicate); err != nil {
		t.Fatalf("enqueue duplicate: %v", err)
	}
	if err := spool.Enqueue(8, second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	row, err := spool.Dequeue()
	if err != nil {
		t.Fatalf("dequeue first: %v", err)
	}
	if row == nil || row.MessageID != first.MessageID {
		t.Fatalf("first row = %#v, want message %d", row, first.MessageID)
	}
	if err := spool.Acknowledge(row.ID); err != nil {
		t.Fatalf("ack first: %v", err)
	}

	row, err = spool.Dequeue()
	if err != nil {
		t.Fatalf("dequeue second: %v", err)
	}
	if row == nil || row.MessageID != second.MessageID {
		t.Fatalf("second row = %#v, want message %d", row, second.MessageID)
	}
	if err := spool.Acknowledge(row.ID); err != nil {
		t.Fatalf("ack second: %v", err)
	}

	row, err = spool.Dequeue()
	if err != nil {
		t.Fatalf("dequeue empty: %v", err)
	}
	if row != nil {
		t.Fatalf("duplicate update was inserted: %#v", row)
	}
}
