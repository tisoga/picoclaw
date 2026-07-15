package oauthprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers/common"
)

const (
	antigravityBaseURL      = "https://cloudcode-pa.googleapis.com"
	antigravityDefaultModel = "gemini-3-flash"
	antigravityUserAgent    = "antigravity"
	antigravityXGoogClient  = "google-cloud-sdk vscode_cloudshelleditor/0.1"
	antigravityVersion      = "1.15.8"
	antigravityIDEVersion   = "2.1.1"
)

// AntigravityProvider implements LLMProvider using Google's Cloud Code Assist (Antigravity) API.
// This provider authenticates via Google OAuth and provides access to models like Claude and Gemini
// through Google's infrastructure.
type AntigravityProvider struct {
	tokenSource func() (string, string, error) // Returns (accessToken, projectID, error)
	httpClient  *http.Client
	baseURL     string
}

// NewAntigravityProvider creates a new Antigravity provider using stored auth credentials.
func NewAntigravityProvider() *AntigravityProvider {
	return &AntigravityProvider{
		tokenSource: createAntigravityTokenSource(),
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		baseURL: "https://daily-cloudcode-pa.googleapis.com",
	}
}

// AntigravityGeneratedImage is an inline image returned by Cloud Code Assist.
type AntigravityGeneratedImage struct {
	Data     []byte
	MIMEType string
}

// GenerateImage calls Antigravity's native non-streaming image generation path.
func (p *AntigravityProvider) GenerateImage(
	ctx context.Context,
	prompt string,
	model string,
	aspectRatio string,
) ([]AntigravityGeneratedImage, error) {
	accessToken, projectID, err := p.tokenSource()
	if err != nil {
		return nil, fmt.Errorf("antigravity auth: %w", err)
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("antigravity image generation requires a prompt")
	}
	model = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(model), "google-antigravity/"), "antigravity/")
	if !strings.Contains(strings.ToLower(model), "image") {
		model = "gemini-3.1-flash-image"
	}
	model = trimAntigravityImageSizeSuffix(model)
	if aspectRatio == "" {
		aspectRatio = "1:1"
	}
	sessionID := "session-" + uuid.NewString()
	conversationID := uuid.NewString()
	trajectoryID := uuid.NewString()
	envelope := map[string]any{
		"project":     projectID,
		"model":       model,
		"userAgent":   antigravityUserAgent,
		"requestType": "image_gen",
		"requestId":   fmt.Sprintf("agent/%s/%d/%s/1", conversationID, time.Now().UnixMilli(), trajectoryID),
		"request": map[string]any{
			"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": prompt}}}},
			"generationConfig": map[string]any{
				"temperature": 1.0, "topP": 0.95, "topK": 40, "maxOutputTokens": 8192,
				"imageConfig": map[string]string{"aspectRatio": aspectRatio},
			},
			"sessionId": sessionID,
		},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal antigravity image request: %w", err)
	}
	baseURL := strings.TrimRight(p.baseURL, "/")
	if baseURL == "" {
		baseURL = "https://daily-cloudcode-pa.googleapis.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1internal:generateContent", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create antigravity image request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", antigravityXGoogClient)
	req.Header.Set("X-Machine-Session-Id", sessionID)
	client := p.httpClient
	if client == nil {
		client = &http.Client{Timeout: 180 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity image API call: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read antigravity image response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, p.parseAntigravityError(resp.StatusCode, responseBody)
	}
	return parseAntigravityImageResponse(responseBody)
}

func trimAntigravityImageSizeSuffix(model string) string {
	separator := strings.LastIndex(model, "-")
	if separator < 0 {
		return model
	}
	var width, height int
	if n, err := fmt.Sscanf(model[separator+1:], "%dx%d", &width, &height); err == nil && n == 2 {
		return model[:separator]
	}
	return model
}

func parseAntigravityImageResponse(body []byte) ([]AntigravityGeneratedImage, error) {
	type inlineData struct {
		Data          string `json:"data"`
		MIMEType      string `json:"mimeType"`
		MIMETypeSnake string `json:"mime_type"`
	}
	type responseBody struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData      *inlineData `json:"inlineData"`
					InlineDataSnake *inlineData `json:"inline_data"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	var payload struct {
		Response *responseBody `json:"response"`
		responseBody
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode antigravity image response: %w", err)
	}
	parsed := payload.responseBody
	if payload.Response != nil {
		parsed = *payload.Response
	}
	var images []AntigravityGeneratedImage
	for _, candidate := range parsed.Candidates {
		for _, part := range candidate.Content.Parts {
			inline := part.InlineData
			if inline == nil {
				inline = part.InlineDataSnake
			}
			if inline == nil || inline.Data == "" {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(inline.Data)
			if err != nil {
				return nil, fmt.Errorf("decode antigravity image data: %w", err)
			}
			mimeType := inline.MIMEType
			if mimeType == "" {
				mimeType = inline.MIMETypeSnake
			}
			if mimeType == "" {
				mimeType = "image/png"
			}
			images = append(images, AntigravityGeneratedImage{Data: decoded, MIMEType: mimeType})
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("antigravity image response contained no image data")
	}
	return images, nil
}

// Chat implements LLMProvider.Chat using the Cloud Code Assist v1internal API.
// The v1internal endpoint wraps the standard Gemini request in an envelope with
// project, model, request, requestType, userAgent, and requestId fields.
func (p *AntigravityProvider) Chat(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
) (*LLMResponse, error) {
	accessToken, projectID, err := p.tokenSource()
	if err != nil {
		return nil, fmt.Errorf("antigravity auth: %w", err)
	}

	if model == "" || model == "antigravity" || model == "google-antigravity" {
		model = antigravityDefaultModel
	}
	// Strip provider prefixes if present
	model = strings.TrimPrefix(model, "google-antigravity/")
	model = strings.TrimPrefix(model, "antigravity/")

	logger.DebugCF("provider.antigravity", "Starting chat", map[string]any{
		"model":     model,
		"project":   projectID,
		"requestId": fmt.Sprintf("agent-%d-%s", time.Now().UnixMilli(), randomString(9)),
	})

	// Wrap in v1internal envelope (matches pi-ai SDK format)
	sessionID := fmt.Sprintf("session-%d-%s", time.Now().UnixMilli(), randomString(9))

	// Build the inner Gemini-format request
	innerRequest := p.buildRequest(messages, tools, model, options, sessionID)

	envelope := map[string]any{
		"project":     projectID,
		"model":       model,
		"request":     innerRequest,
		"requestType": "agent",
		"userAgent":   antigravityUserAgent,
		"requestId":   fmt.Sprintf("agent-%d-%s", time.Now().UnixMilli(), randomString(9)),
	}

	bodyBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	// Build API URL — uses Cloud Code Assist v1internal streaming endpoint
	// 9router: use daily-cloudcode-pa for generation to avoid 429 rate limits
	apiURL := "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Headers matching the pi-ai SDK antigravity format
	clientMetadata, _ := json.Marshal(map[string]any{
		"ideType":    9, // GENERIC_IDE
		"platform":   2, // LINUX
		"pluginType": 2, // GEMINI_CLOUD_CODE
	})
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", antigravityXGoogClient)
	req.Header.Set("Client-Metadata", string(clientMetadata))
	req.Header.Set("X-Machine-Session-Id", sessionID)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity API call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.ErrorCF("provider.antigravity", "API call failed", map[string]any{
			"status_code": resp.StatusCode,
			"response":    string(respBody),
			"model":       model,
		})

		return nil, p.parseAntigravityError(resp.StatusCode, respBody)
	}

	// Response is always SSE from streamGenerateContent — each line is "data: {...}"
	// with a "response" wrapper containing the standard Gemini response
	llmResp, err := p.parseSSEResponse(string(respBody))
	if err != nil {
		return nil, err
	}

	// Check for empty response (some models might return valid success but empty text)
	if llmResp.Content == "" && len(llmResp.ToolCalls) == 0 {
		return nil, fmt.Errorf(
			"antigravity: model returned an empty response (this model might be invalid or restricted)",
		)
	}

	return llmResp, nil
}

// GetDefaultModel returns the default model identifier.
func (p *AntigravityProvider) GetDefaultModel() string {
	return antigravityDefaultModel
}

// --- Request building ---

type antigravityRequest struct {
	SessionID    string                   `json:"sessionId,omitempty"`
	Contents     []antigravityContent     `json:"contents"`
	Tools        []antigravityTool        `json:"tools,omitempty"`
	SystemPrompt *antigravitySystemPrompt `json:"systemInstruction,omitempty"`
	Config       *antigravityGenConfig    `json:"generationConfig,omitempty"`
	ToolConfig   *antigravityToolConfig   `json:"toolConfig,omitempty"`
}

type antigravityContent struct {
	Role  string            `json:"role"`
	Parts []antigravityPart `json:"parts"`
}

type antigravityPart struct {
	Text                  string                       `json:"text,omitempty"`
	ThoughtSignature      string                       `json:"thoughtSignature,omitempty"`
	ThoughtSignatureSnake string                       `json:"thought_signature,omitempty"`
	FunctionCall          *antigravityFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse      *antigravityFunctionResponse `json:"functionResponse,omitempty"`
}

type antigravityFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type antigravityFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type antigravityTool struct {
	FunctionDeclarations []antigravityFuncDecl `json:"functionDeclarations"`
}

type antigravityFuncDecl struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type antigravitySystemPrompt struct {
	Parts []antigravityPart `json:"parts"`
}

type antigravityGenConfig struct {
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
}

type antigravityToolConfig struct {
	FunctionCallingConfig struct {
		Mode string `json:"mode"`
	} `json:"functionCallingConfig"`
}

func (p *AntigravityProvider) buildRequest(
	messages []Message,
	tools []ToolDefinition,
	model string,
	options map[string]any,
	sessionID string,
) antigravityRequest {
	req := antigravityRequest{
		SessionID: sessionID,
	}
	toolCallNames := make(map[string]string)

	// Build contents from messages
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			req.SystemPrompt = &antigravitySystemPrompt{
				Parts: []antigravityPart{{Text: msg.Content}},
			}
		case "user":
			if msg.ToolCallID != "" {
				toolName := common.ResolveToolResponseName(msg.ToolCallID, toolCallNames)

				// Apply same cloak logic to responses
				isNative := false
				for _, nativeName := range []string{
					"browser_subagent", "command_status", "find_by_name", "generate_image",
					"grep_search", "list_dir", "list_resources", "multi_replace_file_content",
					"notify_user", "read_resource", "read_terminal", "read_url_content",
					"replace_file_content", "run_command", "search_web", "send_command_input",
					"task_boundary", "view_content_chunk", "view_file", "write_to_file",
				} {
					if toolName == nativeName {
						isNative = true
						break
					}
				}

				if !isNative && !strings.HasSuffix(toolName, "_ide") {
					toolName += "_ide"
				}
				// Tool result
				req.Contents = append(req.Contents, antigravityContent{
					Role: "user",
					Parts: []antigravityPart{{
						FunctionResponse: &antigravityFunctionResponse{
							Name: toolName,
							Response: map[string]any{
								"result": msg.Content,
							},
						},
					}},
				})
			} else {
				req.Contents = append(req.Contents, antigravityContent{
					Role:  "user",
					Parts: []antigravityPart{{Text: msg.Content}},
				})
			}
		case "assistant":
			content := antigravityContent{
				Role: "model",
			}
			if msg.Content != "" {
				content.Parts = append(content.Parts, antigravityPart{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				toolName, toolArgs, thoughtSignature := common.NormalizeStoredToolCall(tc)
				if toolName == "" {
					logger.WarnCF(
						"provider.antigravity",
						"Skipping tool call with empty name in history",
						map[string]any{
							"tool_call_id": tc.ID,
						},
					)
					continue
				}

				// 9router: Gemini 3 crashes if a tool call has no thoughtSignature. Backfill.
				if thoughtSignature == "" {
					thoughtSignature = antigravityDefaultThoughtSignature
				}

				// 9router: Append _ide to tool names to match decoy pattern
				isNative := false
				for _, nativeName := range []string{
					"browser_subagent", "command_status", "find_by_name", "generate_image",
					"grep_search", "list_dir", "list_resources", "multi_replace_file_content",
					"notify_user", "read_resource", "read_terminal", "read_url_content",
					"replace_file_content", "run_command", "search_web", "send_command_input",
					"task_boundary", "view_content_chunk", "view_file", "write_to_file",
				} {
					if toolName == nativeName {
						isNative = true
						break
					}
				}

				if !isNative {
					toolName = toolName + "_ide"
				}

				if tc.ID != "" {
					toolCallNames[tc.ID] = toolName
				}
				content.Parts = append(content.Parts, antigravityPart{
					ThoughtSignature:      thoughtSignature,
					ThoughtSignatureSnake: thoughtSignature,
					FunctionCall: &antigravityFunctionCall{
						Name: toolName,
						Args: toolArgs,
					},
				})
			}
			if len(content.Parts) > 0 {
				req.Contents = append(req.Contents, content)
			}
		case "tool":
			toolName := common.ResolveToolResponseName(msg.ToolCallID, toolCallNames)
			if !strings.HasSuffix(toolName, "_ide") {
				toolName += "_ide"
			}
			req.Contents = append(req.Contents, antigravityContent{
				Role: "user",
				Parts: []antigravityPart{{
					FunctionResponse: &antigravityFunctionResponse{
						Name: toolName,
						Response: map[string]any{
							"result": msg.Content,
						},
					},
				}},
			})
		}
	}

	// Build tools
	if len(tools) > 0 {
		var funcDecls []antigravityFuncDecl
		existingTools := make(map[string]bool)
		for _, t := range tools {
			if t.Type != "function" {
				continue
			}

			// Don't append _ide to AG defaults so they don't clash with the decoys
			toolName := t.Function.Name

			// If not a native tool, cloak it
			isNative := false
			for _, nativeName := range []string{
				"browser_subagent", "command_status", "find_by_name", "generate_image",
				"grep_search", "list_dir", "list_resources", "multi_replace_file_content",
				"notify_user", "read_resource", "read_terminal", "read_url_content",
				"replace_file_content", "run_command", "search_web", "send_command_input",
				"task_boundary", "view_content_chunk", "view_file", "write_to_file",
			} {
				if toolName == nativeName {
					isNative = true
					break
				}
			}

			if !isNative {
				toolName += "_ide"
			}

			existingTools[toolName] = true
			funcDecls = append(funcDecls, antigravityFuncDecl{
				Name:        toolName,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}

		// 9router: Inject 21 decoy tools to look like a native IDE
		decoyNames := []string{
			"browser_subagent", "command_status", "find_by_name", "generate_image",
			"grep_search", "list_dir", "list_resources", "multi_replace_file_content",
			"notify_user", "read_resource", "read_terminal", "read_url_content",
			"replace_file_content", "run_command", "search_web", "send_command_input",
			"task_boundary", "view_content_chunk", "view_file", "write_to_file",
		}
		for _, name := range decoyNames {
			if !existingTools[name] {
				funcDecls = append(funcDecls, antigravityFuncDecl{
					Name:        name,
					Description: "This tool is currently unavailable.",
					Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
				})
			}
		}

		if len(funcDecls) > 0 {
			req.Tools = []antigravityTool{{FunctionDeclarations: funcDecls}}
			req.ToolConfig = &antigravityToolConfig{}
			req.ToolConfig.FunctionCallingConfig.Mode = "VALIDATED"
		}
	}

	// Generation config
	config := &antigravityGenConfig{}
	if val, ok := options["max_tokens"]; ok {
		if maxTokens, ok := val.(int); ok && maxTokens > 0 {
			config.MaxOutputTokens = maxTokens
		} else if maxTokens, ok := val.(float64); ok && maxTokens > 0 {
			config.MaxOutputTokens = int(maxTokens)
		}
	}
	if temp, ok := options["temperature"].(float64); ok {
		config.Temperature = temp
	}
	if config.MaxOutputTokens > 0 || config.Temperature > 0 {
		req.Config = config
	}

	return req
}

// --- Response parsing ---

type antigravityJSONResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text                  string                   `json:"text,omitempty"`
				Thought               bool                     `json:"thought,omitempty"`
				ThoughtSignature      string                   `json:"thoughtSignature,omitempty"`
				ThoughtSignatureSnake string                   `json:"thought_signature,omitempty"`
				FunctionCall          *antigravityFunctionCall `json:"functionCall,omitempty"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (p *AntigravityProvider) parseSSEResponse(body string) (*LLMResponse, error) {
	var contentParts []string
	var reasoningParts []string
	var toolCalls []ToolCall
	var usage *UsageInfo
	var finishReason string

	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		// v1internal SSE wraps the Gemini response in a "response" field
		var sseChunk struct {
			Response antigravityJSONResponse `json:"response"`
		}
		if err := json.Unmarshal([]byte(data), &sseChunk); err != nil {
			continue
		}
		resp := sseChunk.Response

		for _, candidate := range resp.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					if part.Thought {
						reasoningParts = append(reasoningParts, part.Text)
					} else {
						contentParts = append(contentParts, part.Text)
					}
				}
				if part.FunctionCall != nil {
					name := strings.TrimSuffix(part.FunctionCall.Name, "_ide")
					argumentsJSON, _ := json.Marshal(part.FunctionCall.Args)
					toolCalls = append(toolCalls, ToolCall{
						ID:        fmt.Sprintf("call_%s_%d", name, time.Now().UnixNano()),
						Name:      name,
						Arguments: part.FunctionCall.Args,
						Function: &FunctionCall{
							Name:      name,
							Arguments: string(argumentsJSON),
							ThoughtSignature: extractPartThoughtSignature(
								part.ThoughtSignature,
								part.ThoughtSignatureSnake,
							),
						},
					})
				}
			}
			if candidate.FinishReason != "" {
				finishReason = candidate.FinishReason
			}
		}

		if resp.UsageMetadata.TotalTokenCount > 0 {
			usage = &UsageInfo{
				PromptTokens:     resp.UsageMetadata.PromptTokenCount,
				CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      resp.UsageMetadata.TotalTokenCount,
			}
		}
	}

	mappedFinish := "stop"
	if len(toolCalls) > 0 {
		mappedFinish = "tool_calls"
	}
	if finishReason == "MAX_TOKENS" {
		mappedFinish = "length"
	}

	return &LLMResponse{
		Content:          strings.Join(contentParts, ""),
		ReasoningContent: strings.Join(reasoningParts, ""),
		ToolCalls:        toolCalls,
		FinishReason:     mappedFinish,
		Usage:            usage,
	}, nil
}

func extractPartThoughtSignature(thoughtSignature string, thoughtSignatureSnake string) string {
	if thoughtSignature != "" {
		return thoughtSignature
	}
	if thoughtSignatureSnake != "" {
		return thoughtSignatureSnake
	}
	return ""
}

// --- Token source ---

func createAntigravityTokenSource() func() (string, string, error) {
	return func() (string, string, error) {
		cred, err := auth.GetCredential("google-antigravity")
		if err != nil {
			return "", "", fmt.Errorf("loading auth credentials: %w", err)
		}
		if cred == nil {
			return "", "", fmt.Errorf(
				"no credentials for google-antigravity. Run: picoclaw auth login --provider google-antigravity",
			)
		}

		// Refresh if needed
		if cred.NeedsRefresh() && cred.RefreshToken != "" {
			oauthCfg := auth.GoogleAntigravityOAuthConfig()
			refreshed, err := auth.RefreshAccessToken(cred, oauthCfg)
			if err != nil {
				return "", "", fmt.Errorf("refreshing token: %w", err)
			}
			refreshed.Email = cred.Email
			if refreshed.ProjectID == "" {
				refreshed.ProjectID = cred.ProjectID
			}
			if err := auth.SetCredential("google-antigravity", refreshed); err != nil {
				return "", "", fmt.Errorf("saving refreshed token: %w", err)
			}
			cred = refreshed
		}

		if cred.IsExpired() {
			return "", "", fmt.Errorf(
				"antigravity credentials expired. Run: picoclaw auth login --provider google-antigravity",
			)
		}

		projectID := cred.ProjectID
		if projectID == "" {
			// Fetch project ID and Onboard
			fetchedID, err := FetchAntigravityProjectID(cred.AccessToken)
			if err != nil {
				return "", "", fmt.Errorf("failed to fetch project ID: %w. Your Google account may not be eligible for Gemini Code Assist", err)
			}

			// 9router: Explicitly onboard the user. If we don't do this, chat requests will fail
			// with "project not onboarded" errors.
			if err := OnboardAntigravityUser(cred.AccessToken, fetchedID); err != nil {
				logger.WarnCF("provider.antigravity", "Onboarding returned an error, but continuing anyway", map[string]any{
					"error": err.Error(),
				})
			}

			projectID = fetchedID
			cred.ProjectID = projectID
			_ = auth.SetCredential("google-antigravity", cred)
		}

		return cred.AccessToken, projectID, nil
	}
}

// FetchAntigravityProjectID retrieves the Google Cloud project ID from the loadCodeAssist endpoint.
func FetchAntigravityProjectID(accessToken string) (string, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"ideType":    "IDE_UNSPECIFIED",
			"platform":   "PLATFORM_UNSPECIFIED",
			"pluginType": "GEMINI",
		},
	})

	req, err := http.NewRequest("POST", antigravityBaseURL+"/v1internal:loadCodeAssist", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityUserAgent)
	req.Header.Set("X-Goog-Api-Client", antigravityXGoogClient)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading loadCodeAssist response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("loadCodeAssist failed: %s", string(body))
	}

	var result struct {
		CloudAICompanionProject string `json:"cloudaicompanionProject"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	if result.CloudAICompanionProject == "" {
		return "", fmt.Errorf("no project ID in loadCodeAssist response")
	}

	return result.CloudAICompanionProject, nil
}

// AntigravityQuotaSnapshot is a point-in-time view of the account quota data
// returned by the Cloud Code Assist APIs.
type AntigravityQuotaSnapshot struct {
	Plan      string                 `json:"plan"`
	ProjectID string                 `json:"project_id"`
	Models    []AntigravityModelInfo `json:"models"`
}

// AntigravityAPIError preserves the upstream status code so callers can
// distinguish an expired credential from a quota service failure.
type AntigravityAPIError struct {
	StatusCode int
	Operation  string
	Body       string
}

func (e *AntigravityAPIError) Error() string {
	return fmt.Sprintf("%s failed (HTTP %d): %s", e.Operation, e.StatusCode, e.Body)
}

var antigravityQuotaModelOrder = []string{
	"gemini-3-flash-agent",
	"gemini-3.5-flash-low",
	"gemini-3.5-flash-extra-low",
	"gemini-pro-agent",
	"gemini-3.1-pro-low",
	"claude-sonnet-4-6",
	"claude-opus-4-6-thinking",
	"gpt-oss-120b-medium",
	"gemini-3-flash",
	"gemini-3.1-flash-image",
	"gemini-3-pro-image",
}

// FetchAntigravityQuota fetches the subscription tier and the real quota
// buckets returned for the recommended Antigravity models.
func FetchAntigravityQuota(accessToken, projectID string) (*AntigravityQuotaSnapshot, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	return fetchAntigravityQuota(client, antigravityBaseURL, accessToken, projectID)
}

// FetchAntigravityModels remains available for model catalog callers.
func FetchAntigravityModels(accessToken, projectID string) ([]AntigravityModelInfo, error) {
	snapshot, err := FetchAntigravityQuota(accessToken, projectID)
	if err != nil {
		return nil, err
	}
	return snapshot.Models, nil
}

func fetchAntigravityQuota(
	client *http.Client,
	baseURL, accessToken, projectID string,
) (*AntigravityQuotaSnapshot, error) {
	snapshot := &AntigravityQuotaSnapshot{Plan: "Unknown", ProjectID: projectID}

	subscription, err := fetchAntigravitySubscription(client, baseURL, accessToken)
	if err == nil {
		if subscription.ProjectID != "" {
			snapshot.ProjectID = subscription.ProjectID
		}
		if subscription.Plan != "" {
			snapshot.Plan = subscription.Plan
		}
	} else if snapshot.ProjectID == "" {
		return nil, err
	}

	models, err := fetchAntigravityQuotaModels(client, baseURL, accessToken, snapshot.ProjectID)
	if err != nil {
		return nil, err
	}
	snapshot.Models = models
	return snapshot, nil
}

type antigravitySubscription struct {
	ProjectID string
	Plan      string
}

func fetchAntigravitySubscription(
	client *http.Client,
	baseURL, accessToken string,
) (antigravitySubscription, error) {
	body, err := json.Marshal(map[string]any{
		"metadata": antigravityClientMetadata(),
		"mode":     1,
	})
	if err != nil {
		return antigravitySubscription{}, err
	}

	responseBody, err := doAntigravityQuotaRequest(
		client,
		baseURL+"/v1internal:loadCodeAssist",
		accessToken,
		body,
		"loadCodeAssist",
	)
	if err != nil {
		return antigravitySubscription{}, err
	}

	var result struct {
		ProjectID   string `json:"cloudaicompanionProject"`
		CurrentTier struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"currentTier"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return antigravitySubscription{}, fmt.Errorf("parsing loadCodeAssist response: %w", err)
	}
	plan := strings.TrimSpace(result.CurrentTier.Name)
	if plan == "" {
		plan = strings.TrimSpace(result.CurrentTier.ID)
	}
	return antigravitySubscription{ProjectID: result.ProjectID, Plan: plan}, nil
}

func fetchAntigravityQuotaModels(
	client *http.Client,
	baseURL, accessToken, projectID string,
) ([]AntigravityModelInfo, error) {
	reqBody, err := json.Marshal(map[string]any{"project": projectID})
	if err != nil {
		return nil, err
	}
	responseBody, err := doAntigravityQuotaRequest(
		client,
		baseURL+"/v1internal:fetchAvailableModels",
		accessToken,
		reqBody,
		"fetchAvailableModels",
	)
	if err != nil {
		return nil, err
	}

	type quotaInfo struct {
		RemainingFraction json.RawMessage `json:"remainingFraction"`
		ResetTime         string          `json:"resetTime"`
		IsExhausted       bool            `json:"isExhausted"`
	}
	var result struct {
		Models map[string]struct {
			DisplayName string     `json:"displayName"`
			IsInternal  bool       `json:"isInternal"`
			QuotaInfo   *quotaInfo `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("parsing fetchAvailableModels response: %w", err)
	}

	order := make(map[string]int, len(antigravityQuotaModelOrder))
	for i, id := range antigravityQuotaModelOrder {
		order[id] = i
	}

	models := make([]AntigravityModelInfo, 0, len(result.Models))
	for id, info := range result.Models {
		position, recommended := order[id]
		if !recommended || info.IsInternal || info.QuotaInfo == nil {
			continue
		}
		fraction, err := parseRemainingFraction(info.QuotaInfo.RemainingFraction)
		if err != nil {
			logger.WarnCF("provider.antigravity", "Skipping model with invalid quota", map[string]any{
				"model": id,
				"error": err.Error(),
			})
			continue
		}
		displayName := strings.TrimSpace(info.DisplayName)
		if displayName == "" {
			displayName = id
		}
		models = append(models, AntigravityModelInfo{
			ID:                id,
			DisplayName:       displayName,
			IsExhausted:       info.QuotaInfo.IsExhausted || fraction <= 0,
			RemainingFraction: fraction,
			ResetTime:         info.QuotaInfo.ResetTime,
			order:             position,
		})
	}

	sort.SliceStable(models, func(i, j int) bool {
		return models[i].order < models[j].order
	})
	return models, nil
}

func doAntigravityQuotaRequest(
	client *http.Client,
	url, accessToken string,
	body []byte,
	operation string,
) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", antigravityIDEUserAgent())
	req.Header.Set("X-Client-Name", "antigravity")
	req.Header.Set("X-Client-Version", antigravityIDEVersion)
	req.Header.Set("X-Goog-Api-Client", antigravityXGoogClient)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", operation, err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("reading %s response: %w", operation, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &AntigravityAPIError{
			StatusCode: resp.StatusCode,
			Operation:  operation,
			Body:       truncateString(string(responseBody), 300),
		}
	}
	return responseBody, nil
}

func parseRemainingFraction(raw json.RawMessage) (float64, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return 0, fmt.Errorf("remainingFraction is missing")
	}
	if strings.HasPrefix(value, "\"") {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, err
		}
		value = text
	}
	fraction, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	return fraction, nil
}

func antigravityClientMetadata() map[string]any {
	platform := 0
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64":
		platform = 1
	case "darwin/arm64":
		platform = 2
	case "linux/amd64":
		platform = 3
	case "linux/arm64":
		platform = 4
	case "windows/amd64":
		platform = 5
	}
	return map[string]any{"ideType": 9, "platform": platform, "pluginType": 2}
}

func antigravityIDEUserAgent() string {
	return fmt.Sprintf("antigravity/ide/%s %s/%s", antigravityIDEVersion, runtime.GOOS, runtime.GOARCH)
}

type AntigravityModelInfo struct {
	ID                string  `json:"id"`
	DisplayName       string  `json:"display_name"`
	IsExhausted       bool    `json:"is_exhausted"`
	RemainingFraction float64 `json:"remaining_fraction"`
	ResetTime         string  `json:"reset_time"`
	order             int
}

// --- Helpers ---

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func randomString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func (p *AntigravityProvider) parseAntigravityError(statusCode int, body []byte) error {
	var errResp struct {
		Error struct {
			Code    int              `json:"code"`
			Message string           `json:"message"`
			Status  string           `json:"status"`
			Details []map[string]any `json:"details"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &errResp); err != nil {
		return fmt.Errorf("antigravity API error (HTTP %d): %s", statusCode, truncateString(string(body), 500))
	}

	msg := errResp.Error.Message
	if statusCode == 429 {
		// Try to extract quota reset info
		for _, detail := range errResp.Error.Details {
			if typeVal, ok := detail["@type"].(string); ok && strings.HasSuffix(typeVal, "ErrorInfo") {
				if metadata, ok := detail["metadata"].(map[string]any); ok {
					if delay, ok := metadata["quotaResetDelay"].(string); ok {
						return fmt.Errorf("antigravity rate limit exceeded: %s (reset in %s)", msg, delay)
					}
				}
			}
		}
		return fmt.Errorf("antigravity rate limit exceeded: %s", msg)
	}

	return fmt.Errorf("antigravity API error (%s): %s", errResp.Error.Status, msg)
}

// OnboardAntigravityUser hits the v1internal:onboardUser endpoint to provision the account.
func OnboardAntigravityUser(accessToken, projectID string) error {
	reqBody, _ := json.Marshal(map[string]any{
		"project": projectID,
	})

	req, err := http.NewRequest("POST", antigravityBaseURL+"/v1internal:onboardUser", bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", antigravityXGoogClient)

	client := &http.Client{Timeout: 15 * time.Second}

	// 9router polls this endpoint up to 10 times to ensure provisioning finishes
	for i := 0; i < 5; i++ {
		resp, err := client.Do(req)
		if err != nil {
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return nil
		}

		if i == 4 {
			return fmt.Errorf("onboardUser failed after 5 retries: HTTP %d: %s", resp.StatusCode, string(body))
		}
		time.Sleep(2 * time.Second)
	}
	return nil
}
