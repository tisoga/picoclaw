package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/utils"
)

// webhookSendRequest is the expected JSON body for the webhook endpoint.
type webhookSendRequest struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	Message  string `json:"message"`
	MediaURL string `json:"media_url,omitempty"`
}

// registerWebhookHandler mounts the inbound webhook endpoint on the channel
// manager's shared HTTP server when gateway.webhook is enabled.
func registerWebhookHandler(cfg config.GatewayWebhookConfig, cm *channels.Manager) {
	if !cfg.Enabled {
		return
	}
	token := cfg.Token.String()
	if token == "" {
		logger.WarnCF(
			"gateway",
			"Webhook enabled without a token — skipping",
			nil,
		)
		return
	}

	path := cfg.EffectiveWebhookPath()
	cm.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		handleWebhookSend(w, r, cfg, token, cm)
	})

	logger.InfoCF("gateway", "Webhook endpoint registered", map[string]any{
		"path":             path,
		"allowed_channels": cfg.AllowedChannels,
		"allowed_ips":      cfg.AllowedIPs,
		"allow_all_ips":    cfg.AllowAllIPs,
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
	if !webhookRequestAuthorized(cfg, token, reqToken, r.RemoteAddr) {
		logger.WarnCF("gateway", "Webhook unauthorized attempt", map[string]any{
			"remote_addr": r.RemoteAddr,
		})
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	req, mediaPath, filename, contentType, err := parseWebhookRequest(w, r, cfg)
	if err != nil {
		logger.WarnCF("gateway", "Webhook invalid request", map[string]any{"error": err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if mediaPath != "" {
		defer os.Remove(mediaPath)
	}

	if req.Channel == "" || req.ChatID == "" || (req.Message == "" && mediaPath == "") {
		logger.WarnCF("gateway", "Webhook missing fields", map[string]any{
			"channel":     req.Channel,
			"chat_id":     req.ChatID,
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

	if mediaPath != "" {
		mediaType := "file"
		if strings.HasPrefix(contentType, "image/") {
			mediaType = "image"
		}
		err = cm.SendLocalMediaToChannel(
			r.Context(), req.Channel, req.ChatID, req.Message, mediaType,
			mediaPath, filename, contentType,
		)
	} else {
		err = cm.SendToChannel(r.Context(), req.Channel, req.ChatID, req.Message)
	}
	if err != nil {
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

func parseWebhookRequest(
	w http.ResponseWriter,
	r *http.Request,
	cfg config.GatewayWebhookConfig,
) (webhookSendRequest, string, string, string, error) {
	contentType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return webhookSendRequest{}, "", "", "", fmt.Errorf("invalid Content-Type")
	}
	if contentType == "multipart/form-data" {
		return parseWebhookMultipart(w, r, cfg, params["boundary"])
	}
	if contentType != "application/json" {
		return webhookSendRequest{}, "", "", "", fmt.Errorf(
			"Content-Type must be application/json or multipart/form-data",
		)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return webhookSendRequest{}, "", "", "", fmt.Errorf("failed to read body")
	}
	defer r.Body.Close()
	var req webhookSendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return webhookSendRequest{}, "", "", "", fmt.Errorf("invalid JSON")
	}
	if strings.TrimSpace(req.MediaURL) == "" {
		return req, "", "", "", nil
	}
	path, filename, detectedType, err := downloadWebhookMedia(
		r.Context(), req.MediaURL, cfg.EffectiveMediaMaxBytes(),
	)
	return req, path, filename, detectedType, err
}

func parseWebhookMultipart(
	w http.ResponseWriter,
	r *http.Request,
	cfg config.GatewayWebhookConfig,
	boundary string,
) (webhookSendRequest, string, string, string, error) {
	if boundary == "" {
		return webhookSendRequest{}, "", "", "", fmt.Errorf("missing multipart boundary")
	}
	maxBytes := cfg.EffectiveMediaMaxBytes()
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+(1<<20))
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		return webhookSendRequest{}, "", "", "", fmt.Errorf(
			"invalid multipart form or image exceeds %d MB", maxBytes/(1024*1024),
		)
	}
	req := webhookSendRequest{
		Channel: r.FormValue("channel"),
		ChatID:  r.FormValue("chat_id"),
		Message: r.FormValue("message"),
	}
	file, header, err := r.FormFile("media")
	if err != nil {
		return req, "", "", "", fmt.Errorf("multipart field media is required")
	}
	defer file.Close()
	path, filename, contentType, err := storeWebhookMedia(file, header.Filename, maxBytes)
	return req, path, filename, contentType, err
}

func downloadWebhookMedia(
	ctx context.Context,
	rawURL string,
	maxBytes int64,
) (string, string, string, error) {
	if err := utils.ValidateSafeHTTPURL(rawURL, nil, nil); err != nil {
		return "", "", "", fmt.Errorf("unsafe media_url: %w", err)
	}
	client, err := utils.CreateSafeHTTPClient(utils.SafeHTTPClientOptions{
		Timeout:      30 * time.Second,
		MaxRedirects: 5,
	})
	if err != nil {
		return "", "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("invalid media_url")
	}
	response, err := client.Do(request)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to download media_url: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("media_url returned HTTP %d", response.StatusCode)
	}
	parsed, _ := url.Parse(rawURL)
	filename := filepath.Base(parsed.Path)
	if filename == "." || filename == "/" || filename == "" {
		filename = "webhook-image"
	}
	return storeWebhookMedia(response.Body, filename, maxBytes)
}

func storeWebhookMedia(
	reader io.Reader,
	filename string,
	maxBytes int64,
) (string, string, string, error) {
	tempDir := media.TempDir()
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return "", "", "", err
	}
	temp, err := os.CreateTemp(tempDir, "webhook-media-*")
	if err != nil {
		return "", "", "", err
	}
	path := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(path)
	}
	written, err := io.Copy(temp, io.LimitReader(reader, maxBytes+1))
	if err != nil {
		cleanup()
		return "", "", "", err
	}
	if written > maxBytes {
		cleanup()
		return "", "", "", fmt.Errorf(
			"image exceeds %d MB", maxBytes/(1024*1024),
		)
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return "", "", "", err
	}
	head := make([]byte, 512)
	n, _ := temp.Read(head)
	detectedType := http.DetectContentType(head[:n])
	if err := temp.Close(); err != nil {
		_ = os.Remove(path)
		return "", "", "", err
	}
	return path, utils.SanitizeFilename(filename), detectedType, nil
}

func webhookRequestAuthorized(
	cfg config.GatewayWebhookConfig,
	token string,
	requestToken string,
	remoteAddr string,
) bool {
	tokenAllowed := token != "" &&
		subtle.ConstantTimeCompare([]byte(requestToken), []byte(token)) == 1
	if !tokenAllowed {
		return false
	}
	if cfg.AllowAllIPs || len(cfg.AllowedIPs) == 0 {
		return true
	}
	return cfg.IsIPAllowed(remoteAddr)
}
