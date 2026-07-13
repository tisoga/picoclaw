package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// webhookSendRequest is the expected JSON body for the webhook endpoint.
type webhookSendRequest struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
	Message string `json:"message"`
}

// registerWebhookHandler mounts the inbound webhook endpoint on the channel
// manager's shared HTTP server when gateway.webhook is enabled.
func registerWebhookHandler(cfg config.GatewayWebhookConfig, cm *channels.Manager) {
	if !cfg.Enabled {
		return
	}
	token := cfg.Token.String()
	if token == "" {
		logger.WarnCF("gateway", "Webhook enabled but token is empty — skipping", nil)
		return
	}

	path := cfg.EffectiveWebhookPath()
	cm.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		handleWebhookSend(w, r, cfg, token, cm)
	})

	logger.InfoCF("gateway", "Webhook endpoint registered", map[string]any{
		"path":             path,
		"allowed_channels": cfg.AllowedChannels,
	})
}

func handleWebhookSend(
	w http.ResponseWriter,
	r *http.Request,
	cfg config.GatewayWebhookConfig,
	token string,
	cm *channels.Manager,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	reqToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(reqToken), []byte(token)) != 1 {
		logger.WarnCF("gateway", "Webhook unauthorized attempt", map[string]any{
			"remote_addr": r.RemoteAddr,
		})
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		logger.WarnCF("gateway", "Webhook failed to read body", map[string]any{"error": err.Error()})
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req webhookSendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		logger.WarnCF("gateway", "Webhook invalid JSON", map[string]any{"error": err.Error(), "body": string(body)})
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Channel == "" || req.ChatID == "" || req.Message == "" {
		logger.WarnCF("gateway", "Webhook missing fields", map[string]any{
			"channel": req.Channel,
			"chat_id": req.ChatID,
			"message_len": len(req.Message),
		})
		http.Error(w, `Required fields: "channel", "chat_id", "message"`, http.StatusBadRequest)
		return
	}

	if !cfg.IsChannelAllowed(req.Channel) {
		logger.WarnCF("gateway", "Webhook channel not allowed", map[string]any{
			"requested_channel": req.Channel,
			"allowed_channels":  cfg.AllowedChannels,
		})
		http.Error(w, "Channel not allowed", http.StatusForbidden)
		return
	}

	if err := cm.SendToChannel(r.Context(), req.Channel, req.ChatID, req.Message); err != nil {
		logger.ErrorCF("gateway", "Webhook send failed", map[string]any{
			"channel": req.Channel,
			"chat_id": req.ChatID,
			"error":   err.Error(),
		})
		http.Error(w, "Failed to send message: "+err.Error(), http.StatusBadGateway)
		return
	}

	logger.InfoCF("gateway", "Webhook message sent", map[string]any{
		"channel": req.Channel,
		"chat_id": req.ChatID,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
