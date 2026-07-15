package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	oauthprovider "github.com/sipeed/picoclaw/pkg/providers/oauth"
	toolshared "github.com/sipeed/picoclaw/pkg/tools/shared"
)

// ImageGenerationTool calls an OpenAI-compatible /images/generations endpoint.
// This covers OpenAI, OpenRouter, and compatible gateways configured in model_list.
type ImageGenerationTool struct {
	model       string
	apiBase     string
	apiKey      string
	client      *http.Client
	mediaStore  media.MediaStore
	maxFileSize int
	antigravity antigravityImageGenerator
}

type antigravityImageGenerator interface {
	GenerateImage(context.Context, string, string, string) ([]oauthprovider.AntigravityGeneratedImage, error)
}

func NewImageGenerationTool(model *config.ModelConfig, maxFileSize int, nativeProviders ...any) *ImageGenerationTool {
	if maxFileSize <= 0 {
		maxFileSize = config.DefaultMaxMediaSize
	}
	t := &ImageGenerationTool{client: &http.Client{Timeout: 3 * time.Minute}, maxFileSize: maxFileSize}
	if model != nil {
		protocol, modelID := providers.ExtractProtocol(model)
		t.model = modelID
		t.apiBase = strings.TrimRight(strings.TrimSpace(model.APIBase), "/")
		t.apiKey = model.APIKey()
		if protocol == "antigravity" || protocol == "google-antigravity" {
			t.antigravity = oauthprovider.NewAntigravityProvider()
		}
	}
	if t.antigravity == nil {
		for _, candidate := range nativeProviders {
			if native, ok := candidate.(antigravityImageGenerator); ok {
				t.antigravity = native
				break
			}
		}
	}
	return t
}

func (t *ImageGenerationTool) SetMediaStore(store media.MediaStore) { t.mediaStore = store }
func (t *ImageGenerationTool) Available() bool                      { return t.antigravity != nil || t.apiBase != "" }
func (t *ImageGenerationTool) Name() string                         { return "image_generate" }
func (t *ImageGenerationTool) Description() string {
	return "Generate an image from a text prompt and send it to the user. Use this when the user asks to create, draw, or visualize an image."
}
func (t *ImageGenerationTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"prompt":  map[string]any{"type": "string", "description": "Detailed description of the image to create."},
		"size":    map[string]any{"type": "string", "description": "Image size, for example 1024x1024, 1792x1024, or 1024x1792."},
		"quality": map[string]any{"type": "string", "enum": []string{"standard", "hd"}},
	}, "required": []string{"prompt"}}
}

func (t *ImageGenerationTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	prompt, _ := args["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return toolshared.ErrorResult("image generation requires a non-empty prompt")
	}
	if t.mediaStore == nil {
		return toolshared.ErrorResult("image generation is unavailable because the media store is not initialized")
	}
	if t.antigravity != nil {
		size, _ := args["size"].(string)
		images, err := t.antigravity.GenerateImage(ctx, prompt, t.model, aspectRatioFromSize(size))
		if err != nil {
			return toolshared.ErrorResult("antigravity image generation failed: " + err.Error())
		}
		if len(images) == 0 {
			return toolshared.ErrorResult("antigravity image generation returned no image")
		}
		return t.storeGeneratedImage(ctx, images[0].Data, images[0].MIMEType)
	}
	if t.apiBase == "" {
		return toolshared.ErrorResult("image generation is not configured: set api_base on the active model")
	}

	body := map[string]any{"model": t.model, "prompt": prompt, "n": 1, "response_format": "b64_json"}
	if size, ok := args["size"].(string); ok && strings.TrimSpace(size) != "" {
		body["size"] = strings.TrimSpace(size)
	}
	if quality, ok := args["quality"].(string); ok && strings.TrimSpace(quality) != "" {
		body["quality"] = strings.TrimSpace(quality)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	endpoint := imageEndpoint(t.apiBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return toolshared.ErrorResult("image generation request failed: " + err.Error())
	}
	defer resp.Body.Close()
	// Base64 expands the image by roughly 4/3, so allow enough room for the
	// encoded JSON while still bounding responses from a broken endpoint.
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxFileSize)*2+1))
	if err != nil {
		return toolshared.ErrorResult("failed to read image response: " + err.Error())
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return toolshared.ErrorResult(fmt.Sprintf("image generation API returned %s: %s", resp.Status, truncate(string(data), 500)))
	}
	var result struct {
		Data []struct {
			B64 string `json:"b64_json"`
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return toolshared.ErrorResult("invalid image generation response: " + err.Error())
	}
	if len(result.Data) == 0 {
		return toolshared.ErrorResult("image generation API returned no image")
	}
	img := result.Data[0]
	var imageBytes []byte
	if img.B64 != "" {
		imageBytes, err = base64.StdEncoding.DecodeString(img.B64)
	} else if img.URL != "" {
		imageBytes, err = downloadImage(ctx, img.URL, t.maxFileSize)
	} else {
		err = fmt.Errorf("response contained neither b64_json nor url")
	}
	if err != nil {
		return toolshared.ErrorResult("failed to decode generated image: " + err.Error())
	}
	return t.storeGeneratedImage(ctx, imageBytes, "image/png")
}

func (t *ImageGenerationTool) storeGeneratedImage(ctx context.Context, imageBytes []byte, mimeType string) *toolshared.ToolResult {
	if len(imageBytes) == 0 {
		return toolshared.ErrorResult("image generation returned an empty image")
	}
	if len(imageBytes) > t.maxFileSize {
		return toolshared.ErrorResult("generated image exceeds the configured media size limit")
	}
	extension := ".png"
	if mimeType == "image/jpeg" {
		extension = ".jpg"
	} else if mimeType == "image/webp" {
		extension = ".webp"
	} else if mimeType == "" {
		mimeType = "image/png"
	}
	filename := "generated-image" + extension
	path := filepath.Join(os.TempDir(), "picoclaw-image-"+fmt.Sprintf("%d", time.Now().UnixNano())+extension)
	if err := os.WriteFile(path, imageBytes, 0o600); err != nil {
		return toolshared.ErrorResult("failed to save generated image: " + err.Error())
	}
	scope := toolshared.ToolSessionKey(ctx)
	if scope == "" {
		scope = "image-generation"
	}
	ref, err := t.mediaStore.Store(path, media.MediaMeta{Filename: filename, ContentType: mimeType, Source: "tool:image-gen"}, scope)
	if err != nil {
		_ = os.Remove(path)
		return toolshared.ErrorResult("failed to register generated image: " + err.Error())
	}
	return toolshared.MediaResult("Generated image attached.", []string{ref}).WithResponseHandled()
}

func aspectRatioFromSize(size string) string {
	var width, height int
	if _, err := fmt.Sscanf(strings.TrimSpace(size), "%dx%d", &width, &height); err != nil || width <= 0 || height <= 0 {
		return "1:1"
	}
	gcd := width
	for n := height; n != 0; {
		gcd, n = n, gcd%n
	}
	return fmt.Sprintf("%d:%d", width/gcd, height/gcd)
}

func imageEndpoint(base string) string {
	if strings.HasSuffix(base, "/images/generations") {
		return base
	}
	return strings.TrimRight(base, "/") + "/images/generations"
}

func downloadImage(ctx context.Context, rawURL string, max int) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid image URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image download returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
