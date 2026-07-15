package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/channels/whatsappcommon"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type WhatsAppChannel struct {
	*channels.BaseChannel
	conn      *websocket.Conn
	config    *config.WhatsAppSettings
	url       string
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	connected bool
}

func NewWhatsAppChannel(
	bc *config.Channel,
	cfg *config.WhatsAppSettings,
	bus *bus.MessageBus,
) (*WhatsAppChannel, error) {
	base := channels.NewBaseChannel(
		"whatsapp",
		cfg,
		bus,
		bc.AllowFrom,
		channels.WithMaxMessageLength(65536),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	return &WhatsAppChannel{
		BaseChannel: base,
		config:      cfg,
		url:         cfg.BridgeURL,
		connected:   false,
	}, nil
}

func (c *WhatsAppChannel) dialBridge() error {
	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second
	conn, resp, err := dialer.Dial(c.url, nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.conn
	c.conn = conn
	c.connected = true
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (c *WhatsAppChannel) Start(ctx context.Context) error {
	logger.InfoCF("whatsapp", "Starting WhatsApp channel", map[string]any{
		"bridge_url": c.url,
	})

	c.ctx, c.cancel = context.WithCancel(ctx)

	if err := c.dialBridge(); err != nil {
		c.cancel()
		return fmt.Errorf("failed to connect to WhatsApp bridge: %w", err)
	}

	c.SetRunning(true)
	logger.InfoC("whatsapp", "WhatsApp channel connected")

	go c.listen()

	return nil
}

func (c *WhatsAppChannel) writePayload(ctx context.Context, payload map[string]any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("whatsapp connection not established: %w", channels.ErrTemporary)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = c.conn.WriteMessage(websocket.TextMessage, data)
	_ = c.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("whatsapp send: %w", channels.ErrTemporary)
	}
	return nil
}

func (c *WhatsAppChannel) Stop(ctx context.Context) error {
	logger.InfoC("whatsapp", "Stopping WhatsApp channel...")

	// Cancel context first to signal listen goroutine to exit
	if c.cancel != nil {
		c.cancel()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			logger.ErrorCF("whatsapp", "Error closing WhatsApp connection", map[string]any{
				"error": err.Error(),
			})
		}
		c.conn = nil
	}

	c.connected = false
	c.SetRunning(false)

	return nil
}

func (c *WhatsAppChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	requestID := "pico-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	payload := map[string]any{
		"type": "message", "id": requestID, "to": msg.ChatID, "content": msg.Content,
	}
	if msg.ReplyToMessageID != "" {
		payload["reply_to"] = msg.ReplyToMessageID
	}
	if msg.Poll != nil {
		payload["type"] = "poll"
		payload["question"] = msg.Poll.Question
		payload["options"] = msg.Poll.Options
		payload["anonymous"] = msg.Poll.Anonymous
	}
	if err := c.writePayload(ctx, payload); err != nil {
		return nil, err
	}
	return []string{requestID}, nil
}

func (c *WhatsAppChannel) listen() {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()

			if conn == nil {
				time.Sleep(1 * time.Second)
				continue
			}

			_, message, err := conn.ReadMessage()
			if err != nil {
				logger.ErrorCF("whatsapp", "WhatsApp read error", map[string]any{
					"error": err.Error(),
				})
				c.mu.Lock()
				if c.conn == conn {
					c.conn = nil
					c.connected = false
				}
				c.mu.Unlock()
				_ = conn.Close()
				backoff := time.Second
				for c.ctx.Err() == nil {
					if dialErr := c.dialBridge(); dialErr == nil {
						logger.InfoC("whatsapp", "WhatsApp bridge reconnected")
						break
					}
					timer := time.NewTimer(backoff)
					select {
					case <-c.ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
					if backoff < 30*time.Second {
						backoff *= 2
					}
				}
				continue
			}

			var msg map[string]any
			if err := json.Unmarshal(message, &msg); err != nil {
				logger.ErrorCF("whatsapp", "Failed to unmarshal WhatsApp message", map[string]any{
					"error": err.Error(),
				})
				continue
			}

			msgType, ok := msg["type"].(string)
			if !ok {
				continue
			}

			if msgType == "message" || msgType == "reaction" || msgType == "poll_answer" {
				if msgType != "message" {
					if _, exists := msg["content"]; !exists {
						msg["content"] = "[" + msgType + "]"
					}
					msg["event_type"] = msgType
				}
				c.handleIncomingMessage(msg)
			}
		}
	}
}

func (c *WhatsAppChannel) handleIncomingMessage(msg map[string]any) {
	senderID, ok := msg["from"].(string)
	if !ok {
		return
	}

	chatID, ok := msg["chat"].(string)
	if !ok {
		chatID = senderID
	}

	content, ok := msg["content"].(string)
	if !ok {
		content = ""
	}

	var mediaPaths []string
	if mediaData, ok := msg["media"].([]any); ok {
		mediaPaths = make([]string, 0, len(mediaData))
		for _, m := range mediaData {
			if path, ok := m.(string); ok {
				mediaPaths = append(mediaPaths, path)
			}
		}
	}

	metadata := make(map[string]string)
	var messageID string
	if mid, ok := msg["id"].(string); ok {
		messageID = mid
	}
	if userName, ok := msg["from_name"].(string); ok {
		metadata["user_name"] = userName
	}
	if eventType, ok := msg["event_type"].(string); ok {
		metadata["event_type"] = eventType
	}

	logger.InfoCF("whatsapp", "WhatsApp message received", map[string]any{
		"sender":  senderID,
		"preview": utils.Truncate(content, 50),
	})

	sender := bus.SenderInfo{
		Platform:    "whatsapp",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("whatsapp", senderID),
	}
	if display, ok := metadata["user_name"]; ok {
		sender.DisplayName = display
	}

	if !c.IsAllowedSender(sender) {
		return
	}
	isGroup := chatID != senderID
	if !whatsappcommon.Allowed(c.config, true, senderID, chatID, isGroup) {
		return
	}
	if isGroup {
		mentioned, _ := msg["mentioned"].(bool)
		respond, cleaned := c.ShouldRespondInGroupWithMentionOverride(mentioned, content, whatsappcommon.RequireMention(c.config, chatID))
		if !respond {
			return
		}
		content = cleaned
	}
	inboundCtx := bus.InboundContext{
		Channel:   "whatsapp",
		ChatID:    chatID,
		SenderID:  senderID,
		MessageID: messageID,
		Raw:       metadata,
	}
	if chatID == senderID {
		inboundCtx.ChatType = "direct"
	} else {
		inboundCtx.ChatType = "group"
	}
	if replyTo, ok := msg["reply_to"].(string); ok {
		inboundCtx.ReplyToMessageID = replyTo
	}
	if c.config != nil && c.config.SendReadReceipts && messageID != "" {
		_ = c.writePayload(c.ctx, map[string]any{"type": "read", "chat": chatID, "id": messageID})
	}
	if c.config != nil && c.config.AckReaction != "" && messageID != "" {
		_ = c.writePayload(c.ctx, map[string]any{"type": "reaction", "chat": chatID, "id": messageID, "emoji": c.config.AckReaction})
	}

	c.HandleInboundContext(c.ctx, chatID, content, mediaPaths, inboundCtx, sender)
}

func (c *WhatsAppChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	if err := c.writePayload(ctx, map[string]any{"type": "typing", "chat": chatID, "active": true}); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = c.writePayload(context.Background(), map[string]any{"type": "typing", "chat": chatID, "active": false})
		})
	}, nil
}

func (c *WhatsAppChannel) ReactToMessage(ctx context.Context, chatID, messageID string) (func(), error) {
	if err := c.writePayload(ctx, map[string]any{"type": "reaction", "chat": chatID, "id": messageID, "emoji": "👀"}); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = c.writePayload(context.Background(), map[string]any{"type": "reaction", "chat": chatID, "id": messageID, "emoji": ""})
		})
	}, nil
}

func (c *WhatsAppChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}
	maxBytes := whatsappcommon.MediaMaxBytes(c.config)
	parts := make([]map[string]any, 0, len(msg.Parts))
	for _, part := range msg.Parts {
		path, err := store.Resolve(part.Ref)
		if err != nil {
			return nil, err
		}
		if info, err := os.Stat(path); err != nil {
			return nil, err
		} else if maxBytes > 0 && info.Size() > maxBytes {
			return nil, fmt.Errorf("whatsapp media exceeds configured limit")
		}
		parts = append(parts, map[string]any{"type": part.Type, "path": path, "caption": part.Caption, "filename": part.Filename, "content_type": part.ContentType})
	}
	requestID := "pico-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := c.writePayload(ctx, map[string]any{"type": "media", "id": requestID, "to": msg.ChatID, "media": parts}); err != nil {
		return nil, err
	}
	return []string{requestID}, nil
}
