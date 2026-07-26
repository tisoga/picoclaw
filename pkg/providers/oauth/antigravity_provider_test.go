package oauthprovider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestDefaultThoughtSignatureIsBase64(t *testing.T) {
	decoded, err := base64.StdEncoding.DecodeString(antigravityDefaultThoughtSignature)
	if err != nil {
		t.Fatalf("fallback thought signature is not base64: %v", err)
	}
	if len(decoded) < 512 {
		t.Fatalf("fallback thought signature is unexpectedly short: %d bytes", len(decoded))
	}
	if antigravityDefaultThoughtSignature == antigravityLegacyThoughtSignature {
		t.Fatal("fallback thought signature still uses the legacy placeholder")
	}
}

func TestResolveAntigravityGemini36Aliases(t *testing.T) {
	for _, tt := range []struct {
		alias string
		level string
	}{
		{"gemini-3.6-flash-high", "high"},
		{"gemini-3.6-flash-medium", "medium"},
		{"gemini-3.6-flash-low", "low"},
	} {
		model, level := resolveAntigravityModel(
			tt.alias,
			map[string]any{"thinking_level": "high"},
		)
		if model != "gemini-3.6-flash-tiered" || level != tt.level {
			t.Errorf("%s resolved to model=%q level=%q", tt.alias, model, level)
		}
	}
}

func TestBuildRequestIncludesGemini36ThinkingConfig(t *testing.T) {
	p := &AntigravityProvider{}
	model, level := resolveAntigravityModel("gemini-3.6-flash-medium", nil)
	req := p.buildRequest(
		[]Message{{Role: "user", Content: "hello"}},
		nil,
		model,
		nil,
		"session-test",
	)
	if req.Config == nil {
		req.Config = &antigravityGenConfig{}
	}
	req.Config.Thinking = &antigravityThinkingConfig{
		ThinkingLevel:   level,
		IncludeThoughts: true,
	}
	if req.Config.Thinking.ThinkingLevel != "medium" ||
		!req.Config.Thinking.IncludeThoughts {
		t.Fatalf("thinking config = %#v", req.Config.Thinking)
	}
}

func TestBuildRequestReplacesLegacyThoughtSignature(t *testing.T) {
	p := &AntigravityProvider{}
	req := p.buildRequest([]Message{{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:               "call_legacy",
			Name:             "read_file",
			Arguments:        map[string]any{"path": "README.md"},
			ThoughtSignature: antigravityLegacyThoughtSignature,
		}},
	}}, nil, "gemini-3-flash", nil, "session-test")

	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 {
		t.Fatalf("unexpected request contents: %#v", req.Contents)
	}
	part := req.Contents[0].Parts[0]
	if part.ThoughtSignature != antigravityDefaultThoughtSignature ||
		part.ThoughtSignatureSnake != antigravityDefaultThoughtSignature {
		t.Fatalf("legacy thought signature was not replaced")
	}
}

func TestGenerateImageUsesNativeAntigravityRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1internal:generateContent" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "antigravity/ide/2.1.1 darwin/arm64" {
			t.Errorf("user-agent = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gemini-3.1-flash-image" || body["requestType"] != "image_gen" {
			t.Errorf("envelope = %#v", body)
		}
		requestID, _ := body["requestId"].(string)
		if matched, _ := regexp.MatchString(
			`^agent/[0-9a-f-]{36}/[0-9]+/[0-9a-f-]{36}/1$`,
			requestID,
		); !matched {
			t.Errorf("requestId = %q", requestID)
		}
		request := body["request"].(map[string]any)
		generation := request["generationConfig"].(map[string]any)
		imageConfig := generation["imageConfig"].(map[string]any)
		if imageConfig["aspectRatio"] != "16:9" {
			t.Errorf("imageConfig = %#v", imageConfig)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte("native-image"))
		_, _ = w.Write([]byte(`{"response":{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + encoded + `"}}]}}]}}`))
	}))
	defer server.Close()

	p := &AntigravityProvider{
		tokenSource: func() (string, string, error) { return "access-token", "project-id", nil },
		httpClient:  server.Client(),
		baseURL:     server.URL,
	}
	images, err := p.GenerateImage(context.Background(), "sunset", "gemini-3-flash", "16:9")
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || string(images[0].Data) != "native-image" || images[0].MIMEType != "image/png" {
		t.Fatalf("images = %#v", images)
	}
}

func TestChatUsesCurrentAntigravityIDERequest(t *testing.T) {
	requestIDPattern := regexp.MustCompile(
		`^agent/[0-9a-f-]{36}/[0-9]+/[0-9a-f-]{36}/[0-9]+$`,
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.URL.RequestURI() != "/v1internal:streamGenerateContent?alt=sse" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "antigravity/ide/2.1.1 darwin/arm64" {
			t.Errorf("user-agent = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "gemini-3-flash" || body["requestType"] != "agent" {
			t.Errorf("envelope = %#v", body)
		}
		requestID, _ := body["requestId"].(string)
		if !requestIDPattern.MatchString(requestID) {
			t.Errorf("requestId = %q", requestID)
		}
		request, _ := body["request"].(map[string]any)
		if sessionID, _ := request["sessionId"].(string); sessionID == "" {
			t.Errorf("request sessionId is missing: %#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}]}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()

	p := &AntigravityProvider{
		tokenSource: func() (string, string, error) { return "access-token", "project-id", nil },
		httpClient:  server.Client(),
		baseURL:     server.URL,
	}
	resp, err := p.Chat(
		context.Background(),
		[]Message{{Role: "user", Content: "hello"}},
		nil,
		"antigravity/gemini-3-flash",
		map[string]any{"max_tokens": 128},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
}

func TestBuildRequestSanitizesAntigravityToolSchema(t *testing.T) {
	p := &AntigravityProvider{}
	req := p.buildRequest(
		[]Message{{Role: "user", Content: "hello"}},
		[]ToolDefinition{{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name: "9 invalid tool",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"value": map[string]any{
							"type":     "string",
							"optional": true,
						},
					},
				},
			},
		}},
		"gemini-3-flash",
		nil,
		"session-test",
	)
	declaration := req.Tools[0].FunctionDeclarations[0]
	if declaration.Name != "_9_invalid_tool" {
		t.Fatalf("tool name = %q", declaration.Name)
	}
	properties := declaration.Parameters.(map[string]any)["properties"].(map[string]any)
	value := properties["value"].(map[string]any)
	if _, found := value["optional"]; found {
		t.Fatalf("unsupported optional field was preserved: %#v", value)
	}
}

func TestBuildRequestUsesFunctionFieldsWhenToolCallNameMissing(t *testing.T) {
	p := &AntigravityProvider{}

	messages := []Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				ID: "call_read_file_123",
				Function: &FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"README.md"}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call_read_file_123",
			Content:    "ok",
		},
	}

	req := p.buildRequest(messages, nil, "", nil, "session-test")
	if len(req.Contents) != 2 {
		t.Fatalf("expected 2 contents, got %d", len(req.Contents))
	}

	modelPart := req.Contents[0].Parts[0]
	if modelPart.FunctionCall == nil {
		t.Fatal("expected functionCall in assistant message")
	}
	if modelPart.FunctionCall.Name != "read_file" {
		t.Fatalf("expected functionCall name read_file, got %q", modelPart.FunctionCall.Name)
	}
	if modelPart.FunctionCall.ID != "call_read_file_123" {
		t.Fatalf("expected functionCall id call_read_file_123, got %q", modelPart.FunctionCall.ID)
	}
	if got := modelPart.FunctionCall.Args["path"]; got != "README.md" {
		t.Fatalf("expected functionCall args[path] to be README.md, got %v", got)
	}

	toolPart := req.Contents[1].Parts[0]
	if toolPart.FunctionResponse == nil {
		t.Fatal("expected functionResponse in tool message")
	}
	if toolPart.FunctionResponse.Name != "read_file" {
		t.Fatalf("expected functionResponse name read_file, got %q", toolPart.FunctionResponse.Name)
	}
	if toolPart.FunctionResponse.ID != modelPart.FunctionCall.ID {
		t.Fatalf(
			"functionResponse id %q does not match functionCall id %q",
			toolPart.FunctionResponse.ID,
			modelPart.FunctionCall.ID,
		)
	}
}

func TestBuildRequestGeneratesClaudeCompatibleToolCallIDs(t *testing.T) {
	p := &AntigravityProvider{}
	req := p.buildRequest([]Message{
		{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				Name:      "mcp.read:file",
				Arguments: map[string]any{"path": "README.md"},
			}},
		},
	}, nil, "claude-sonnet-4-6", nil, "session-test")

	got := req.Contents[0].Parts[0].FunctionCall.ID
	if matched, _ := regexp.MatchString(`^[a-zA-Z0-9_-]+$`, got); !matched {
		t.Fatalf("generated tool call id is not Claude-compatible: %q", got)
	}
	if got == "" {
		t.Fatal("generated tool call id is empty")
	}
}

func TestParseSSEResponse_SplitsThoughtAndVisibleContent(t *testing.T) {
	p := &AntigravityProvider{}
	body := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hidden reasoning\",\"thought\":true},{\"text\":\"visible answer\"}],\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":17,\"totalTokenCount\":216}}}\n" +
		"data: [DONE]\n"

	resp, err := p.parseSSEResponse(body)
	if err != nil {
		t.Fatalf("parseSSEResponse() error = %v", err)
	}

	if resp.Content != "visible answer" {
		t.Fatalf("Content = %q, want %q", resp.Content, "visible answer")
	}
	if resp.ReasoningContent != "hidden reasoning" {
		t.Fatalf("ReasoningContent = %q, want %q", resp.ReasoningContent, "hidden reasoning")
	}
	if resp.FinishReason != "stop" {
		t.Fatalf("FinishReason = %q, want %q", resp.FinishReason, "stop")
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 216 {
		t.Fatalf("Usage.TotalTokens = %v, want %d", resp.Usage, 216)
	}
}

func TestBuildRequest_SanitizesComplexToolSchemas(t *testing.T) {
	p := &AntigravityProvider{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"parent": map[string]any{
				"anyOf": []any{
					map[string]any{"$ref": "#/$defs/pageParent"},
					map[string]any{"$ref": "#/$defs/databaseParent"},
				},
			},
			"icon": map[string]any{
				"anyOf": []any{
					map[string]any{"type": "null"},
					map[string]any{"$ref": "#/$defs/emoji"},
				},
			},
		},
		"$defs": map[string]any{
			"pageParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"page_id": map[string]any{"type": "string"},
				},
				"required": []any{"page_id"},
			},
			"databaseParent": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"database_id": map[string]any{"type": "string"},
				},
				"required": []any{"database_id"},
			},
			"emoji": map[string]any{
				"type":    "string",
				"pattern": "^:[a-z_]+:$",
			},
		},
	}

	req := p.buildRequest(
		[]Message{{Role: "user", Content: "hello"}},
		[]ToolDefinition{{
			Type: "function",
			Function: ToolFunctionDefinition{
				Name:        "mcp_notion_create",
				Description: "Create a Notion object",
				Parameters:  schema,
			},
		}},
		"gemini-3-flash",
		nil,
		"session-test",
	)

	if len(req.Tools) != 1 || len(req.Tools[0].FunctionDeclarations) == 0 {
		t.Fatalf("request tools = %#v, want function declarations", req.Tools)
	}

	var parameters any
	for _, declaration := range req.Tools[0].FunctionDeclarations {
		if declaration.Name == "mcp_notion_create" {
			parameters = declaration.Parameters
			break
		}
	}
	got, ok := parameters.(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want map", parameters)
	}
	if got["$defs"] != nil {
		t.Fatalf("parameters = %#v, want unsupported $defs removed", got)
	}
	if got["properties"] == nil {
		t.Fatalf("parameters = %#v, want sanitized properties", got)
	}
}
