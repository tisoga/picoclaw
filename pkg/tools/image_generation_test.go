package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	oauthprovider "github.com/sipeed/picoclaw/pkg/providers/oauth"
)

type fakeAntigravityImageGenerator struct {
	model       string
	aspectRatio string
}

func (f *fakeAntigravityImageGenerator) GenerateImage(_ context.Context, _ string, model string, aspectRatio string) ([]oauthprovider.AntigravityGeneratedImage, error) {
	f.model = model
	f.aspectRatio = aspectRatio
	return []oauthprovider.AntigravityGeneratedImage{{Data: []byte("antigravity-image"), MIMEType: "image/webp"}}, nil
}

func TestImageGenerationToolUsesAntigravityOAuthProvider(t *testing.T) {
	tool := NewImageGenerationTool(&config.ModelConfig{Provider: "google-antigravity", Model: "gemini-3-flash"}, 1024)
	if tool.antigravity == nil {
		t.Fatal("Antigravity model did not select the native image generator")
	}
	fake := &fakeAntigravityImageGenerator{}
	tool.antigravity = fake
	store := media.NewFileMediaStore()
	tool.SetMediaStore(store)
	result := tool.Execute(context.Background(), map[string]any{"prompt": "wide landscape", "size": "1792x1024"})
	if result.IsError || len(result.Media) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if fake.model != "gemini-3-flash" || fake.aspectRatio != "7:4" {
		t.Fatalf("model = %q, aspect ratio = %q", fake.model, fake.aspectRatio)
	}
	_, meta, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseAll("image-generation")
	if meta.ContentType != "image/webp" || meta.Filename != "generated-image.webp" {
		t.Fatalf("meta = %#v", meta)
	}
}

func TestImageGenerationToolStoresGeneratedImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["prompt"] != "a red kite" || body["model"] != "image-model" {
			t.Errorf("body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString([]byte("fake-png")) + `"}]}`))
	}))
	defer server.Close()

	tool := NewImageGenerationTool(&config.ModelConfig{Model: "openai/image-model", APIBase: server.URL + "/v1", APIKeys: config.SimpleSecureStrings("test-key")}, 1024)
	store := media.NewFileMediaStore()
	tool.SetMediaStore(store)
	result := tool.Execute(context.Background(), map[string]any{"prompt": "a red kite"})
	if result.IsError || len(result.Media) != 1 || !result.ResponseHandled {
		t.Fatalf("result = %#v", result)
	}
	path, meta, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseAll("image-generation")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fake-png" || meta.ContentType != "image/png" {
		t.Errorf("stored image = %q, meta = %#v", data, meta)
	}
}

func TestImageEndpoint(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.example/v1":                 "https://api.example/v1/images/generations",
		"https://api.example/v1/":                "https://api.example/v1/images/generations",
		"https://api.example/images/generations": "https://api.example/images/generations",
	} {
		if got := imageEndpoint(base); got != want {
			t.Errorf("imageEndpoint(%q) = %q, want %q", base, got, want)
		}
	}
}
