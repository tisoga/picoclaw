package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/utils"
)

var (
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+([^\n]+)`)
	reBlockquote = regexp.MustCompile(`^>\s*(.*)$`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldStar   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder  = regexp.MustCompile(`__(.+?)__`)
	reItalic     = regexp.MustCompile(`_([^_]+)_`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reListItem   = regexp.MustCompile(`^[-*]\s+`)
	reCodeBlock  = regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
)

const (
	defaultMediaGroupDelay = 500 * time.Millisecond
	telegramCaptionLimit   = 1024
)

type TelegramChannel struct {
	*channels.BaseChannel
	bot       *telego.Bot
	bc        *config.Channel
	chatIDsMu sync.Mutex
	chatIDs   map[string]int64
	ctx       context.Context
	cancel    context.CancelFunc
	tgCfg     *config.TelegramSettings
	progress  *channels.ToolFeedbackAnimator

	registerFunc      func(context.Context, []commands.Definition) error
	commandRegDelayFn func(int) time.Duration
	commandRegCancel  context.CancelFunc

	mediaGroupMu    sync.Mutex
	mediaGroups     map[string]*telegramMediaGroup
	mediaGroupDelay time.Duration
	pollTargetsMu   sync.Mutex
	pollTargets     map[string]telegramPollTarget
	pollTargetOrder []string

	spool     *Spool
	spoolDone chan struct{}
	pollDone  chan struct{}
}

type telegramMediaGroup struct {
	messages   []*telego.Message
	timer      *time.Timer
	generation uint64
}

type telegramMessageParts struct {
	content    []string
	mediaPaths []string
}

type telegramPollTarget struct {
	chatID  string
	topicID int
}

func NewTelegramChannel(
	bc *config.Channel,
	telegramCfg *config.TelegramSettings,
	bus *bus.MessageBus,
) (*TelegramChannel, error) {
	channelName := bc.Name()
	var opts []telego.BotOption

	if telegramCfg.Proxy != "" {
		proxyURL, parseErr := url.Parse(telegramCfg.Proxy)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", telegramCfg.Proxy, parseErr)
		}
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}))
	} else if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		// Use environment proxy if configured
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		}))
	}

	if baseURL := strings.TrimRight(strings.TrimSpace(telegramCfg.BaseURL), "/"); baseURL != "" {
		opts = append(opts, telego.WithAPIServer(baseURL))
	}
	opts = append(opts, telego.WithLogger(logger.NewLogger("telego")))

	bot, err := telego.NewBot(telegramCfg.Token.String(), opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	base := channels.NewBaseChannel(
		channelName,
		telegramCfg,
		bus,
		bc.AllowFrom,
		channels.WithMaxMessageLength(4000),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	spool, err := NewSpool(channelName)
	if err != nil {
		logger.WarnCF("telegram", "Failed to initialize spool db, will run without persistence", map[string]any{
			"error": err.Error(),
		})
	}

	ch := &TelegramChannel{
		BaseChannel: base,
		bot:         bot,
		bc:          bc,
		chatIDs:     make(map[string]int64),
		tgCfg:       telegramCfg,
		spool:       spool,

		mediaGroups:     make(map[string]*telegramMediaGroup),
		mediaGroupDelay: telegramMediaGroupDelay(telegramCfg),
		pollTargets:     make(map[string]telegramPollTarget),
	}
	ch.progress = channels.NewToolFeedbackAnimator(ch.EditMessage)
	return ch, nil
}

func telegramMediaGroupDelay(telegramCfg *config.TelegramSettings) time.Duration {
	if telegramCfg != nil && telegramCfg.MediaGroupDelayMS > 0 {
		return time.Duration(telegramCfg.MediaGroupDelayMS) * time.Millisecond
	}
	return defaultMediaGroupDelay
}

func (c *TelegramChannel) telegramSenderAllowed(senderID string, chatID int64, topicID int, direct bool) bool {
	if c.tgCfg == nil {
		return c.IsAllowedSender(bus.SenderInfo{Platform: "telegram", PlatformID: senderID, CanonicalID: identity.BuildCanonicalID("telegram", senderID)})
	}
	policy := strings.ToLower(strings.TrimSpace(c.tgCfg.DMPolicy))
	if direct && policy == "disabled" {
		return false
	}
	if !direct && strings.EqualFold(strings.TrimSpace(c.tgCfg.GroupPolicy), "disabled") {
		return false
	}
	allowed := c.IsAllowedSender(bus.SenderInfo{Platform: "telegram", PlatformID: senderID, CanonicalID: identity.BuildCanonicalID("telegram", senderID)})
	if direct && policy == "open" {
		return true
	}
	if !direct {
		if strings.EqualFold(strings.TrimSpace(c.tgCfg.GroupPolicy), "open") {
			allowed = true
		}
		g, ok := c.tgCfg.Groups[strconv.FormatInt(chatID, 10)]
		if !ok {
			g, ok = c.tgCfg.Groups["*"]
		}
		if strings.EqualFold(strings.TrimSpace(c.tgCfg.GroupPolicy), "allowlist") && !ok {
			return false
		}
		if ok && len(g.AllowFrom) > 0 {
			allowed = false
			for _, v := range g.AllowFrom {
				if v == "*" || v == senderID {
					allowed = true
					break
				}
			}
		}
		if ok {
			if g.Enabled != nil && !*g.Enabled {
				return false
			}
			if topic, exists := g.Topics[strconv.Itoa(topicID)]; exists {
				if topic.Enabled != nil && !*topic.Enabled {
					return false
				}
				if len(topic.AllowFrom) > 0 {
					allowed = false
					for _, v := range topic.AllowFrom {
						if v == "*" || v == senderID {
							allowed = true
							break
						}
					}
				}
			}
		}
	}
	return allowed
}

func (c *TelegramChannel) telegramRequireMention(chatID int64, topicID int) *bool {
	if c.tgCfg == nil {
		return nil
	}
	g, ok := c.tgCfg.Groups[strconv.FormatInt(chatID, 10)]
	if !ok {
		g, ok = c.tgCfg.Groups["*"]
	}
	if !ok {
		return nil
	}
	if topic, exists := g.Topics[strconv.Itoa(topicID)]; exists && topic.RequireMention != nil {
		return topic.RequireMention
	}
	return g.RequireMention
}

// SendInlineKeyboard sends a small inline-keyboard message. Callback data is
// delivered as an inbound event and is capped by Telegram at 64 bytes.
func (c *TelegramChannel) SendInlineKeyboard(ctx context.Context, chatID string, content string, rows [][]telego.InlineKeyboardButton) (string, error) {
	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return "", err
	}
	if len(rows) > 8 {
		rows = rows[:8]
	}
	markup := &telego.InlineKeyboardMarkup{InlineKeyboard: rows}
	params := tu.Message(tu.ID(cid), content).WithReplyMarkup(markup)
	params.MessageThreadID = threadID
	m, err := c.bot.SendMessage(ctx, params)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(m.MessageID), nil
}

func (c *TelegramChannel) SendPoll(ctx context.Context, chatID string, question string, options []string, anonymous bool) (string, error) {
	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return "", err
	}
	if len(options) < 2 || len(options) > 12 {
		return "", fmt.Errorf("telegram polls require 2-12 options")
	}
	input := make([]telego.InputPollOption, 0, len(options))
	for _, option := range options {
		input = append(input, telego.InputPollOption{Text: option})
	}
	m, err := c.bot.SendPoll(ctx, &telego.SendPollParams{ChatID: tu.ID(cid), MessageThreadID: threadID, Question: question, Options: input, IsAnonymous: &anonymous})
	if err != nil {
		return "", err
	}
	if m.Poll != nil {
		c.rememberPollTarget(m.Poll.ID, strconv.FormatInt(cid, 10), threadID)
	}
	return strconv.Itoa(m.MessageID), nil
}

func (c *TelegramChannel) rememberPollTarget(pollID, chatID string, topicID int) {
	if pollID == "" {
		return
	}
	c.pollTargetsMu.Lock()
	defer c.pollTargetsMu.Unlock()
	if c.pollTargets == nil {
		c.pollTargets = make(map[string]telegramPollTarget)
	}
	if _, exists := c.pollTargets[pollID]; !exists {
		c.pollTargetOrder = append(c.pollTargetOrder, pollID)
	}
	c.pollTargets[pollID] = telegramPollTarget{chatID: chatID, topicID: topicID}
	for len(c.pollTargetOrder) > 128 {
		oldest := c.pollTargetOrder[0]
		c.pollTargetOrder = c.pollTargetOrder[1:]
		delete(c.pollTargets, oldest)
	}
}

func (c *TelegramChannel) pollTarget(pollID string) (telegramPollTarget, bool) {
	c.pollTargetsMu.Lock()
	defer c.pollTargetsMu.Unlock()
	target, ok := c.pollTargets[pollID]
	return target, ok
}

func (c *TelegramChannel) ReactToMessage(ctx context.Context, chatID, messageID string) (string, error) {
	cid, _, err := parseTelegramChatID(chatID)
	if err != nil {
		return "", err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return "", err
	}
	err = c.bot.SetMessageReaction(ctx, &telego.SetMessageReactionParams{ChatID: tu.ID(cid), MessageID: mid, Reaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Type: telego.ReactionEmoji, Emoji: "👍"}}})
	return messageID, err
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot (polling mode)...")

	c.ctx, c.cancel = context.WithCancel(ctx)

	c.SetRunning(true)
	logger.InfoCF("telegram", "Telegram bot connected", map[string]any{
		"username": c.bot.Username(),
	})

	if c.spool != nil {
		c.spoolDone = make(chan struct{})
		go func() {
			defer close(c.spoolDone)
			c.processSpoolWorker()
		}()
	}

	c.startCommandRegistration(c.ctx, commands.BuiltinDefinitions())

	c.pollDone = make(chan struct{})
	go func() {
		defer close(c.pollDone)
		c.pollUpdates()
	}()

	return nil
}

func (c *TelegramChannel) Stop(ctx context.Context) error {
	logger.InfoC("telegram", "Stopping Telegram bot...")
	c.SetRunning(false)

	// Cancel polling and spool processing before closing their shared resources.
	if c.cancel != nil {
		c.cancel()
	}
	c.flushPendingMediaGroups(ctx)
	waitForTelegramWorker(ctx, c.pollDone)
	waitForTelegramWorker(ctx, c.spoolDone)
	if c.spool != nil {
		_ = c.spool.Close()
	}
	if c.progress != nil {
		c.progress.StopAll()
	}
	if c.commandRegCancel != nil {
		c.commandRegCancel()
	}

	return nil
}

func waitForTelegramWorker(ctx context.Context, done <-chan struct{}) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// pollUpdates commits each message to the local spool before advancing the
// getUpdates offset. Telegram may redeliver the last batch after a crash; the
// spool's update_id uniqueness makes that replay idempotent.
func (c *TelegramChannel) pollUpdates() {
	defer c.SetRunning(false)
	offset := 0
	retryDelay := time.Second
	for c.ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(c.ctx, 45*time.Second)
		updates, err := c.bot.GetUpdates(requestCtx, &telego.GetUpdatesParams{
			Offset:         offset,
			Timeout:        30,
			AllowedUpdates: []string{"message", "callback_query", "message_reaction", "poll_answer"},
		})
		cancel()
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			logger.WarnCF("telegram", "Long polling failed; retrying", map[string]any{
				"error": err.Error(),
				"delay": retryDelay.String(),
			})
			timer := time.NewTimer(retryDelay)
			select {
			case <-c.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if retryDelay < 30*time.Second {
				retryDelay *= 2
				if retryDelay > 30*time.Second {
					retryDelay = 30 * time.Second
				}
			}
			continue
		}

		retryDelay = time.Second
		for _, update := range updates {
			if update.Message != nil {
				if c.spool != nil {
					if err := c.spool.Enqueue(update.UpdateID, update.Message); err != nil {
						logger.WarnCF("telegram", "Failed to enqueue message to spool", map[string]any{
							"error":     err.Error(),
							"update_id": update.UpdateID,
						})
						if err := c.handleMessage(c.ctx, update.Message); err != nil {
							logger.WarnCF("telegram", "Failed to process unspooled message", map[string]any{
								"error": err.Error(),
							})
							return
						}
					}
				} else if err := c.handleMessage(c.ctx, update.Message); err != nil {
					logger.WarnCF("telegram", "Failed to process message", map[string]any{
						"error": err.Error(),
					})
					return
				}
			}
			if update.CallbackQuery != nil {
				c.handleCallbackQuery(update.CallbackQuery)
			}
			if update.MessageReaction != nil {
				c.handleReactionUpdate(update.MessageReaction)
			}
			if update.PollAnswer != nil {
				c.handlePollAnswer(update.PollAnswer)
			}
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
		}
	}
}

func (c *TelegramChannel) publishTelegramEvent(chatID, senderID, messageID, content, eventType string, topicID int) {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(content) == "" {
		return
	}
	chatType := "group"
	if chatID == senderID {
		chatType = "direct"
	}
	ctx := bus.InboundContext{Channel: c.Name(), ChatID: chatID, ChatType: chatType, SenderID: senderID, MessageID: messageID,
		Raw: map[string]string{"event_type": eventType}}
	if topicID != 0 {
		ctx.TopicID = strconv.Itoa(topicID)
		ctx.ChatID = fmt.Sprintf("%s/%d", chatID, topicID)
	}
	sender := bus.SenderInfo{Platform: "telegram", PlatformID: senderID, CanonicalID: identity.BuildCanonicalID("telegram", senderID)}
	c.HandleMessageWithContext(c.ctx, ctx.ChatID, content, nil, ctx, sender)
}

func (c *TelegramChannel) handleCallbackQuery(q *telego.CallbackQuery) {
	if q == nil || q.Message == nil {
		return
	}
	if c.ctx != nil {
		_ = c.bot.AnswerCallbackQuery(c.ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: q.ID})
	}
	chat := q.Message.GetChat()
	if chat.ID == 0 {
		return
	}
	threadID := 0
	if m := q.Message.Message(); m != nil {
		threadID = m.MessageThreadID
	}
	if !c.telegramSenderAllowed(strconv.FormatInt(q.From.ID, 10), chat.ID, threadID, chat.Type == "private") {
		return
	}
	c.publishTelegramEvent(strconv.FormatInt(chat.ID, 10), strconv.FormatInt(q.From.ID, 10), strconv.Itoa(q.Message.GetMessageID()), q.Data, "callback", threadID)
}

func (c *TelegramChannel) handleReactionUpdate(r *telego.MessageReactionUpdated) {
	if r == nil {
		return
	}
	senderID := "anonymous"
	if r.User != nil {
		senderID = strconv.FormatInt(r.User.ID, 10)
	}
	if r.User != nil && !c.telegramSenderAllowed(senderID, r.Chat.ID, 0, r.Chat.Type == "private") {
		return
	}
	c.publishTelegramEvent(strconv.FormatInt(r.Chat.ID, 10), senderID, strconv.Itoa(r.MessageID), fmt.Sprintf("reaction changed: %d -> %d", len(r.OldReaction), len(r.NewReaction)), "reaction", 0)
}

func (c *TelegramChannel) handlePollAnswer(p *telego.PollAnswer) {
	if p == nil || p.User == nil {
		return
	}
	senderID := strconv.FormatInt(p.User.ID, 10)
	target, ok := c.pollTarget(p.PollID)
	if !ok {
		return
	}
	chatID, err := strconv.ParseInt(target.chatID, 10, 64)
	if err != nil || !c.telegramSenderAllowed(senderID, chatID, target.topicID, chatID == p.User.ID) {
		return
	}
	content := fmt.Sprintf("poll %s answer: %v", p.PollID, p.OptionIDs)
	c.publishTelegramEvent(target.chatID, senderID, p.PollID, content, "poll_answer", target.topicID)
}

func (c *TelegramChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	useMarkdownV2 := c.tgCfg.UseMarkdownV2

	chatID, threadID, err := resolveTelegramOutboundTarget(msg.ChatID, &msg.Context)
	if err != nil {
		return nil, fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
	}
	compositeTarget := strconv.FormatInt(chatID, 10)
	if threadID != 0 {
		compositeTarget = fmt.Sprintf("%s/%d", compositeTarget, threadID)
	}
	if msg.Poll != nil {
		pollID, pollErr := c.SendPoll(ctx, compositeTarget, msg.Poll.Question, msg.Poll.Options, msg.Poll.Anonymous)
		if pollErr != nil {
			return nil, pollErr
		}
		return []string{pollID}, nil
	}
	if len(msg.Buttons) > 0 {
		rows, buttonErr := telegramInlineRows(msg.Buttons)
		if buttonErr != nil {
			return nil, buttonErr
		}
		messageID, sendErr := c.SendInlineKeyboard(ctx, compositeTarget, msg.Content, rows)
		if sendErr != nil {
			return nil, sendErr
		}
		return []string{messageID}, nil
	}

	if msg.Content == "" {
		return nil, nil
	}

	isToolFeedback := outboundMessageIsToolFeedback(msg)
	toolFeedbackContent := msg.Content
	if isToolFeedback {
		toolFeedbackContent = fitToolFeedbackForTelegram(msg.Content, useMarkdownV2, 4096)
	}
	trackedChatID := telegramToolFeedbackChatKey(msg.ChatID, &msg.Context)
	if isToolFeedback {
		if msgID, handled, err := c.progress.Update(ctx, trackedChatID, toolFeedbackContent); handled {
			if err != nil {
				return nil, err
			}
			return []string{msgID}, nil
		}
	}
	trackedMsgID, hasTrackedMsg := c.currentToolFeedbackMessage(trackedChatID)
	if !isToolFeedback {
		if msgIDs, handled := c.finalizeToolFeedbackMessageForChat(ctx, trackedChatID, msg); handled {
			return msgIDs, nil
		}
	}

	// The Manager already splits messages to ≤4000 chars (WithMaxMessageLength),
	// so msg.Content is guaranteed to be within that limit. We still need to
	// check if HTML expansion pushes it beyond Telegram's 4096-char API limit.
	replyToID := msg.ReplyToMessageID
	var messageIDs []string
	queue := []string{msg.Content}
	if isToolFeedback {
		queue = []string{channels.InitialAnimatedToolFeedbackContent(toolFeedbackContent)}
	}
	for len(queue) > 0 {
		chunk := queue[0]
		queue = queue[1:]

		content := parseContent(chunk, useMarkdownV2)

		if len([]rune(content)) > 4096 {
			if isToolFeedback {
				fittedChunk := fitToolFeedbackForTelegram(chunk, useMarkdownV2, 4096)
				if fittedChunk != "" && fittedChunk != chunk {
					queue = append([]string{fittedChunk}, queue...)
					continue
				}
			}
			runeChunk := []rune(chunk)
			ratio := float64(len(runeChunk)) / float64(len([]rune(content)))
			smallerLen := int(float64(4096) * ratio * 0.95) // 5% safety margin

			// Guarantee progress: if estimated length is >= chunk length, force it smaller
			if smallerLen >= len(runeChunk) {
				smallerLen = len(runeChunk) - 1
			}

			if smallerLen <= 0 {
				msgID, err := c.sendChunk(ctx, sendChunkParams{
					chatID:        chatID,
					threadID:      threadID,
					content:       content,
					replyToID:     replyToID,
					mdFallback:    chunk,
					useMarkdownV2: useMarkdownV2,
				})
				if err != nil {
					return nil, err
				}
				messageIDs = append(messageIDs, msgID)
				replyToID = ""
				continue
			}

			// Use the estimated smaller length as a guide for SplitMessage.
			// SplitMessage will find natural break points (newlines/spaces) and respect code blocks.
			subChunks := channels.SplitMessage(chunk, smallerLen)

			// Safety fallback: If SplitMessage failed to shorten the chunk, force a manual hard split.
			if len(subChunks) == 1 && subChunks[0] == chunk {
				part1 := string(runeChunk[:smallerLen])
				part2 := string(runeChunk[smallerLen:])
				subChunks = []string{part1, part2}
			}

			// Filter out empty chunks to avoid sending empty messages to Telegram.
			nonEmpty := make([]string, 0, len(subChunks))
			for _, s := range subChunks {
				if s != "" {
					nonEmpty = append(nonEmpty, s)
				}
			}

			// Push sub-chunks back to the front of the queue
			queue = append(nonEmpty, queue...)
			continue
		}

		msgID, err := c.sendChunk(ctx, sendChunkParams{
			chatID:        chatID,
			threadID:      threadID,
			content:       content,
			replyToID:     replyToID,
			mdFallback:    chunk,
			useMarkdownV2: useMarkdownV2,
		})
		if err != nil {
			return nil, err
		}
		messageIDs = append(messageIDs, msgID)
		// Only the first chunk should be a reply; subsequent chunks are normal messages.
		replyToID = ""
	}

	if isToolFeedback && len(messageIDs) > 0 {
		c.RecordToolFeedbackMessage(trackedChatID, messageIDs[0], toolFeedbackContent)
	} else if !isToolFeedback && hasTrackedMsg {
		c.dismissTrackedToolFeedbackMessage(ctx, trackedChatID, trackedMsgID)
	}

	return messageIDs, nil
}

func telegramInlineRows(rows [][]bus.InlineButton) ([][]telego.InlineKeyboardButton, error) {
	if len(rows) > 8 {
		return nil, fmt.Errorf("telegram inline keyboard supports at most 8 rows")
	}
	out := make([][]telego.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		if len(row) > 8 {
			return nil, fmt.Errorf("telegram inline keyboard supports at most 8 buttons per row")
		}
		converted := make([]telego.InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			if button.Text == "" || (button.URL == "") == (button.CallbackData == "") {
				return nil, fmt.Errorf("telegram button requires text and exactly one action")
			}
			if len([]byte(button.CallbackData)) > 64 {
				return nil, fmt.Errorf("telegram callback data exceeds 64 bytes")
			}
			converted = append(converted, telego.InlineKeyboardButton{Text: button.Text, URL: button.URL, CallbackData: button.CallbackData})
		}
		out = append(out, converted)
	}
	return out, nil
}

type sendChunkParams struct {
	chatID        int64
	threadID      int
	content       string
	replyToID     string
	mdFallback    string
	useMarkdownV2 bool
}

// sendChunk sends a single HTML/MarkdownV2 message, falling back to the original
// markdown as plain text on parse failure so users never see raw HTML/MarkdownV2 tags.
func (c *TelegramChannel) sendChunk(
	ctx context.Context,
	params sendChunkParams,
) (string, error) {
	if c.tgCfg.RichMessages && !params.useMarkdownV2 {
		richParams := &telego.SendRichMessageParams{
			ChatID:          tu.ID(params.chatID),
			MessageThreadID: params.threadID,
			RichMessage:     telego.InputRichMessage{HTML: params.content},
		}
		if params.replyToID != "" {
			if mid, parseErr := strconv.Atoi(params.replyToID); parseErr == nil {
				richParams.ReplyParameters = &telego.ReplyParameters{MessageID: mid}
			}
		}
		if sent, richErr := c.bot.SendRichMessage(ctx, richParams); richErr == nil {
			return strconv.Itoa(sent.MessageID), nil
		} else {
			if !strings.Contains(richErr.Error(), "Bad Request") &&
				!strings.Contains(richErr.Error(), "Not Found") {
				return "", fmt.Errorf("telegram rich send: %w", channels.ErrTemporary)
			}
			logger.WarnCF("telegram", "Rich message send failed; falling back to standard HTML", map[string]any{
				"error": richErr.Error(),
			})
		}
	}

	tgMsg := tu.Message(tu.ID(params.chatID), params.content)
	tgMsg.MessageThreadID = params.threadID
	if params.useMarkdownV2 {
		tgMsg.WithParseMode(telego.ModeMarkdownV2)
	} else {
		tgMsg.WithParseMode(telego.ModeHTML)
	}

	if params.replyToID != "" {
		if mid, parseErr := strconv.Atoi(params.replyToID); parseErr == nil {
			tgMsg.ReplyParameters = &telego.ReplyParameters{
				MessageID: mid,
			}
		}
	}

	pMsg, err := c.bot.SendMessage(ctx, tgMsg)
	if err != nil {
		if !strings.Contains(err.Error(), "Bad Request") {
			return "", fmt.Errorf("telegram send: %w", channels.ErrTemporary)
		}
		logParseFailed(err, params.useMarkdownV2)

		tgMsg.Text = params.mdFallback
		tgMsg.ParseMode = ""
		pMsg, err = c.bot.SendMessage(ctx, tgMsg)
		if err != nil {
			return "", fmt.Errorf("telegram send: %w", channels.ErrTemporary)
		}
	}

	return strconv.Itoa(pMsg.MessageID), nil
}

// maxTypingDuration limits how long the typing indicator can run.
// Prevents endless typing when the LLM fails/hangs and preSend never invokes cancel.
// Matches channels.Manager's typingStopTTL (5 min) so behavior is consistent.
const maxTypingDuration = 5 * time.Minute

// StartTyping implements channels.TypingCapable.
// It sends ChatAction(typing) immediately and then repeats every 4 seconds
// (Telegram's typing indicator expires after ~5s) in a background goroutine.
// The returned stop function is idempotent and cancels the goroutine.
// The goroutine also exits automatically after maxTypingDuration if cancel is
// never called (e.g. when the LLM fails or times out without publishing).
func (c *TelegramChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return func() {}, err
	}

	action := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
	action.MessageThreadID = threadID

	// Send the first typing action immediately
	_ = c.bot.SendChatAction(ctx, action)

	typingCtx, cancel := context.WithCancel(ctx)
	// Cap lifetime so the goroutine cannot run indefinitely if cancel is never called
	maxCtx, maxCancel := context.WithTimeout(typingCtx, maxTypingDuration)
	go func() {
		defer maxCancel()
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-maxCtx.Done():
				return
			case <-ticker.C:
				a := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
				a.MessageThreadID = threadID
				_ = c.bot.SendChatAction(typingCtx, a)
			}
		}
	}()

	return cancel, nil
}

// EditMessage implements channels.MessageEditor.
func (c *TelegramChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	useMarkdownV2 := c.tgCfg.UseMarkdownV2
	cid, _, err := parseTelegramChatID(chatID)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	parsedContent := parseContent(content, useMarkdownV2)
	editMsg := tu.EditMessageText(tu.ID(cid), mid, parsedContent)
	if c.tgCfg.RichMessages && !useMarkdownV2 {
		editMsg.Text = ""
		editMsg.RichMessage = &telego.InputRichMessage{HTML: parsedContent}
	} else if useMarkdownV2 {
		editMsg.WithParseMode(telego.ModeMarkdownV2)
	} else {
		editMsg.WithParseMode(telego.ModeHTML)
	}
	_, err = c.bot.EditMessageText(ctx, editMsg)
	if err != nil {
		// If it failed because it was already modified (likely from a previous
		// attempt that timed out on our end but landed on Telegram), we treat
		// it as success to prevent the Manager from sending a duplicate message.
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}

		// Only fallback to plain text if the error looks like a parsing failure (Bad Request).
		// Network errors or timeouts should NOT trigger a retry with different content.
		if strings.Contains(err.Error(), "Bad Request") {
			logParseFailed(err, useMarkdownV2)
			_, err = c.bot.EditMessageText(ctx, tu.EditMessageText(tu.ID(cid), mid, content))
		}
	}

	if err != nil {
		if strings.Contains(err.Error(), "message is not modified") {
			return nil
		}

		if isPostConnectError(err) {
			logger.WarnCF(
				"telegram",
				"EditMessage likely landed but result is unknown; swallowing error to prevent duplicate",
				map[string]any{
					"chat_id": chatID,
					"mid":     mid,
					"error":   err.Error(),
				},
			)
			return nil // Swallow to prevent Manager fallback to a new SendMessage
		}
	}

	return err
}

// DeleteMessage implements channels.MessageDeleter.
func (c *TelegramChannel) DeleteMessage(ctx context.Context, chatID string, messageID string) error {
	cid, _, err := parseTelegramChatID(chatID)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	return c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    tu.ID(cid),
		MessageID: mid,
	})
}

func outboundMessageIsToolFeedback(msg bus.OutboundMessage) bool {
	if len(msg.Context.Raw) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(msg.Context.Raw["message_kind"]), "tool_feedback")
}

func (c *TelegramChannel) currentToolFeedbackMessage(chatID string) (string, bool) {
	if c.progress == nil {
		return "", false
	}
	return c.progress.Current(chatID)
}

func (c *TelegramChannel) takeToolFeedbackMessage(chatID string) (string, string, bool) {
	if c.progress == nil {
		return "", "", false
	}
	return c.progress.Take(chatID)
}

func (c *TelegramChannel) RecordToolFeedbackMessage(chatID, messageID, content string) {
	if c.progress == nil {
		return
	}
	c.progress.Record(chatID, messageID, content)
}

func (c *TelegramChannel) ClearToolFeedbackMessage(chatID string) {
	if c.progress == nil {
		return
	}
	c.progress.Clear(chatID)
}

func (c *TelegramChannel) DismissToolFeedbackMessage(ctx context.Context, chatID string) {
	msgID, ok := c.currentToolFeedbackMessage(chatID)
	if !ok {
		return
	}
	c.dismissTrackedToolFeedbackMessage(ctx, chatID, msgID)
}

func (c *TelegramChannel) dismissTrackedToolFeedbackMessage(ctx context.Context, chatID, messageID string) {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(messageID) == "" {
		return
	}
	c.ClearToolFeedbackMessage(chatID)
	_ = c.DeleteMessage(ctx, chatID, messageID)
}

func (c *TelegramChannel) finalizeTrackedToolFeedbackMessage(
	ctx context.Context,
	chatID string,
	content string,
	editFn func(context.Context, string, string, string) error,
) ([]string, bool) {
	msgID, baseContent, ok := c.takeToolFeedbackMessage(chatID)
	if !ok || editFn == nil {
		return nil, false
	}
	if err := editFn(ctx, chatID, msgID, content); err != nil {
		c.RecordToolFeedbackMessage(chatID, msgID, baseContent)
		return nil, false
	}
	return []string{msgID}, true
}

func (c *TelegramChannel) FinalizeToolFeedbackMessage(ctx context.Context, msg bus.OutboundMessage) ([]string, bool) {
	if outboundMessageIsToolFeedback(msg) {
		return nil, false
	}
	return c.finalizeToolFeedbackMessageForChat(ctx, telegramToolFeedbackChatKey(msg.ChatID, &msg.Context), msg)
}

func (c *TelegramChannel) finalizeToolFeedbackMessageForChat(
	ctx context.Context,
	chatID string,
	msg bus.OutboundMessage,
) ([]string, bool) {
	return c.finalizeTrackedToolFeedbackMessage(ctx, chatID, msg.Content, c.EditMessage)
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message (e.g. "Thinking... 💭") that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *TelegramChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	phCfg := c.bc.Placeholder
	if !phCfg.Enabled {
		return "", nil
	}

	text := phCfg.GetRandomText()

	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return "", err
	}

	phMsg := tu.Message(tu.ID(cid), text)
	phMsg.MessageThreadID = threadID
	pMsg, err := c.bot.SendMessage(ctx, phMsg)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%d", pMsg.MessageID), nil
}

// SendMedia implements the channels.MediaSender interface.
func (c *TelegramChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	trackedChatID := telegramToolFeedbackChatKey(msg.ChatID, &msg.Context)
	trackedMsgID, hasTrackedMsg := c.currentToolFeedbackMessage(trackedChatID)

	chatID, threadID, err := resolveTelegramOutboundTarget(msg.ChatID, &msg.Context)
	if err != nil {
		return nil, fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
	}

	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}
	maxItems := 0
	if c.tgCfg != nil {
		maxItems = c.tgCfg.MaxAlbumItems
	}
	if maxItems > 0 && len(msg.Parts) > maxItems {
		return nil, fmt.Errorf("telegram media limit: at most %d attachments", maxItems)
	}
	maxBytes := int64(0)
	if c.tgCfg != nil && c.tgCfg.MediaMaxMB > 0 {
		maxBytes = int64(c.tgCfg.MediaMaxMB) * 1024 * 1024
	}
	for _, part := range msg.Parts {
		path, resolveErr := store.Resolve(part.Ref)
		if resolveErr != nil {
			continue
		}
		if maxBytes > 0 {
			if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxBytes {
				return nil, fmt.Errorf("telegram media %s exceeds configured limit", part.Filename)
			}
		}
	}

	var messageIDs []string
	leadingCaption := telegramLeadingCaption(msg.Parts)
	if len([]rune(leadingCaption)) > telegramCaptionLimit {
		leadingIDs, leadingErr := c.sendCaptionText(ctx, chatID, threadID, leadingCaption)
		if leadingErr != nil {
			return nil, leadingErr
		}
		messageIDs = append(messageIDs, leadingIDs...)
		msg = telegramClearMediaCaptions(msg)
	}

	if len(msg.Parts) > 1 && telegramCanSendMediaGroup(msg.Parts) {
		groupIDs, err := c.sendImageMediaGroups(ctx, chatID, threadID, store, msg.Parts)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to send media group", map[string]any{
				"count": len(msg.Parts),
				"error": err.Error(),
			})
			return nil, fmt.Errorf("telegram send media group: %w", channels.ErrTemporary)
		}
		if len(groupIDs) > 0 {
			messageIDs = append(messageIDs, groupIDs...)
			if hasTrackedMsg {
				c.dismissTrackedToolFeedbackMessage(ctx, trackedChatID, trackedMsgID)
			}
			return messageIDs, nil
		}
	}

	for _, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		file, err := os.Open(localPath)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to open media file", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			continue
		}

		var tgResult *telego.Message
		switch part.Type {
		case "image":
			params := &telego.SendPhotoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Photo:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			tgResult, err = c.bot.SendPhoto(ctx, params)
			if err != nil && strings.Contains(err.Error(), "PHOTO_INVALID_DIMENSIONS") {
				if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
					file.Close()
					return nil, fmt.Errorf("telegram rewind media after photo failure: %w", channels.ErrTemporary)
				}

				docParams := &telego.SendDocumentParams{
					ChatID:          tu.ID(chatID),
					MessageThreadID: threadID,
					Document:        telego.InputFile{File: file},
					Caption:         part.Caption,
				}
				tgResult, err = c.bot.SendDocument(ctx, docParams)
			}
		case "audio":
			// Send OGG files with "voice" in the filename as Telegram voice
			// bubbles (SendVoice) instead of audio attachments (SendAudio).
			fn := strings.ToLower(part.Filename)
			if strings.Contains(fn, "voice") && (strings.HasSuffix(fn, ".ogg") || strings.HasSuffix(fn, ".oga")) {
				vparams := &telego.SendVoiceParams{
					ChatID:          tu.ID(chatID),
					MessageThreadID: threadID,
					Voice:           telego.InputFile{File: file},
					Caption:         part.Caption,
				}
				tgResult, err = c.bot.SendVoice(ctx, vparams)
			} else {
				params := &telego.SendAudioParams{
					ChatID:          tu.ID(chatID),
					MessageThreadID: threadID,
					Audio:           telego.InputFile{File: file},
					Caption:         part.Caption,
				}
				tgResult, err = c.bot.SendAudio(ctx, params)
			}
		case "video":
			params := &telego.SendVideoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Video:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			tgResult, err = c.bot.SendVideo(ctx, params)
		default: // "file" or unknown types
			params := &telego.SendDocumentParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Document:        telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			tgResult, err = c.bot.SendDocument(ctx, params)
		}

		if tgResult != nil {
			messageIDs = append(messageIDs, strconv.Itoa(tgResult.MessageID))
		}
		file.Close()

		if err != nil {
			logger.ErrorCF("telegram", "Failed to send media", map[string]any{
				"type":  part.Type,
				"error": err.Error(),
			})
			return nil, fmt.Errorf("telegram send media: %w", channels.ErrTemporary)
		}
	}

	if hasTrackedMsg {
		c.dismissTrackedToolFeedbackMessage(ctx, trackedChatID, trackedMsgID)
	}

	return messageIDs, nil
}

func telegramCanSendMediaGroup(parts []bus.MediaPart) bool {
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part.Type != "image" {
			return false
		}
	}
	return true
}

func (c *TelegramChannel) sendImageMediaGroups(
	ctx context.Context,
	chatID int64,
	threadID int,
	store media.MediaStore,
	parts []bus.MediaPart,
) ([]string, error) {
	const maxGroupSize = 10

	messageIDs := make([]string, 0, len(parts))
	for start := 0; start < len(parts); start += maxGroupSize {
		end := start + maxGroupSize
		if end > len(parts) {
			end = len(parts)
		}
		groupIDs, err := c.sendSingleImageMediaGroup(ctx, chatID, threadID, store, parts[start:end])
		if err != nil {
			return nil, err
		}
		messageIDs = append(messageIDs, groupIDs...)
	}
	return messageIDs, nil
}

func (c *TelegramChannel) sendSingleImageMediaGroup(
	ctx context.Context,
	chatID int64,
	threadID int,
	store media.MediaStore,
	parts []bus.MediaPart,
) ([]string, error) {
	opened := make([]*os.File, 0, len(parts))
	defer func() {
		for _, file := range opened {
			file.Close()
		}
	}()

	inputMedia := make([]telego.InputMedia, 0, len(parts))
	for i, part := range parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to resolve media ref for media group", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			return nil, err
		}

		file, err := os.Open(localPath)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to open media file for media group", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			return nil, err
		}
		opened = append(opened, file)

		mediaItem := &telego.InputMediaPhoto{
			Type:  telego.MediaTypePhoto,
			Media: telego.InputFile{File: file},
		}
		if i == 0 {
			mediaItem.Caption = part.Caption
		}
		inputMedia = append(inputMedia, mediaItem)
	}

	results, err := c.bot.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
		ChatID:          tu.ID(chatID),
		MessageThreadID: threadID,
		Media:           inputMedia,
	})
	if err != nil {
		return nil, err
	}

	messageIDs := make([]string, 0, len(results))
	for _, result := range results {
		messageIDs = append(messageIDs, strconv.Itoa(result.MessageID))
	}
	return messageIDs, nil
}

func (c *TelegramChannel) sendCaptionText(
	ctx context.Context,
	chatID int64,
	threadID int,
	text string,
) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	chunks := channels.SplitMessage(text, c.MaxMessageLength())
	messageIDs := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		msgID, err := c.sendChunk(ctx, sendChunkParams{
			chatID:        chatID,
			threadID:      threadID,
			content:       chunk,
			mdFallback:    chunk,
			useMarkdownV2: false,
		})
		if err != nil {
			return nil, err
		}
		messageIDs = append(messageIDs, msgID)
	}
	return messageIDs, nil
}

func telegramLeadingCaption(parts []bus.MediaPart) string {
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0].Caption)
}

func telegramClearMediaCaptions(msg bus.OutboundMediaMessage) bus.OutboundMediaMessage {
	if len(msg.Parts) == 0 {
		return msg
	}
	cloned := msg
	cloned.Parts = append([]bus.MediaPart(nil), msg.Parts...)
	for i := range cloned.Parts {
		cloned.Parts[i].Caption = ""
	}
	return cloned
}

func (c *TelegramChannel) handleMessage(ctx context.Context, message *telego.Message) error {
	if message != nil && strings.TrimSpace(message.MediaGroupID) != "" {
		return c.bufferMediaGroupMessage(ctx, message)
	}
	return c.handleMessages(ctx, []*telego.Message{message})
}

func (c *TelegramChannel) bufferMediaGroupMessage(ctx context.Context, message *telego.Message) error {
	if message == nil {
		return fmt.Errorf("message is nil")
	}
	groupID := strings.TrimSpace(message.MediaGroupID)
	if groupID == "" {
		return c.handleMessages(ctx, []*telego.Message{message})
	}

	msgCopy := *message
	msgCopy.Photo = append([]telego.PhotoSize(nil), message.Photo...)
	key := fmt.Sprintf("%d:%s", message.Chat.ID, groupID)

	c.mediaGroupMu.Lock()
	if c.mediaGroups == nil {
		c.mediaGroups = make(map[string]*telegramMediaGroup)
	}
	group := c.mediaGroups[key]
	if group == nil {
		group = &telegramMediaGroup{}
		c.mediaGroups[key] = group
	}
	group.messages = append(group.messages, &msgCopy)
	maxItems := 0
	if c.tgCfg != nil {
		maxItems = c.tgCfg.MaxAlbumItems
	}
	if maxItems > 0 && len(group.messages) > maxItems {
		group.messages = group.messages[:maxItems]
	}
	group.generation++
	generation := group.generation
	if group.timer != nil {
		group.timer.Stop()
	}
	delay := c.mediaGroupDelay
	if delay <= 0 {
		delay = defaultMediaGroupDelay
	}
	group.timer = time.AfterFunc(delay, func() {
		c.flushMediaGroup(c.ctx, key, generation)
	})
	c.mediaGroupMu.Unlock()

	logger.DebugCF("telegram", "Buffered media group message", map[string]any{
		"chat_id":        message.Chat.ID,
		"media_group_id": groupID,
		"message_id":     message.MessageID,
	})
	return nil
}

func (c *TelegramChannel) flushPendingMediaGroups(ctx context.Context) {
	c.mediaGroupMu.Lock()
	keys := make([]string, 0, len(c.mediaGroups))
	for key, group := range c.mediaGroups {
		if group.timer != nil {
			group.timer.Stop()
		}
		keys = append(keys, key)
	}
	c.mediaGroupMu.Unlock()

	for _, key := range keys {
		c.flushMediaGroup(ctx, key, 0)
	}
}

func (c *TelegramChannel) flushMediaGroup(ctx context.Context, key string, generation uint64) {
	c.mediaGroupMu.Lock()
	group := c.mediaGroups[key]
	if group == nil {
		c.mediaGroupMu.Unlock()
		return
	}
	if generation != 0 && group.generation != generation {
		c.mediaGroupMu.Unlock()
		return
	}
	delete(c.mediaGroups, key)
	if group.timer != nil {
		group.timer.Stop()
	}
	messages := append([]*telego.Message(nil), group.messages...)
	c.mediaGroupMu.Unlock()

	if len(messages) == 0 {
		return
	}
	slices.SortFunc(messages, func(a, b *telego.Message) int {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		case b == nil:
			return 1
		default:
			return a.MessageID - b.MessageID
		}
	})
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.handleMessages(ctx, messages); err != nil {
		logger.ErrorCF("telegram", "Failed to handle media group", map[string]any{
			"key":   key,
			"error": err.Error(),
		})
	}
}

func (c *TelegramChannel) handleMessages(ctx context.Context, messages []*telego.Message) error {
	if len(messages) == 0 {
		return nil
	}
	message := messages[0]
	for _, candidate := range messages {
		if candidate == nil {
			continue
		}
		if strings.TrimSpace(candidate.Text) != "" || strings.TrimSpace(candidate.Caption) != "" {
			message = candidate
			break
		}
	}
	if message == nil {
		return fmt.Errorf("message is nil")
	}

	user := message.From
	if user == nil {
		return fmt.Errorf("message sender (user) is nil")
	}

	platformID := fmt.Sprintf("%d", user.ID)
	chatID := message.Chat.ID
	sender := bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  platformID,
		CanonicalID: identity.BuildCanonicalID("telegram", platformID),
		Username:    user.Username,
		DisplayName: user.FirstName,
	}

	// check allowlist to avoid downloading attachments for rejected users
	if !c.telegramSenderAllowed(platformID, chatID, message.MessageThreadID, message.Chat.Type == "private") {
		logger.DebugCF("telegram", "Message rejected by allowlist", map[string]any{
			"user_id": platformID,
		})
		return nil
	}
	if c.tgCfg != nil && c.tgCfg.AckReactions {
		_ = c.bot.SetMessageReaction(ctx, &telego.SetMessageReactionParams{ChatID: tu.ID(chatID), MessageID: message.MessageID, Reaction: []telego.ReactionType{&telego.ReactionTypeEmoji{Type: telego.ReactionEmoji, Emoji: "👀"}}})
	}

	c.chatIDsMu.Lock()
	c.chatIDs[platformID] = chatID
	c.chatIDsMu.Unlock()

	content := ""
	mediaPaths := []string{}

	chatIDStr := fmt.Sprintf("%d", chatID)
	messageIDStr := fmt.Sprintf("%d", message.MessageID)
	scope := channels.BuildMediaScope("telegram", chatIDStr, messageIDStr)

	// Helper to register a local file with the media store
	storeMedia := func(localPath, filename string) string {
		if store := c.GetMediaStore(); store != nil {
			ref, err := store.Store(localPath, media.MediaMeta{
				Filename:      filename,
				Source:        "telegram",
				CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
			}, scope)
			if err == nil {
				return ref
			}
		}
		return localPath // fallback: use raw path
	}

	for i, msg := range messages {
		if msg == nil {
			continue
		}
		parts := c.collectTelegramMessageParts(ctx, msg, i, len(messages), storeMedia)
		for _, part := range parts.content {
			if content != "" {
				content += "\n"
			}
			content += part
		}
		mediaPaths = append(mediaPaths, parts.mediaPaths...)
	}

	if content == "" && len(mediaPaths) == 0 {
		return nil
	}

	if content == "" {
		content = "[media only]"
	}

	// In group chats, apply unified group trigger filtering
	isMentioned := false
	if message.Chat.Type != "private" {
		isMentioned = c.isBotMentioned(message)
		if isMentioned {
			content = c.stripBotMention(content)
		}
		respond, cleaned := c.ShouldRespondInGroupWithMentionOverride(isMentioned, content, c.telegramRequireMention(chatID, message.MessageThreadID))
		if !respond {
			return nil
		}
		content = cleaned
	}

	if message.ReplyToMessage != nil {
		quotedMedia := quotedTelegramMediaRefs(
			message.ReplyToMessage,
			func(fileID, ext, filename string) string {
				localPath := c.downloadFile(ctx, fileID, ext)
				if localPath == "" {
					return ""
				}
				return storeMedia(localPath, filename)
			},
		)
		if len(quotedMedia) > 0 {
			mediaPaths = append(quotedMedia, mediaPaths...)
		}
		content = c.prependTelegramQuotedReply(content, message.ReplyToMessage)
	}

	// For forum topics, embed the thread ID as "chatID/threadID" so replies
	// route to the correct topic and each topic gets its own session.
	// Only forum groups (IsForum) are handled; regular group reply threads
	// must share one session per group.
	compositeChatID := fmt.Sprintf("%d", chatID)
	threadID := message.MessageThreadID
	if message.Chat.IsForum && threadID != 0 {
		compositeChatID = fmt.Sprintf("%d/%d", chatID, threadID)
	}

	logger.DebugCF("telegram", "Received message", map[string]any{
		"sender_id": sender.CanonicalID,
		"chat_id":   compositeChatID,
		"thread_id": threadID,
		"preview":   utils.Truncate(content, 50),
	})

	peerKind := "direct"
	if message.Chat.Type != "private" {
		peerKind = "group"
	}
	messageID := fmt.Sprintf("%d", message.MessageID)

	metadata := map[string]string{
		"user_id":    fmt.Sprintf("%d", user.ID),
		"username":   user.Username,
		"first_name": user.FirstName,
		"is_group":   fmt.Sprintf("%t", message.Chat.Type != "private"),
	}

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    compositeChatID,
		ChatType:  peerKind,
		SenderID:  platformID,
		MessageID: messageID,
		Mentioned: isMentioned,
		Raw:       metadata,
	}
	if message.Chat.IsForum && threadID != 0 {
		inboundCtx.TopicID = fmt.Sprintf("%d", threadID)
	}
	if message.ReplyToMessage != nil {
		inboundCtx.ReplyToMessageID = fmt.Sprintf("%d", message.ReplyToMessage.MessageID)
	}

	c.HandleMessageWithContext(
		c.ctx,
		compositeChatID,
		content,
		mediaPaths,
		inboundCtx,
		sender,
	)
	return nil
}

func (c *TelegramChannel) collectTelegramMessageParts(
	ctx context.Context,
	msg *telego.Message,
	index int,
	total int,
	storeMedia func(localPath, filename string) string,
) telegramMessageParts {
	parts := telegramMessageParts{}
	if msg == nil {
		return parts
	}
	if text := strings.TrimSpace(msg.Text); text != "" {
		parts.content = append(parts.content, text)
	}
	if caption := strings.TrimSpace(msg.Caption); caption != "" {
		parts.content = append(parts.content, caption)
	}
	if msg.Location != nil {
		parts.content = append(parts.content, fmt.Sprintf(
			"[User location: lat=%.6f, lng=%.6f]",
			msg.Location.Latitude,
			msg.Location.Longitude,
		))
	}
	if len(msg.Photo) > 0 {
		photo := msg.Photo[len(msg.Photo)-1]
		photoPath := c.downloadPhoto(ctx, photo.FileID)
		if photoPath != "" {
			photoNumber := index + 1
			parts.mediaPaths = append(parts.mediaPaths, storeMedia(photoPath, fmt.Sprintf("photo-%d.jpg", photoNumber)))
			parts.content = append(parts.content, fmt.Sprintf("[image: photo %d]", photoNumber))
		}
	}
	if msg.Voice != nil {
		voicePath := c.downloadFile(ctx, msg.Voice.FileID, ".ogg")
		if voicePath != "" {
			parts.mediaPaths = append(
				parts.mediaPaths,
				storeMedia(voicePath, indexedMediaFilename("voice", ".ogg", index, total)),
			)
			parts.content = append(parts.content, "[voice]")
		}
	}
	if msg.Audio != nil {
		audioPath := c.downloadFile(ctx, msg.Audio.FileID, ".mp3")
		if audioPath != "" {
			filename := msg.Audio.FileName
			if strings.TrimSpace(filename) == "" {
				filename = indexedMediaFilename("audio", ".mp3", index, total)
			}
			parts.mediaPaths = append(parts.mediaPaths, storeMedia(audioPath, filename))
			parts.content = append(parts.content, "[audio]")
		}
	}
	if msg.Document != nil {
		docPath := c.downloadFile(ctx, msg.Document.FileID, "")
		if docPath != "" {
			filename := msg.Document.FileName
			if strings.TrimSpace(filename) == "" {
				filename = indexedMediaFilename("document", "", index, total)
			}
			parts.mediaPaths = append(parts.mediaPaths, storeMedia(docPath, filename))
			parts.content = append(parts.content, "[file]")
		}
	}
	return parts
}

func indexedMediaFilename(prefix, ext string, index int, total int) string {
	if total <= 1 {
		return prefix + ext
	}
	return fmt.Sprintf("%s-%d%s", prefix, index+1, ext)
}

func (c *TelegramChannel) prependTelegramQuotedReply(content string, reply *telego.Message) string {
	quoted := strings.TrimSpace(telegramQuotedContent(reply))
	if quoted == "" {
		return content
	}

	author := telegramQuotedAuthor(reply)
	role := c.telegramQuotedRole(reply)
	if strings.TrimSpace(content) == "" {
		return fmt.Sprintf("[quoted %s message from %s]: %s", role, author, quoted)
	}
	return fmt.Sprintf("[quoted %s message from %s]: %s\n\n%s", role, author, quoted, content)
}

func (c *TelegramChannel) telegramQuotedRole(message *telego.Message) string {
	if message == nil {
		return "unknown"
	}

	if message.From != nil {
		if !message.From.IsBot {
			return "user"
		}
		if c.isOwnBotUser(message.From) {
			return "assistant"
		}
		return "bot"
	}

	if message.SenderChat != nil {
		return "chat"
	}

	return "unknown"
}

func (c *TelegramChannel) isOwnBotUser(user *telego.User) bool {
	if c == nil || c.bot == nil || user == nil || !user.IsBot {
		return false
	}

	if botID := c.bot.ID(); botID != 0 && user.ID == botID {
		return true
	}

	botUsername := strings.TrimPrefix(strings.TrimSpace(c.bot.Username()), "@")
	if botUsername == "" {
		return false
	}
	return strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(user.Username), "@"), botUsername)
}

func telegramQuotedAuthor(message *telego.Message) string {
	if message == nil || message.From == nil {
		return "unknown"
	}
	if username := strings.TrimSpace(message.From.Username); username != "" {
		return username
	}
	if firstName := strings.TrimSpace(message.From.FirstName); firstName != "" {
		return firstName
	}
	return "unknown"
}

func telegramQuotedContent(message *telego.Message) string {
	if message == nil {
		return ""
	}

	var parts []string
	if text := strings.TrimSpace(message.Text); text != "" {
		parts = append(parts, text)
	}
	if caption := strings.TrimSpace(message.Caption); caption != "" {
		parts = append(parts, caption)
	}
	switch {
	case len(message.Photo) > 0:
		parts = append(parts, "[image: photo]")
	}
	switch {
	case message.Voice != nil:
		parts = append(parts, "[voice]")
	case message.Audio != nil:
		parts = append(parts, "[audio]")
	}
	if message.Document != nil {
		parts = append(parts, "[file]")
	}

	return strings.Join(parts, "\n")
}

func quotedTelegramMediaRefs(
	message *telego.Message,
	resolve func(fileID, ext, filename string) string,
) []string {
	if message == nil || resolve == nil {
		return nil
	}

	var refs []string
	if message.Voice != nil {
		if ref := resolve(message.Voice.FileID, ".ogg", "voice.ogg"); ref != "" {
			refs = append(refs, ref)
		}
	}
	if message.Audio != nil {
		if ref := resolve(message.Audio.FileID, ".mp3", "audio.mp3"); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (c *TelegramChannel) downloadPhoto(ctx context.Context, fileID string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get photo file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ".jpg")
}

func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file.FilePath == "" {
		return ""
	}
	maxBytes := int64(0)
	if c.tgCfg != nil && c.tgCfg.MediaMaxMB > 0 {
		maxBytes = int64(c.tgCfg.MediaMaxMB) * 1024 * 1024
	}
	if maxBytes > 0 && file.FileSize > 0 && int64(file.FileSize) > maxBytes {
		logger.WarnCF("telegram", "Rejected oversized media", map[string]any{"size": file.FileSize, "limit": maxBytes})
		return ""
	}

	url := c.bot.FileDownloadURL(file.FilePath)
	// The download URL embeds the bot token. Never write it to logs.
	logger.DebugCF("telegram", "Downloading Telegram file", map[string]any{"file_path": file.FilePath})

	// Use FilePath as filename for better identification
	filename := file.FilePath + ext
	return utils.DownloadFile(url, filename, utils.DownloadOptions{
		LoggerPrefix: "telegram",
	})
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID, ext string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ext)
}

func parseContent(text string, useMarkdownV2 bool) string {
	if useMarkdownV2 {
		return markdownToTelegramMarkdownV2(text)
	}

	return markdownToTelegramHTML(text)
}

func fitToolFeedbackForTelegram(content string, useMarkdownV2 bool, maxParsedLen int) string {
	content = strings.TrimSpace(content)
	if content == "" || maxParsedLen <= 0 {
		return ""
	}
	animationSafeLen := maxParsedLen - channels.MaxToolFeedbackAnimationFrameLength()
	if animationSafeLen <= 0 {
		animationSafeLen = maxParsedLen
	}
	if len([]rune(parseContent(content, useMarkdownV2))) <= animationSafeLen {
		return content
	}

	low := 1
	high := len([]rune(content))
	best := utils.Truncate(content, 1)

	for low <= high {
		mid := (low + high) / 2
		candidate := utils.FitToolFeedbackMessage(content, mid)
		if candidate == "" {
			high = mid - 1
			continue
		}
		if len([]rune(parseContent(candidate, useMarkdownV2))) <= animationSafeLen {
			best = candidate
			low = mid + 1
			continue
		}
		high = mid - 1
	}

	return best
}

func (c *TelegramChannel) PrepareToolFeedbackMessageContent(content string) string {
	if c == nil || c.tgCfg == nil {
		return strings.TrimSpace(content)
	}
	return fitToolFeedbackForTelegram(content, c.tgCfg.UseMarkdownV2, 4096)
}

func telegramToolFeedbackChatKey(chatID string, outboundCtx *bus.InboundContext) string {
	resolvedChatID, threadID, err := resolveTelegramOutboundTarget(chatID, outboundCtx)
	if err != nil || threadID == 0 {
		return strings.TrimSpace(chatID)
	}
	return fmt.Sprintf("%d/%d", resolvedChatID, threadID)
}

func (c *TelegramChannel) ToolFeedbackMessageChatID(chatID string, outboundCtx *bus.InboundContext) string {
	return telegramToolFeedbackChatKey(chatID, outboundCtx)
}

// parseTelegramChatID splits "chatID/threadID" into its components.
// Returns threadID=0 when no "/" is present (non-forum messages).
func parseTelegramChatID(chatID string) (int64, int, error) {
	idx := strings.Index(chatID, "/")
	if idx == -1 {
		cid, err := strconv.ParseInt(chatID, 10, 64)
		return cid, 0, err
	}
	cid, err := strconv.ParseInt(chatID[:idx], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tid, err := strconv.Atoi(chatID[idx+1:])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid thread ID in chat ID %q: %w", chatID, err)
	}
	return cid, tid, nil
}

func resolveTelegramOutboundTarget(chatID string, outboundCtx *bus.InboundContext) (int64, int, error) {
	targetChatID := strings.TrimSpace(chatID)
	if targetChatID == "" && outboundCtx != nil {
		targetChatID = strings.TrimSpace(outboundCtx.ChatID)
	}
	resolvedChatID, resolvedThreadID, err := parseTelegramChatID(targetChatID)
	if err != nil {
		return 0, 0, err
	}
	if resolvedThreadID != 0 || outboundCtx == nil {
		return resolvedChatID, resolvedThreadID, nil
	}
	topicID := strings.TrimSpace(outboundCtx.TopicID)
	if topicID == "" {
		return resolvedChatID, resolvedThreadID, nil
	}
	if threadID, convErr := strconv.Atoi(topicID); convErr == nil {
		return resolvedChatID, threadID, nil
	}
	return resolvedChatID, resolvedThreadID, nil
}

func logParseFailed(err error, useMarkdownV2 bool) {
	parsingName := "HTML"
	if useMarkdownV2 {
		parsingName = "MarkdownV2"
	}

	logger.ErrorCF("telegram",
		fmt.Sprintf("%s parse failed, falling back to plain text", parsingName),
		map[string]any{
			"error": err.Error(),
		},
	)
}

// isBotMentioned checks if the bot is mentioned in the message via entities.
func (c *TelegramChannel) isBotMentioned(message *telego.Message) bool {
	text, entities := telegramEntityTextAndList(message)
	if text == "" || len(entities) == 0 {
		return false
	}

	botUsername := ""
	if c.bot != nil {
		botUsername = c.bot.Username()
	}
	for _, entity := range entities {
		entityText, ok := telegramEntityText(text, entity)
		if !ok {
			continue
		}

		switch entity.Type {
		case telego.EntityTypeMention:
			if botUsername != "" && strings.EqualFold(entityText, "@"+botUsername) {
				return true
			}
		case telego.EntityTypeTextMention:
			if botUsername != "" && entity.User != nil && strings.EqualFold(entity.User.Username, botUsername) {
				return true
			}
		case telego.EntityTypeBotCommand:
			if isBotCommandEntityForThisBot(entityText, botUsername) {
				return true
			}
		}
	}
	return false
}

func telegramEntityTextAndList(message *telego.Message) (string, []telego.MessageEntity) {
	if message.Text != "" {
		return message.Text, message.Entities
	}
	return message.Caption, message.CaptionEntities
}

func telegramEntityText(text string, entity telego.MessageEntity) (string, bool) {
	if entity.Offset < 0 || entity.Length <= 0 {
		return "", false
	}
	// Telegram entity offsets are UTF-16 code units, not Unicode code points.
	units := utf16.Encode([]rune(text))
	end := entity.Offset + entity.Length
	if entity.Offset >= len(units) || end > len(units) {
		return "", false
	}
	return string(utf16.Decode(units[entity.Offset:end])), true
}

func isBotCommandEntityForThisBot(entityText, botUsername string) bool {
	if !strings.HasPrefix(entityText, "/") {
		return false
	}
	command := strings.TrimPrefix(entityText, "/")
	if command == "" {
		return false
	}

	at := strings.IndexRune(command, '@')
	if at == -1 {
		// A bare /command delivered to this bot is intended for this bot.
		return true
	}

	mentionUsername := command[at+1:]
	if mentionUsername == "" || botUsername == "" {
		return false
	}
	return strings.EqualFold(mentionUsername, botUsername)
}

// stripBotMention removes the @bot mention from the content.
func (c *TelegramChannel) stripBotMention(content string) string {
	botUsername := c.bot.Username()
	if botUsername == "" {
		return content
	}
	// Case-insensitive replacement
	re := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(botUsername))
	content = re.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}

// BeginStream implements channels.StreamingCapable.
func (c *TelegramChannel) BeginStream(ctx context.Context, chatID string) (channels.Streamer, error) {
	if !c.tgCfg.Streaming.Enabled {
		return nil, fmt.Errorf("streaming disabled in config")
	}

	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return nil, err
	}

	streamCfg := c.tgCfg.Streaming.WithDefaults(3, 200)
	return &telegramStreamer{
		bot:              c.bot,
		chatID:           cid,
		threadID:         threadID,
		richMessages:     c.tgCfg.RichMessages,
		throttleInterval: time.Duration(streamCfg.ThrottleSeconds) * time.Second,
		minGrowth:        streamCfg.MinGrowthChars,
	}, nil
}

// telegramStreamer streams by sending one persistent preview and editing it in
// place. Unlike sendMessageDraft, this works in DMs, groups, and forum topics,
// and finalization does not create a duplicate answer bubble.
type telegramStreamer struct {
	bot              *telego.Bot
	chatID           int64
	threadID         int
	richMessages     bool
	throttleInterval time.Duration
	minGrowth        int
	lastLen          int
	lastAt           time.Time
	failed           bool
	messageID        int
	mu               sync.Mutex
}

func (s *telegramStreamer) Update(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failed {
		return fmt.Errorf("telegram streaming disabled after previous preview failure")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	// Throttle: skip if not enough time or content has passed
	now := time.Now()
	growth := len(content) - s.lastLen
	if s.lastLen > 0 && now.Sub(s.lastAt) < s.throttleInterval && growth < s.minGrowth {
		return nil
	}

	preview := fitTelegramStreamPreview(content)
	var err error
	if s.messageID == 0 {
		s.messageID, err = s.send(ctx, preview)
	} else {
		err = s.edit(ctx, s.messageID, preview)
	}
	if err != nil {
		logger.WarnCF("telegram", "Telegram preview update failed, disabling streaming", map[string]any{
			"error": err.Error(),
		})
		s.failed = true
		return fmt.Errorf("telegram preview update: %w", err)
	}

	s.lastLen = len(content)
	s.lastAt = now
	return nil
}

func (s *telegramStreamer) Finalize(ctx context.Context, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chunks := channels.SplitMessage(strings.TrimSpace(content), 4000)
	if len(chunks) == 0 {
		return nil
	}

	if s.messageID == 0 {
		messageID, err := s.send(ctx, chunks[0])
		if err != nil {
			return fmt.Errorf("telegram finalize: %w", err)
		}
		s.messageID = messageID
	} else if err := s.edit(ctx, s.messageID, chunks[0]); err != nil {
		// The preview is only provisional. If it cannot be finalized, remove it
		// and fall back to a normal final send.
		_ = s.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
			ChatID: tu.ID(s.chatID), MessageID: s.messageID,
		})
		s.messageID = 0
		messageID, sendErr := s.send(ctx, chunks[0])
		if sendErr != nil {
			return fmt.Errorf("telegram finalize after edit failure: %w", sendErr)
		}
		s.messageID = messageID
	}

	for _, chunk := range chunks[1:] {
		if _, err := s.send(ctx, chunk); err != nil {
			return fmt.Errorf("telegram finalize continuation: %w", err)
		}
	}
	return nil
}

func (s *telegramStreamer) Cancel(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messageID == 0 {
		return
	}
	_ = s.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID: tu.ID(s.chatID), MessageID: s.messageID,
	})
	s.messageID = 0
}

func (s *telegramStreamer) send(ctx context.Context, content string) (int, error) {
	parsed := markdownToTelegramHTML(content)
	if s.richMessages {
		sent, err := s.bot.SendRichMessage(ctx, &telego.SendRichMessageParams{
			ChatID:          tu.ID(s.chatID),
			MessageThreadID: s.threadID,
			RichMessage:     telego.InputRichMessage{HTML: parsed},
		})
		if err == nil {
			return sent.MessageID, nil
		}
		logger.WarnCF("telegram", "Rich preview send failed; falling back to standard HTML", map[string]any{
			"error": err.Error(),
		})
	}
	msg := tu.Message(tu.ID(s.chatID), parsed)
	msg.MessageThreadID = s.threadID
	msg.ParseMode = telego.ModeHTML
	sent, err := s.bot.SendMessage(ctx, msg)
	if err != nil && strings.Contains(err.Error(), "Bad Request") {
		msg.Text = content
		msg.ParseMode = ""
		sent, err = s.bot.SendMessage(ctx, msg)
	}
	if err != nil {
		return 0, err
	}
	return sent.MessageID, nil
}

func (s *telegramStreamer) edit(ctx context.Context, messageID int, content string) error {
	parsed := markdownToTelegramHTML(content)
	params := tu.EditMessageText(tu.ID(s.chatID), messageID, parsed)
	if s.richMessages {
		params.Text = ""
		params.RichMessage = &telego.InputRichMessage{HTML: parsed}
	} else {
		params.ParseMode = telego.ModeHTML
	}
	_, err := s.bot.EditMessageText(ctx, params)
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	if err != nil && strings.Contains(err.Error(), "Bad Request") {
		_, err = s.bot.EditMessageText(ctx, tu.EditMessageText(tu.ID(s.chatID), messageID, content))
	}
	return err
}

func fitTelegramStreamPreview(content string) string {
	if len([]rune(markdownToTelegramHTML(content))) <= 4096 {
		return content
	}
	low, high := 1, len([]rune(content))
	best := utils.Truncate(content, 1)
	for low <= high {
		mid := (low + high) / 2
		candidate := utils.Truncate(content, mid)
		if len([]rune(markdownToTelegramHTML(candidate))) <= 4096 {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best
}

// isPostConnectError identifies network errors that likely occurred after
// the request was transmitted to Telegram (e.g. dropped connection while
// waiting for response). Swallowing these for edits prevents duplicate
// fallbacks, at the small risk of leaving a stale placeholder if the
// edit never actually reached the server.
func isPostConnectError(err error) bool {
	if err == nil {
		return false
	}

	// Context errors (timeout/canceled) are too broad; they can be triggered
	// locally before any data is sent. Never swallow them.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}

	msg := strings.ToLower(err.Error())
	// Narrowly target connection dropouts where the request likely landed.
	return strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection closed by foreign host") ||
		strings.Contains(msg, "broken pipe")
}

// VoiceCapabilities returns the voice capabilities of the channel.
func (c *TelegramChannel) VoiceCapabilities() channels.VoiceCapabilities {
	return channels.VoiceCapabilities{ASR: true, TTS: true}
}

// processSpoolWorker constantly checks the SQLite queue for pending messages
// and processes them. It guarantees that if the app restarts, unprocessed
// messages are safely picked back up.
func (c *TelegramChannel) processSpoolWorker() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// Fetch next message
			msgRow, err := c.spool.Dequeue()
			if err != nil {
				logger.WarnCF("telegram", "Error reading from ingress spool", map[string]any{
					"error": err.Error(),
				})
				time.Sleep(1 * time.Second) // backoff
				continue
			}

			// If queue is empty, do nothing
			if msgRow == nil {
				continue
			}

			// Decode the Telegram message
			var tgMsg telego.Message
			if err := json.Unmarshal(msgRow.Payload, &tgMsg); err != nil {
				logger.WarnCF("telegram", "Corrupt message in spool, dropping", map[string]any{
					"error": err.Error(),
					"id":    msgRow.ID,
				})
				_ = c.spool.Acknowledge(msgRow.ID)
				continue
			}

			// Process it exactly like normal. A real context is required for media
			// downloads; passing nil here silently discarded single attachments.
			err = c.handleMessage(c.ctx, &tgMsg)
			if err != nil {
				logger.WarnCF("telegram", "Failed to process spooled message", map[string]any{
					"error": err.Error(),
					"id":    msgRow.ID,
				})
				attempt := msgRow.Attempts + 1
				const maxAttempts = 5
				if attempt >= maxAttempts {
					logger.ErrorCF("telegram", "Dropping poison message after retry limit", map[string]any{
						"id":       msgRow.ID,
						"attempts": attempt,
					})
					_ = c.spool.Acknowledge(msgRow.ID)
					continue
				}
				delay := 100 * time.Millisecond * time.Duration(1<<min(attempt-1, 5))
				timer := time.NewTimer(delay)
				select {
				case <-c.ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			} else {
				// Success! Remove from spool.
				_ = c.spool.Acknowledge(msgRow.ID)
			}
		}
	}
}
