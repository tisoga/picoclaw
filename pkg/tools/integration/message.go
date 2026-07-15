package integrationtools

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/h2non/filetype"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	fstools "github.com/sipeed/picoclaw/pkg/tools/fs"
)

type SendCallbackWithContext func(
	ctx context.Context,
	channel, chatID, content, replyToMessageID string,
	mediaParts []bus.MediaPart,
) error

type InteractiveSendCallback func(ctx context.Context, channel, chatID, content, replyToMessageID string, buttons [][]bus.InlineButton, poll *bus.OutboundPoll) error

type messageMediaArg struct {
	Path     string
	Type     string
	Filename string
}

// sentTarget records the channel+chatID that the message tool sent to.
type sentTarget struct {
	Channel string
	ChatID  string
}

type MessageTool struct {
	sendCallback        SendCallbackWithContext
	interactiveCallback InteractiveSendCallback
	workspace           string
	restrict            bool
	maxFileSize         int
	mediaStore          media.MediaStore
	allowPaths          []*regexp.Regexp
	localMediaEnabled   bool
	mu                  sync.Mutex
	sentTargets         map[string][]sentTarget
}

func NewMessageTool() *MessageTool {
	return &MessageTool{
		sentTargets: make(map[string][]sentTarget),
	}
}

func (t *MessageTool) Name() string {
	return "message"
}

func (t *MessageTool) Description() string {
	if !t.localMediaEnabled {
		return "Send a text message to the user on a chat channel."
	}
	return "Send a message to the user on a chat channel. Supports text-only, media-only, or text with media attachments."
}

func (t *MessageTool) Parameters() map[string]any {
	properties := map[string]any{
		"content": map[string]any{
			"type":        "string",
			"description": "Optional message text. When media is present, this text is used as the caption/body for the media message.",
		},
		"channel": map[string]any{
			"type":        "string",
			"description": "Optional: target channel (telegram, whatsapp, etc.)",
		},
		"chat_id": map[string]any{
			"type":        "string",
			"description": "Optional: target chat/user ID",
		},
		"reply_to_message_id": map[string]any{
			"type":        "string",
			"description": "Optional: reply target message ID for channels that support threaded replies",
		},
		"buttons": map[string]any{
			"type": "array", "description": "Optional Telegram inline-button rows.",
			"items": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"text": map[string]any{"type": "string"}, "callback_data": map[string]any{"type": "string"}, "url": map[string]any{"type": "string"},
			}, "required": []string{"text"}}},
		},
		"poll": map[string]any{
			"type": "object", "description": "Optional Telegram native poll.", "properties": map[string]any{
				"question": map[string]any{"type": "string"}, "options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "anonymous": map[string]any{"type": "boolean"},
			}, "required": []string{"question", "options"},
		},
	}
	params := map[string]any{
		"type":       "object",
		"properties": properties,
		"anyOf": []map[string]any{
			{"required": []string{"content"}},
			{"required": []string{"poll"}},
		},
	}
	if t.localMediaEnabled {
		properties["media"] = map[string]any{
			"type":        "array",
			"description": "Optional local media attachments to send with the message. Requires tools.message.media_enabled.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Path to the local file. Relative paths are resolved from workspace.",
					},
					"type": map[string]any{
						"type":        "string",
						"description": "Optional media type hint: image, audio, video, or file.",
					},
					"filename": map[string]any{
						"type":        "string",
						"description": "Optional display filename. Defaults to the basename of path.",
					},
				},
				"required": []string{"path"},
			},
		}
		params["anyOf"] = []map[string]any{
			{"required": []string{"content"}},
			{"required": []string{"media"}},
			{"required": []string{"poll"}},
		}
	}
	return params
}

func (t *MessageTool) ConfigureLocalMedia(
	workspace string,
	restrict bool,
	maxFileSize int,
	allowPaths []*regexp.Regexp,
) {
	t.workspace = workspace
	t.restrict = restrict
	if maxFileSize <= 0 {
		maxFileSize = config.DefaultMaxMediaSize
	}
	t.maxFileSize = maxFileSize
	t.allowPaths = allowPaths
	t.localMediaEnabled = true
}

func (t *MessageTool) SetMediaStore(store media.MediaStore) {
	t.mediaStore = store
}

// ResetSentInRound resets the per-round send tracker for the given session key.
// Called by the agent loop at the start of each inbound message processing round.
func (t *MessageTool) ResetSentInRound(sessionKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Delete the key entirely to prevent unbounded map growth over time
	// with many unique sessions. Truncating the slice keeps the key alive.
	delete(t.sentTargets, sessionKey)
}

// HasSentInRound returns true if the message tool sent a message during the current round.
func (t *MessageTool) HasSentInRound(sessionKey string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sentTargets[sessionKey]) > 0
}

// HasSentTo returns true if the message tool sent to the specific channel+chatID
// during the current round. Used by PublishResponseIfNeeded to avoid suppressing
// the final response when the message tool only sent to a different conversation.
func (t *MessageTool) HasSentTo(sessionKey, channel, chatID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, st := range t.sentTargets[sessionKey] {
		if st.Channel == channel && st.ChatID == chatID {
			return true
		}
	}
	return false
}

func (t *MessageTool) SetSendCallback(callback SendCallbackWithContext) {
	t.sendCallback = callback
}

func (t *MessageTool) SetInteractiveSendCallback(callback InteractiveSendCallback) {
	t.interactiveCallback = callback
}

func (t *MessageTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	content, _ := args["content"].(string)
	content = strings.TrimSpace(content)
	mediaArgs, err := parseMessageMediaArgs(args["media"])
	if err != nil {
		return &ToolResult{ForLLM: err.Error(), IsError: true}
	}
	buttons, err := parseInlineButtons(args["buttons"])
	if err != nil {
		return &ToolResult{ForLLM: err.Error(), IsError: true}
	}
	poll, err := parseOutboundPoll(args["poll"])
	if err != nil {
		return &ToolResult{ForLLM: err.Error(), IsError: true}
	}
	if len(mediaArgs) > 0 && !t.localMediaEnabled {
		return &ToolResult{
			ForLLM:  "message media attachments are disabled; enable tools.message.media_enabled to send local media through message",
			IsError: true,
		}
	}
	if content == "" && len(mediaArgs) == 0 && poll == nil {
		return &ToolResult{ForLLM: "content, media, or poll is required", IsError: true}
	}
	if len(mediaArgs) > 0 && (len(buttons) > 0 || poll != nil) {
		return &ToolResult{ForLLM: "media cannot be combined with buttons or a poll", IsError: true}
	}
	if poll != nil && len(buttons) > 0 {
		return &ToolResult{ForLLM: "buttons and poll cannot be combined", IsError: true}
	}

	channel, _ := args["channel"].(string)
	chatID, _ := args["chat_id"].(string)
	replyToMessageID, _ := args["reply_to_message_id"].(string)

	if channel == "" {
		channel = ToolChannel(ctx)
	}
	if chatID == "" {
		chatID = ToolChatID(ctx)
	}

	if channel == "" || chatID == "" {
		return &ToolResult{ForLLM: "No target channel/chat specified", IsError: true}
	}

	if t.sendCallback == nil {
		return &ToolResult{ForLLM: "Message sending not configured", IsError: true}
	}

	parts, err := t.buildMediaParts(channel, chatID, content, mediaArgs)
	if err != nil {
		return &ToolResult{ForLLM: err.Error(), IsError: true, Err: err}
	}

	var sendErr error
	if len(buttons) > 0 || poll != nil {
		if t.interactiveCallback == nil {
			return &ToolResult{ForLLM: "interactive messages are not configured", IsError: true}
		}
		sendErr = t.interactiveCallback(ctx, channel, chatID, content, replyToMessageID, buttons, poll)
	} else {
		sendErr = t.sendCallback(ctx, channel, chatID, content, replyToMessageID, parts)
	}
	if sendErr != nil {
		return &ToolResult{
			ForLLM:  fmt.Sprintf("sending message: %v", sendErr),
			IsError: true,
			Err:     sendErr,
		}
	}

	sessionKey := ToolSessionKey(ctx)
	t.mu.Lock()
	t.sentTargets[sessionKey] = append(t.sentTargets[sessionKey], sentTarget{Channel: channel, ChatID: chatID})
	t.mu.Unlock()

	status := fmt.Sprintf("Message sent to %s:%s", channel, chatID)
	if len(parts) > 0 {
		status = fmt.Sprintf("Message with %d media attachment(s) sent to %s:%s", len(parts), channel, chatID)
	}

	return &ToolResult{
		ForLLM: status,
		Silent: true,
	}
}

func parseInlineButtons(raw any) ([][]bus.InlineButton, error) {
	if raw == nil {
		return nil, nil
	}
	rows, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("buttons must be an array of rows")
	}
	result := make([][]bus.InlineButton, 0, len(rows))
	for i, rawRow := range rows {
		items, ok := rawRow.([]any)
		if !ok {
			return nil, fmt.Errorf("buttons[%d] must be an array", i)
		}
		row := make([]bus.InlineButton, 0, len(items))
		for j, rawButton := range items {
			obj, ok := rawButton.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("buttons[%d][%d] must be an object", i, j)
			}
			text, _ := obj["text"].(string)
			callback, _ := obj["callback_data"].(string)
			url, _ := obj["url"].(string)
			row = append(row, bus.InlineButton{Text: strings.TrimSpace(text), CallbackData: callback, URL: strings.TrimSpace(url)})
		}
		result = append(result, row)
	}
	return result, nil
}

func parseOutboundPoll(raw any) (*bus.OutboundPoll, error) {
	if raw == nil {
		return nil, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("poll must be an object")
	}
	question, _ := obj["question"].(string)
	rawOptions, ok := obj["options"].([]any)
	if !ok {
		return nil, fmt.Errorf("poll.options must be an array")
	}
	options := make([]string, 0, len(rawOptions))
	for i, rawOption := range rawOptions {
		option, ok := rawOption.(string)
		if !ok {
			return nil, fmt.Errorf("poll.options[%d] must be a string", i)
		}
		options = append(options, strings.TrimSpace(option))
	}
	anonymous, _ := obj["anonymous"].(bool)
	return &bus.OutboundPoll{Question: strings.TrimSpace(question), Options: options, Anonymous: anonymous}, nil
}

func parseMessageMediaArgs(raw any) ([]messageMediaArg, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("media must be an array")
	}
	result := make([]messageMediaArg, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("media[%d] must be an object", i)
		}
		path, _ := obj["path"].(string)
		path = strings.TrimSpace(path)
		if path == "" {
			return nil, fmt.Errorf("media[%d].path is required", i)
		}
		typ, _ := obj["type"].(string)
		filename, _ := obj["filename"].(string)
		result = append(result, messageMediaArg{
			Path:     path,
			Type:     strings.TrimSpace(typ),
			Filename: strings.TrimSpace(filename),
		})
	}
	return result, nil
}

func (t *MessageTool) buildMediaParts(
	channel, chatID, content string,
	mediaArgs []messageMediaArg,
) ([]bus.MediaPart, error) {
	if len(mediaArgs) == 0 {
		return nil, nil
	}
	if !t.localMediaEnabled {
		return nil, fmt.Errorf("message media attachments are disabled")
	}
	if t.mediaStore == nil {
		return nil, fmt.Errorf("media store not configured")
	}
	if strings.TrimSpace(t.workspace) == "" {
		return nil, fmt.Errorf("message media delivery is not configured")
	}

	scope := fmt.Sprintf("tool:message:%s:%s", channel, chatID)
	parts := make([]bus.MediaPart, 0, len(mediaArgs))
	for i, item := range mediaArgs {
		resolved, err := fstools.ValidatePathWithAllowPaths(item.Path, t.workspace, t.restrict, t.allowPaths)
		if err != nil {
			return nil, fmt.Errorf("invalid media[%d].path: %w", i, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("media[%d] file not found: %w", i, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("media[%d] path is a directory, expected a file", i)
		}
		if t.maxFileSize > 0 && info.Size() > int64(t.maxFileSize) {
			return nil, fmt.Errorf("media[%d] file too large: %d bytes (max %d bytes)", i, info.Size(), t.maxFileSize)
		}

		filename := item.Filename
		if filename == "" {
			filename = filepath.Base(resolved)
		}
		contentType := detectMessageMediaType(resolved)
		partType := normalizeMessageMediaType(item.Type, filename, contentType)
		ref, err := t.mediaStore.Store(resolved, media.MediaMeta{
			Filename:      filename,
			ContentType:   contentType,
			Source:        "tool:message",
			CleanupPolicy: media.CleanupPolicyForgetOnly,
		}, scope)
		if err != nil {
			return nil, fmt.Errorf("failed to register media[%d]: %w", i, err)
		}

		part := bus.MediaPart{
			Type:        partType,
			Ref:         ref,
			Filename:    filename,
			ContentType: contentType,
		}
		if i == 0 && content != "" {
			part.Caption = content
		}
		parts = append(parts, part)
	}
	return parts, nil
}

func detectMessageMediaType(path string) string {
	kind, err := filetype.MatchFile(path)
	if err == nil && kind != filetype.Unknown {
		return kind.MIME.Value
	}
	if ext := filepath.Ext(path); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	return "application/octet-stream"
}

func normalizeMessageMediaType(typeHint, filename, contentType string) string {
	switch strings.ToLower(strings.TrimSpace(typeHint)) {
	case "image", "audio", "video", "file":
		return strings.ToLower(strings.TrimSpace(typeHint))
	}

	ct := strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(ct, "image/"):
		return "image"
	case strings.HasPrefix(ct, "audio/"):
		return "audio"
	case strings.HasPrefix(ct, "video/"):
		return "video"
	}

	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return "image"
	case ".mp3", ".wav", ".ogg", ".oga", ".m4a", ".flac":
		return "audio"
	case ".mp4", ".mov", ".mkv", ".webm", ".avi":
		return "video"
	default:
		return "file"
	}
}
