package telegram

import (
	"crypto/sha256"
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
	UpdateID  sql.NullInt64
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
func NewSpool(channelName string) (*Spool, error) {
	dbPath := telegramSpoolPath(channelName)
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
		update_id INTEGER,
		message_id INTEGER NOT NULL,
		chat_id INTEGER NOT NULL,
		payload BLOB NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		attempts INTEGER DEFAULT 0,
		completed INTEGER NOT NULL DEFAULT 0,
		completed_at DATETIME
	);
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create spool table: %w", err)
	}

	// Migrate databases created before update_id was introduced. SQLite has no
	// portable IF NOT EXISTS form for ADD COLUMN, so duplicate-column is ignored.
	_, _ = db.Exec("ALTER TABLE spool ADD COLUMN update_id INTEGER")
	_, _ = db.Exec("ALTER TABLE spool ADD COLUMN completed INTEGER NOT NULL DEFAULT 0")
	_, _ = db.Exec("ALTER TABLE spool ADD COLUMN completed_at DATETIME")
	if _, err = db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_spool_id ON spool(id);
		CREATE INDEX IF NOT EXISTS idx_spool_pending ON spool(completed, id);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_spool_update_id
			ON spool(update_id) WHERE update_id IS NOT NULL;
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create spool indexes: %w", err)
	}
	// Telegram does not retain ordinary updates indefinitely. Keeping completed
	// update IDs for seven days covers realistic redelivery windows without
	// allowing the tombstone table to grow forever.
	_, _ = db.Exec(`
		DELETE FROM spool
		WHERE completed = 1 AND completed_at < datetime('now', '-7 days')
	`)

	return &Spool{db: db}, nil
}

func telegramSpoolPath(channelName string) string {
	// Preserve the legacy path for the default channel so queued updates survive
	// an upgrade. Named Telegram accounts receive isolated databases.
	if channelName == "" || channelName == "telegram" {
		return filepath.Join(config.GetHome(), "telegram_ingress.db")
	}
	sum := sha256.Sum256([]byte(channelName))
	return filepath.Join(config.GetHome(), fmt.Sprintf("telegram_ingress_%x.db", sum[:8]))
}

// Enqueue saves a message to the database before processing.
func (s *Spool) Enqueue(updateID int, msg *telego.Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.db.Exec(
		"INSERT OR IGNORE INTO spool (update_id, message_id, chat_id, payload) VALUES (?, ?, ?, ?)",
		updateID, msg.MessageID, msg.Chat.ID, payload,
	)
	return err
}

// Dequeue retrieves the oldest message from the spool that needs processing.
func (s *Spool) Dequeue() (*spoolMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	row := s.db.QueryRow(`
		SELECT id, update_id, message_id, chat_id, payload, created_at, attempts
		FROM spool
		WHERE completed = 0
		ORDER BY id ASC
		LIMIT 1
	`)

	var msg spoolMessage
	err := row.Scan(
		&msg.ID,
		&msg.UpdateID,
		&msg.MessageID,
		&msg.ChatID,
		&msg.Payload,
		&msg.CreatedAt,
		&msg.Attempts,
	)
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

// Acknowledge tombstones a successfully processed message. Retaining update_id
// prevents Telegram crash-window redelivery from running the same turn twice.
func (s *Spool) Acknowledge(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`
		UPDATE spool
		SET completed = 1, completed_at = CURRENT_TIMESTAMP, payload = X''
		WHERE id = ?
	`, id)
	return err
}

// Close gracefully closes the database connection.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}
