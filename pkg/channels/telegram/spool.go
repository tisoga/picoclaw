package telegram

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	"github.com/sipeed/picoclaw/pkg/config"

	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// spoolMessage represents a queued Telegram message.
type spoolMessage struct {
	ID        int64
	MessageID int
	ChatID    int64
	Payload   []byte
	CreatedAt time.Time
	Attempts  int
}

// Spool provides a persistent SQLite-backed queue for incoming Telegram updates.
// This ensures that if the agent restarts or crashes during processing, the
// message is not lost.
type Spool struct {
	db *sql.DB
	mu sync.Mutex
}

// NewSpool initializes the ingress spool database.
func NewSpool() (*Spool, error) {
	dbPath := filepath.Join(config.GetHome(), "telegram_ingress.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open spool db: %w", err)
	}

	// Set pragmas for better concurrency and durability
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")
	db.Exec("PRAGMA foreign_keys=ON;")

	// Create table
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS spool (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		message_id INTEGER NOT NULL,
		chat_id INTEGER NOT NULL,
		payload BLOB NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		attempts INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_spool_created ON spool(created_at);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create spool table: %w", err)
	}

	return &Spool{db: db}, nil
}

// Enqueue saves a message to the database before processing.
func (s *Spool) Enqueue(msg *telego.Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.db.Exec(
		"INSERT INTO spool (message_id, chat_id, payload) VALUES (?, ?, ?)",
		msg.MessageID, msg.Chat.ID, payload,
	)
	return err
}

// Dequeue retrieves the oldest message from the spool that needs processing.
func (s *Spool) Dequeue() (*spoolMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(`
		SELECT id, message_id, chat_id, payload, created_at, attempts 
		FROM spool 
		ORDER BY created_at ASC 
		LIMIT 1
	`)

	var msg spoolMessage
	err := row.Scan(&msg.ID, &msg.MessageID, &msg.ChatID, &msg.Payload, &msg.CreatedAt, &msg.Attempts)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Empty queue
		}
		return nil, err
	}

	// Increment attempts immediately so if it crashes, we know we tried.
	s.db.Exec("UPDATE spool SET attempts = attempts + 1 WHERE id = ?", msg.ID)

	return &msg, nil
}

// Acknowledge removes a successfully processed message from the spool.
func (s *Spool) Acknowledge(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM spool WHERE id = ?", id)
	return err
}

// Close gracefully closes the database connection.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}
