//go:build whatsapp_native

// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/channels/whatsappcommon"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	sqliteDriver   = "sqlite"
	whatsappDBName = "store.db"

	reconnectInitial    = 5 * time.Second
	reconnectMax        = 5 * time.Minute
	reconnectMultiplier = 2.0
	versionFetchTimeout = 15 * time.Second
)

// whatsmeow keeps the WhatsApp Web version in package-level state. Serializing
// refreshes avoids a race if multiple native channel accounts start together.
var waVersionMu sync.Mutex

func refreshWhatsAppWebVersion(ctx context.Context) {
	waVersionMu.Lock()
	defer waVersionMu.Unlock()

	versionCtx, cancel := context.WithTimeout(ctx, versionFetchTimeout)
	defer cancel()

	latest, err := whatsmeow.GetLatestVersion(versionCtx, &http.Client{Timeout: versionFetchTimeout})
	if err != nil {
		logger.WarnCF("whatsapp", "Could not refresh WhatsApp Web client version; using embedded version", map[string]any{
			"error":   err.Error(),
			"version": store.GetWAVersion().String(),
		})
		return
	}

	previous := store.GetWAVersion()
	store.SetWAVersion(*latest)
	logger.InfoCF("whatsapp", "WhatsApp Web client version ready", map[string]any{
		"version":  latest.String(),
		"previous": previous.String(),
	})
}

// WhatsAppNativeChannel implements the WhatsApp channel using whatsmeow (in-process, no external bridge).
type WhatsAppNativeChannel struct {
	*channels.BaseChannel
	config       *config.WhatsAppSettings
	storePath    string
	client       *whatsmeow.Client
	container    *sqlstore.Container
	mu           sync.Mutex
	runCtx       context.Context
	runCancel    context.CancelFunc
	reconnectMu  sync.Mutex
	reconnecting bool
	stopping     atomic.Bool    // set once Stop begins; prevents new wg.Add calls
	wg           sync.WaitGroup // tracks background goroutines (QR handler, reconnect)
}

// NewWhatsAppNativeChannel creates a WhatsApp channel that uses whatsmeow for connection.
// storePath is the directory for the SQLite session store (e.g. workspace/whatsapp).
func NewWhatsAppNativeChannel(
	bc *config.Channel,
	name string,
	cfg *config.WhatsAppSettings,
	bus *bus.MessageBus,
	storePath string,
) (channels.Channel, error) {
	base := channels.NewBaseChannel(name, cfg, bus, bc.AllowFrom, channels.WithMaxMessageLength(65536), channels.WithGroupTrigger(bc.GroupTrigger), channels.WithReasoningChannelID(bc.ReasoningChannelID))
	if storePath == "" {
		storePath = "whatsapp"
	}
	c := &WhatsAppNativeChannel{
		BaseChannel: base,
		config:      cfg,
		storePath:   storePath,
	}
	return c, nil
}

func (c *WhatsAppNativeChannel) Start(ctx context.Context) error {
	logger.InfoCF("whatsapp", "Starting WhatsApp native channel (whatsmeow)", map[string]any{"store": c.storePath})
	refreshWhatsAppWebVersion(ctx)

	// Reset lifecycle state from any previous Stop() so a restarted channel
	// behaves correctly.  Use reconnectMu to be consistent with eventHandler
	// and Stop() which coordinate under the same lock.
	c.reconnectMu.Lock()
	c.stopping.Store(false)
	c.reconnecting = false
	c.reconnectMu.Unlock()

	if err := os.MkdirAll(c.storePath, 0o700); err != nil {
		return fmt.Errorf("create session store dir: %w", err)
	}

	dbPath := filepath.Join(c.storePath, whatsappDBName)
	connStr := "file:" + dbPath + "?_foreign_keys=on"

	db, err := sql.Open(sqliteDriver, connStr)
	if err != nil {
		return fmt.Errorf("open whatsapp store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	waLogger := waLog.Stdout("WhatsApp", "WARN", true)
	container := sqlstore.NewWithDB(db, sqliteDriver, waLogger)
	if err = container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("open whatsapp store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return fmt.Errorf("get device store: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, waLogger)

	// Create runCtx/runCancel BEFORE registering event handler and starting
	// goroutines so that Stop() can cancel them at any time, including during
	// the QR-login flow.
	c.runCtx, c.runCancel = context.WithCancel(ctx)

	client.AddEventHandler(c.eventHandler)

	c.mu.Lock()
	c.container = container
	c.client = client
	c.mu.Unlock()

	// cleanupOnError clears struct references and releases resources when
	// Start() fails after fields are already assigned.  This prevents
	// Stop() from operating on stale references (double-close, disconnect
	// of a partially-initialized client, or stray event handler callbacks).
	startOK := false
	defer func() {
		if startOK {
			return
		}
		c.runCancel()
		client.Disconnect()
		c.mu.Lock()
		c.client = nil
		c.container = nil
		c.mu.Unlock()
		_ = container.Close()
	}()

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(c.runCtx)
		if err != nil {
			return fmt.Errorf("get QR channel: %w", err)
		}
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		// Handle QR events in a background goroutine so Start() returns
		// promptly.  The goroutine is tracked via c.wg and respects
		// c.runCtx for cancellation.
		// Guard wg.Add with reconnectMu + stopping check (same protocol
		// as eventHandler) so a concurrent Stop() cannot enter wg.Wait()
		// while we call wg.Add(1).
		c.reconnectMu.Lock()
		if c.stopping.Load() {
			c.reconnectMu.Unlock()
			return fmt.Errorf("channel stopped during QR setup")
		}
		c.wg.Add(1)
		c.reconnectMu.Unlock()
		go func() {
			defer c.wg.Done()
			for {
				select {
				case <-c.runCtx.Done():
					return
				case evt, ok := <-qrChan:
					if !ok {
						return
					}
					if evt.Event == "code" {
						logger.InfoCF("whatsapp", "Scan this QR code with WhatsApp (Linked Devices):", nil)
						qrterminal.GenerateWithConfig(evt.Code, qrterminal.Config{
							Level:      qrterminal.L,
							Writer:     os.Stdout,
							HalfBlocks: true,
						})
					} else {
						logger.InfoCF("whatsapp", "WhatsApp login event", map[string]any{"event": evt.Event})
					}
				}
			}
		}()
	} else {
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	}

	startOK = true
	c.SetRunning(true)
	logger.InfoC("whatsapp", "WhatsApp native channel connected")
	return nil
}

func (c *WhatsAppNativeChannel) Stop(ctx context.Context) error {
	logger.InfoC("whatsapp", "Stopping WhatsApp native channel")

	// Mark as stopping under reconnectMu so the flag is visible to
	// eventHandler atomically with respect to its wg.Add(1) call.
	// This closes the TOCTOU window where eventHandler could check
	// stopping (false), then Stop sets it true + enters wg.Wait,
	// then eventHandler calls wg.Add(1) — causing a panic.
	c.reconnectMu.Lock()
	c.stopping.Store(true)
	c.reconnectMu.Unlock()

	if c.runCancel != nil {
		c.runCancel()
	}

	// Disconnect the client first so any blocking Connect()/reconnect loops
	// can be interrupted before we wait on the goroutines.
	c.mu.Lock()
	client := c.client
	container := c.container
	c.mu.Unlock()

	if client != nil {
		client.Disconnect()
	}

	// Wait for background goroutines (QR handler, reconnect) to finish in a
	// context-aware way so Stop can be bounded by ctx.
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines have finished.
	case <-ctx.Done():
		// Context canceled or timed out; log and proceed with best-effort cleanup.
		logger.WarnC("whatsapp", fmt.Sprintf("Stop context canceled before all goroutines finished: %v", ctx.Err()))
	}

	// Now it is safe to clear and close resources.
	c.mu.Lock()
	c.client = nil
	c.container = nil
	c.mu.Unlock()

	if container != nil {
		_ = container.Close()
	}
	c.SetRunning(false)
	return nil
}

func (c *WhatsAppNativeChannel) eventHandler(evt any) {
	switch v := evt.(type) {
	case *events.Message:
		c.handleIncoming(v)
	case *events.Disconnected:
		logger.InfoCF("whatsapp", "WhatsApp disconnected, will attempt reconnection", nil)
		c.reconnectMu.Lock()
		if c.reconnecting {
			c.reconnectMu.Unlock()
			return
		}
		// Check stopping while holding the lock so the check and wg.Add
		// are atomic with respect to Stop() setting the flag + calling
		// wg.Wait(). This prevents the TOCTOU race.
		if c.stopping.Load() {
			c.reconnectMu.Unlock()
			return
		}
		c.reconnecting = true
		c.wg.Add(1)
		c.reconnectMu.Unlock()
		go func() {
			defer c.wg.Done()
			c.reconnectWithBackoff()
		}()
	}
}

func (c *WhatsAppNativeChannel) reconnectWithBackoff() {
	defer func() {
		c.reconnectMu.Lock()
		c.reconnecting = false
		c.reconnectMu.Unlock()
	}()

	backoff := reconnectInitial
	for {
		select {
		case <-c.runCtx.Done():
			return
		default:
		}

		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client == nil {
			return
		}

		logger.InfoCF("whatsapp", "WhatsApp reconnecting", map[string]any{"backoff": backoff.String()})
		err := client.Connect()
		if err == nil {
			logger.InfoC("whatsapp", "WhatsApp reconnected")
			return
		}

		logger.WarnCF("whatsapp", "WhatsApp reconnect failed", map[string]any{"error": err.Error()})

		select {
		case <-c.runCtx.Done():
			return
		case <-time.After(backoff):
			if backoff < reconnectMax {
				next := time.Duration(float64(backoff) * reconnectMultiplier)
				if next > reconnectMax {
					next = reconnectMax
				}
				backoff = next
			}
		}
	}
}

func (c *WhatsAppNativeChannel) handleIncoming(evt *events.Message) {
	if evt.Message == nil {
		return
	}
	senderID := evt.Info.Sender.String()
	chatID := evt.Info.Chat.String()
	content := evt.Message.GetConversation()
	if content == "" && evt.Message.ExtendedTextMessage != nil {
		content = evt.Message.ExtendedTextMessage.GetText()
	}
	content = utils.SanitizeMessageContent(content)

	var mediaPaths []string
	eventType := ""
	if reaction := evt.Message.GetReactionMessage(); reaction != nil && content == "" {
		content = "[reaction] " + reaction.GetText()
		eventType = "reaction"
	}
	if poll := evt.Message.GetPollCreationMessage(); poll != nil && content == "" {
		content = "[poll] " + poll.GetName()
		eventType = "poll"
	}
	if evt.Message.GetPollUpdateMessage() != nil && content == "" {
		content = "[poll_answer]"
		eventType = "poll_answer"
	}

	metadata := make(map[string]string)
	metadata["message_id"] = evt.Info.ID
	if eventType != "" {
		metadata["event_type"] = eventType
	}
	if evt.Info.PushName != "" {
		metadata["user_name"] = evt.Info.PushName
	}
	if evt.Info.Chat.Server == types.GroupServer {
		metadata["peer_kind"] = "group"
		metadata["peer_id"] = chatID
	} else {
		metadata["peer_kind"] = "direct"
		metadata["peer_id"] = senderID
	}

	peerKind := "direct"
	if evt.Info.Chat.Server == types.GroupServer {
		peerKind = "group"
	}
	messageID := evt.Info.ID
	sender := bus.SenderInfo{
		Platform:    "whatsapp",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("whatsapp", senderID),
		DisplayName: evt.Info.PushName,
	}

	if !whatsappcommon.Allowed(c.config, c.IsAllowedSender(sender), senderID, chatID, evt.Info.Chat.Server == types.GroupServer) {
		return
	}
	if evt.Info.Chat.Server == types.GroupServer {
		mentioned := false
		if c.client != nil && c.client.Store.ID != nil {
			mentioned = strings.Contains(strings.ToLower(content), strings.ToLower(c.client.Store.ID.User))
		}
		respond, cleaned := c.ShouldRespondInGroupWithMentionOverride(mentioned, content, whatsappcommon.RequireMention(c.config, chatID))
		if !respond {
			return
		}
		content = cleaned
	}
	if ref := c.downloadIncomingMedia(evt); ref != "" {
		mediaPaths = append(mediaPaths, ref)
	}
	if content == "" && len(mediaPaths) > 0 {
		content = "[media]"
	}
	if content == "" {
		return
	}

	logger.DebugCF(
		"whatsapp",
		"WhatsApp message received",
		map[string]any{"sender_id": senderID, "content_preview": utils.Truncate(content, 50)},
	)

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    chatID,
		SenderID:  senderID,
		MessageID: messageID,
		ChatType:  peerKind,
		Raw:       metadata,
	}
	if evt.Message.ExtendedTextMessage != nil && evt.Message.ExtendedTextMessage.ContextInfo != nil {
		inboundCtx.ReplyToMessageID = evt.Message.ExtendedTextMessage.ContextInfo.GetStanzaID()
	}

	if c.config != nil && c.config.SendReadReceipts && c.client != nil {
		_ = c.client.MarkRead(c.runCtx, []types.MessageID{evt.Info.ID}, evt.Info.Timestamp, evt.Info.Chat, evt.Info.Sender)
	}
	if c.config != nil && c.config.AckReaction != "" && c.client != nil {
		_, _ = c.client.SendMessage(c.runCtx, evt.Info.Chat, c.client.BuildReaction(evt.Info.Chat, evt.Info.Sender, evt.Info.ID, c.config.AckReaction))
	}
	c.HandleInboundContext(c.runCtx, chatID, content, mediaPaths, inboundCtx, sender)
}

func (c *WhatsAppNativeChannel) downloadIncomingMedia(evt *events.Message) string {
	if c.client == nil || c.GetMediaStore() == nil {
		return ""
	}
	var downloadable whatsmeow.DownloadableMessage
	filename, contentType := "whatsapp-media", "application/octet-stream"
	switch {
	case evt.Message.GetImageMessage() != nil:
		downloadable = evt.Message.GetImageMessage()
		filename, contentType = "image.jpg", evt.Message.GetImageMessage().GetMimetype()
	case evt.Message.GetAudioMessage() != nil:
		downloadable = evt.Message.GetAudioMessage()
		filename, contentType = "audio.ogg", evt.Message.GetAudioMessage().GetMimetype()
	case evt.Message.GetVideoMessage() != nil:
		downloadable = evt.Message.GetVideoMessage()
		filename, contentType = "video.mp4", evt.Message.GetVideoMessage().GetMimetype()
	case evt.Message.GetDocumentMessage() != nil:
		downloadable = evt.Message.GetDocumentMessage()
		filename, contentType = evt.Message.GetDocumentMessage().GetFileName(), evt.Message.GetDocumentMessage().GetMimetype()
	default:
		return ""
	}
	if downloadable == nil {
		return ""
	}
	maxBytes := whatsappcommon.MediaMaxBytes(c.config)
	if maxBytes > 0 {
		if sized, ok := downloadable.(interface{ GetFileLength() uint64 }); ok && sized.GetFileLength() > uint64(maxBytes) {
			return ""
		}
	}
	tmp, err := os.CreateTemp("", "picoclaw-whatsapp-*")
	if err != nil {
		return ""
	}
	path := tmp.Name()
	defer func() { _ = tmp.Close() }()
	if err := c.client.DownloadToFile(c.runCtx, downloadable, tmp); err != nil {
		return ""
	}
	if info, err := tmp.Stat(); err != nil || (maxBytes > 0 && info.Size() > maxBytes) {
		return ""
	}
	ref, err := c.GetMediaStore().Store(path, media.MediaMeta{Filename: filename, ContentType: contentType, Source: "whatsapp", CleanupPolicy: media.CleanupPolicyDeleteOnCleanup}, channels.BuildMediaScope("whatsapp", evt.Info.Chat.String(), evt.Info.ID))
	if err != nil {
		return ""
	}
	return ref
}

func (c *WhatsAppNativeChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("whatsapp connection not established: %w", channels.ErrTemporary)
	}

	// Detect unpaired state: the client is connected (to WhatsApp servers)
	// but has not completed QR-login yet, so sending would fail.
	if client.Store.ID == nil {
		return nil, fmt.Errorf("whatsapp not yet paired (QR login pending): %w", channels.ErrTemporary)
	}

	to, err := parseJID(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat id %q: %w", msg.ChatID, err)
	}

	waMsg := &waE2E.Message{Conversation: proto.String(msg.Content)}
	if msg.Poll != nil {
		waMsg = c.client.BuildPollCreation(msg.Poll.Question, msg.Poll.Options, 1)
	}
	if msg.ReplyToMessageID != "" {
		waMsg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{Text: proto.String(msg.Content), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(msg.ReplyToMessageID)}}
		waMsg.Conversation = nil
	}

	resp, err := client.SendMessage(ctx, to, waMsg)
	if err != nil {
		return nil, fmt.Errorf("whatsapp send: %w", channels.ErrTemporary)
	}
	return []string{string(resp.ID)}, nil
}

func (c *WhatsAppNativeChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	to, err := parseJID(chatID)
	if err != nil {
		return func() {}, err
	}
	if err := c.client.SendChatPresence(ctx, to, types.ChatPresenceComposing, types.ChatPresenceMediaText); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = c.client.SendChatPresence(context.Background(), to, types.ChatPresencePaused, types.ChatPresenceMediaText)
		})
	}, nil
}

func (c *WhatsAppNativeChannel) ReactToMessage(ctx context.Context, chatID, messageID string) (func(), error) {
	to, err := parseJID(chatID)
	if err != nil {
		return func() {}, err
	}
	target := types.MessageID(messageID)
	if _, err = c.client.SendMessage(ctx, to, c.client.BuildReaction(to, to, target, "👀")); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_, _ = c.client.SendMessage(context.Background(), to, c.client.BuildReaction(to, to, target, ""))
		})
	}, nil
}

func (c *WhatsAppNativeChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}
	to, err := parseJID(msg.ChatID)
	if err != nil {
		return nil, err
	}
	maxBytes := whatsappcommon.MediaMaxBytes(c.config)
	ids := make([]string, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		path, err := store.Resolve(part.Ref)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if maxBytes > 0 && info.Size() > maxBytes {
			return nil, fmt.Errorf("whatsapp media exceeds configured limit")
		}
		input, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		temp, err := os.CreateTemp("", "picoclaw-whatsapp-upload-*")
		if err != nil {
			_ = input.Close()
			return nil, err
		}
		mediaType := whatsmeow.MediaDocument
		switch part.Type {
		case "image":
			mediaType = whatsmeow.MediaImage
		case "audio":
			mediaType = whatsmeow.MediaAudio
		case "video":
			mediaType = whatsmeow.MediaVideo
		}
		resp, err := c.client.UploadReader(ctx, input, temp, mediaType)
		_ = input.Close()
		_ = temp.Close()
		_ = os.Remove(temp.Name())
		if err != nil {
			return nil, err
		}
		mimeType := part.ContentType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		var waMsg *waE2E.Message
		switch part.Type {
		case "image":
			waMsg = &waE2E.Message{ImageMessage: &waE2E.ImageMessage{URL: &resp.URL, DirectPath: &resp.DirectPath, MediaKey: resp.MediaKey, FileSHA256: resp.FileSHA256, FileEncSHA256: resp.FileEncSHA256, FileLength: &resp.FileLength, Mimetype: proto.String(mimeType), Caption: proto.String(part.Caption)}}
		case "audio":
			waMsg = &waE2E.Message{AudioMessage: &waE2E.AudioMessage{URL: &resp.URL, DirectPath: &resp.DirectPath, MediaKey: resp.MediaKey, FileSHA256: resp.FileSHA256, FileEncSHA256: resp.FileEncSHA256, FileLength: &resp.FileLength, Mimetype: proto.String(mimeType)}}
		case "video":
			waMsg = &waE2E.Message{VideoMessage: &waE2E.VideoMessage{URL: &resp.URL, DirectPath: &resp.DirectPath, MediaKey: resp.MediaKey, FileSHA256: resp.FileSHA256, FileEncSHA256: resp.FileEncSHA256, FileLength: &resp.FileLength, Mimetype: proto.String(mimeType), Caption: proto.String(part.Caption)}}
		default:
			name := part.Filename
			waMsg = &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{URL: &resp.URL, DirectPath: &resp.DirectPath, MediaKey: resp.MediaKey, FileSHA256: resp.FileSHA256, FileEncSHA256: resp.FileEncSHA256, FileLength: &resp.FileLength, Mimetype: proto.String(mimeType), FileName: &name, Caption: proto.String(part.Caption)}}
		}
		respMsg, err := c.client.SendMessage(ctx, to, waMsg)
		if err != nil {
			return nil, err
		}
		ids = append(ids, string(respMsg.ID))
	}
	return ids, nil
}

// parseJID converts a chat ID (phone number or JID string) to types.JID.
func parseJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, fmt.Errorf("empty chat id")
	}
	if strings.Contains(s, "@") {
		return types.ParseJID(s)
	}
	return types.NewJID(s, types.DefaultUserServer), nil
}
