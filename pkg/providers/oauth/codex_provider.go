package oauthprovider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/logger"
	orc "github.com/sipeed/picoclaw/pkg/providers/openai_responses_common"
)

const (
	codexDefaultModel        = "gpt-5.3-codex"
	codexDefaultInstructions = "You are Codex, a coding assistant."
)

type CodexProvider struct {
	client          *openai.Client
	token           string
	accountID       string
	tokenSource     func() (string, string, error)
	enableWebSearch bool
}

const defaultCodexInstructions = "You are Codex, a coding assistant."

func NewCodexProvider(token, accountID string) *CodexProvider {
	opts := []option.RequestOption{
		option.WithBaseURL("https://chatgpt.com/backend-api/codex"),
		option.WithAPIKey(token),
		option.WithHeader("originator", "codex_cli_rs"),
		option.WithHeader("OpenAI-Beta", "responses=experimental"),
	}
	if accountID != "" {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	}
	client := openai.NewClient(opts...)
	return &CodexProvider{
		client:          &client,
		token:           token,
		accountID:       accountID,
		enableWebSearch: true,
	}
}

// GenerateImage uses the Codex OAuth Responses API image_generation tool.
// This is the same subscription-backed route used by OpenClaw, and is
// intentionally separate from the API-key /images/generations endpoint.
func (p *CodexProvider) GenerateImage(ctx context.Context, prompt, model, _ string) ([]AntigravityGeneratedImage, error) {
	token := p.token
	accountID := p.accountID
	if p.tokenSource != nil {
		refreshed, account, err := p.tokenSource()
		if err != nil {
			return nil, fmt.Errorf("refreshing codex token: %w", err)
		}
		token, accountID = refreshed, account
	}
	if token == "" {
		return nil, fmt.Errorf("codex OAuth token is missing")
	}
	imageModel := strings.TrimSpace(model)
	if i := strings.LastIndex(imageModel, "/"); i >= 0 {
		imageModel = imageModel[i+1:]
	}
	if !strings.Contains(strings.ToLower(imageModel), "image") {
		imageModel = "gpt-image-2"
	}
	body := map[string]any{
		"model": "gpt-5.6-sol",
		"input": []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": prompt}},
		}},
		"instructions": "You are an image generation assistant.",
		"tools":        []any{map[string]any{"type": "image_generation", "model": imageModel}},
		"tool_choice":  map[string]any{"type": "image_generation"},
		"stream":       true,
		"store":        false,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal codex image request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	if accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	client := http.DefaultClient
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex image API call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("codex image API returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return parseCodexGeneratedImages(resp.Body)
}

func parseCodexGeneratedImages(reader io.Reader) ([]AntigravityGeneratedImage, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 64<<20))
	scanner.Buffer(make([]byte, 64*1024), 64<<20)
	var images []AntigravityGeneratedImage
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event struct {
			Type     string                        `json:"type"`
			Item     struct{ Type, Result string } `json:"item"`
			Response struct {
				Output []struct{ Type, Result string } `json:"output"`
			} `json:"response"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		if event.Type == "response.failed" || event.Type == "error" {
			if event.Error.Message == "" {
				event.Error.Message = "codex image generation failed"
			}
			return nil, errors.New(event.Error.Message)
		}
		if event.Type == "response.output_item.done" && event.Item.Type == "image_generation_call" {
			if image, err := decodeCodexImage(event.Item.Result); err != nil {
				return nil, err
			} else if image != nil {
				images = append(images, *image)
			}
		}
		if event.Type == "response.completed" {
			for _, output := range event.Response.Output {
				if output.Type == "image_generation_call" {
					if image, err := decodeCodexImage(output.Result); err != nil {
						return nil, err
					} else if image != nil {
						images = append(images, *image)
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("codex image response contained no image data")
	}
	return images, nil
}

func decodeCodexImage(encoded string) (*AntigravityGeneratedImage, error) {
	if encoded == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode codex image data: %w", err)
	}
	return &AntigravityGeneratedImage{Data: data, MIMEType: "image/png"}, nil
}

func NewCodexProviderWithTokenSource(
	token, accountID string, tokenSource func() (string, string, error),
) *CodexProvider {
	p := NewCodexProvider(token, accountID)
	p.tokenSource = tokenSource
	return p
}

func (p *CodexProvider) Chat(
	ctx context.Context, messages []Message, tools []ToolDefinition, model string, options map[string]any,
) (*LLMResponse, error) {
	var opts []option.RequestOption
	accountID := p.accountID
	resolvedModel, fallbackReason := resolveCodexModel(model)
	if fallbackReason != "" {
		logger.WarnCF(
			"provider.codex",
			"Requested model is not compatible with Codex backend, using fallback",
			map[string]any{
				"requested_model": model,
				"resolved_model":  resolvedModel,
				"reason":          fallbackReason,
			},
		)
	}
	if p.tokenSource != nil {
		tok, accID, err := p.tokenSource()
		if err != nil {
			return nil, fmt.Errorf("refreshing token: %w", err)
		}
		opts = append(opts, option.WithAPIKey(tok))
		if accID != "" {
			accountID = accID
		}
	}
	if accountID != "" {
		opts = append(opts, option.WithHeader("Chatgpt-Account-Id", accountID))
	} else {
		logger.WarnCF(
			"provider.codex",
			"No account id found for Codex request; backend may reject with 400",
			map[string]any{
				"requested_model": model,
				"resolved_model":  resolvedModel,
			},
		)
	}

	// Respect tools.web.prefer_native: only inject native search when the agent
	// loop passes options["native_search"]=true, so prefer_native=false means no injection.
	useNativeSearch := p.enableWebSearch && (options["native_search"] == true)
	params := buildCodexParams(messages, tools, resolvedModel, options, useNativeSearch)

	stream := p.client.Responses.NewStreaming(ctx, params, opts...)
	defer stream.Close()

	var resp *responses.Response
	var streamedText strings.Builder
	streamedOutputItems := make([]responses.ResponseOutputItemUnion, 0)
	for stream.Next() {
		evt := stream.Current()
		if evt.Type == "response.output_text.delta" {
			streamedText.WriteString(evt.Delta)
		}
		if evt.Type == "response.output_item.done" {
			itemEvt := evt.AsResponseOutputItemDone()
			if itemEvt.Item.Type != "" {
				streamedOutputItems = append(streamedOutputItems, itemEvt.Item)
			}
		}
		if evt.Type == "response.completed" || evt.Type == "response.failed" || evt.Type == "response.incomplete" {
			evtResp := evt.Response
			if evtResp.ID != "" {
				evtRespCopy := evtResp
				resp = &evtRespCopy
			}
		}
	}
	err := stream.Err()
	if err != nil {
		fields := map[string]any{
			"requested_model":    model,
			"resolved_model":     resolvedModel,
			"messages_count":     len(messages),
			"tools_count":        len(tools),
			"account_id_present": accountID != "",
			"error":              err.Error(),
		}
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			fields["status_code"] = apiErr.StatusCode
			fields["api_type"] = apiErr.Type
			fields["api_code"] = apiErr.Code
			fields["api_param"] = apiErr.Param
			fields["api_message"] = apiErr.Message
			if apiErr.StatusCode == 400 {
				fields["hint"] = "verify account id header and model compatibility for codex backend"
			}
			if apiErr.Response != nil {
				fields["request_id"] = apiErr.Response.Header.Get("x-request-id")
			}
		}
		logger.ErrorCF("provider.codex", "Codex API call failed", fields)
		return nil, fmt.Errorf("codex API call: %w", err)
	}
	if resp == nil {
		fields := map[string]any{
			"requested_model":    model,
			"resolved_model":     resolvedModel,
			"messages_count":     len(messages),
			"tools_count":        len(tools),
			"account_id_present": accountID != "",
		}
		logger.ErrorCF("provider.codex", "Codex stream ended without completed response event", fields)
		return nil, fmt.Errorf("codex API call: stream ended without completed response")
	}
	if len(resp.Output) == 0 && len(streamedOutputItems) > 0 {
		resp.Output = streamedOutputItems
	}

	parsed := orc.ParseResponseFromStruct(resp)
	if parsed.Content == "" && streamedText.Len() > 0 {
		parsed.Content = streamedText.String()
	}
	return parsed, nil
}

func (p *CodexProvider) GetDefaultModel() string {
	return codexDefaultModel
}

func (p *CodexProvider) SupportsNativeSearch() bool {
	return p.enableWebSearch
}

func resolveCodexModel(model string) (string, string) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return codexDefaultModel, "empty model"
	}

	if after, ok := strings.CutPrefix(m, "openai/"); ok {
		m = after
	} else if strings.Contains(m, "/") {
		return codexDefaultModel, "non-openai model namespace"
	}

	unsupportedPrefixes := []string{
		"glm",
		"claude",
		"anthropic",
		"gemini",
		"google",
		"moonshot",
		"kimi",
		"qwen",
		"deepseek",
		"llama",
		"meta-llama",
		"mistral",
		"grok",
		"xai",
		"zhipu",
	}
	for _, prefix := range unsupportedPrefixes {
		if strings.HasPrefix(m, prefix) {
			return codexDefaultModel, "unsupported model prefix"
		}
	}

	if strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") {
		return m, ""
	}

	return codexDefaultModel, "unsupported model family"
}

func buildCodexParams(
	messages []Message, tools []ToolDefinition, model string, options map[string]any, enableWebSearch bool,
) responses.ResponseNewParams {
	inputItems, instructions := orc.TranslateMessages(messages)

	params := responses.ResponseNewParams{
		Model: model,
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems,
		},
		Store: openai.Opt(false),
	}

	if instructions != "" {
		params.Instructions = openai.Opt(instructions)
	} else {
		// ChatGPT Codex backend requires instructions to be present.
		params.Instructions = openai.Opt(defaultCodexInstructions)
	}

	// Prompt caching: pass a stable cache key so OpenAI can bucket requests
	// and reuse prefix KV cache across calls with the same key.
	// See: https://platform.openai.com/docs/guides/prompt-caching
	if cacheKey, ok := options["prompt_cache_key"].(string); ok && cacheKey != "" {
		params.PromptCacheKey = openai.Opt(cacheKey)
	}

	if len(tools) > 0 || enableWebSearch {
		params.Tools = orc.TranslateTools(tools, enableWebSearch)
	}

	return params
}

func CreateCodexTokenSource() func() (string, string, error) {
	return func() (string, string, error) {
		cred, err := auth.GetCredential("openai")
		if err != nil {
			return "", "", fmt.Errorf("loading auth credentials: %w", err)
		}
		if cred == nil {
			return "", "", fmt.Errorf("no credentials for openai. Run: picoclaw auth login --provider openai")
		}

		if cred.AuthMethod == "oauth" && cred.NeedsRefresh() && cred.RefreshToken != "" {
			oauthCfg := auth.OpenAIOAuthConfig()
			refreshed, err := auth.RefreshAccessToken(cred, oauthCfg)
			if err != nil {
				return "", "", fmt.Errorf("refreshing token: %w", err)
			}
			if refreshed.AccountID == "" {
				refreshed.AccountID = cred.AccountID
			}
			if err := auth.SetCredential("openai", refreshed); err != nil {
				return "", "", fmt.Errorf("saving refreshed token: %w", err)
			}
			return refreshed.AccessToken, refreshed.AccountID, nil
		}

		return cred.AccessToken, cred.AccountID, nil
	}
}
