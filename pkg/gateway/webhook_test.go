package gateway

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestWebhookRequestAuthorizedRequiresTokenWithAllowAllIPs(t *testing.T) {
	cfg := config.GatewayWebhookConfig{AllowAllIPs: true}
	if webhookRequestAuthorized(cfg, "secret", "", "203.0.113.10:1234") {
		t.Fatal("allow_all_ips authorized a request without a token")
	}
	if !webhookRequestAuthorized(cfg, "secret", "secret", "203.0.113.10:1234") {
		t.Fatal("allow_all_ips rejected a request with the valid token")
	}
}

func TestWebhookRequestAuthorizedAppliesIPAllowlistAfterToken(t *testing.T) {
	cfg := config.GatewayWebhookConfig{AllowedIPs: []string{"192.168.1.0/24"}}
	if webhookRequestAuthorized(cfg, "secret", "secret", "203.0.113.10:1234") {
		t.Fatal("valid token bypassed the IP allowlist")
	}
	if !webhookRequestAuthorized(cfg, "secret", "secret", "192.168.1.10:1234") {
		t.Fatal("valid token from an allowed IP was rejected")
	}
}

func TestParseWebhookMultipartImageWithCaption(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("channel", "whatsapp")
	_ = writer.WriteField("chat_id", "123@s.whatsapp.net")
	_ = writer.WriteField("message", "**caption**")
	file, err := writer.CreateFormFile("media", "result.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(append(
		[]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		make([]byte, 512)...,
	))
	_ = writer.Close()

	request := httptest.NewRequest(http.MethodPost, "/webhook/send", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	req, path, filename, contentType, err := parseWebhookRequest(
		response, request, config.GatewayWebhookConfig{MediaMaxMB: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if req.Message != "**caption**" || filename != "result.png" ||
		contentType != "image/png" {
		t.Fatalf("request=%#v filename=%q contentType=%q", req, filename, contentType)
	}
}

func TestDownloadWebhookMediaBlocksPrivateURL(t *testing.T) {
	_, _, _, err := downloadWebhookMedia(
		t.Context(), "http://127.0.0.1/private.png", 1<<20,
	)
	if err == nil {
		t.Fatal("private media_url was not blocked")
	}
}

func TestParseWebhookMultipartDocumentWithCaption(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("channel", "telegram")
	_ = writer.WriteField("chat_id", "123")
	_ = writer.WriteField("message", "Monthly report")
	file, err := writer.CreateFormFile("media", "report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("%PDF-1.7\nwebhook document"))
	_ = writer.Close()

	request := httptest.NewRequest(http.MethodPost, "/webhook/send", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	req, path, filename, contentType, err := parseWebhookRequest(
		response, request, config.GatewayWebhookConfig{MediaMaxMB: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if req.Message != "Monthly report" || filename != "report.pdf" ||
		contentType != "application/pdf" {
		t.Fatalf("request=%#v filename=%q contentType=%q", req, filename, contentType)
	}
}
