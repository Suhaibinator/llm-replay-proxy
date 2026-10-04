package rehearsal

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"git.per.com/suhaib/go-common/pkg/common_service/common_service_models"
	"git.per.com/suhaib/go-common/pkg/config"
	"git.per.com/suhaib/go-common/pkg/genai"
	"git.per.com/suhaib/go-common/pkg/genai/apishape"
	"git.per.com/suhaib/go-common/pkg/proto_models"
	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
)

const providerID = "replay-proxy"

type Client struct {
	factory *genai.ModelRuntimeFactory
	model   string
	mu      sync.Mutex
	tools   map[proto_models.InferenceAPI]ai.Tool
}

// New creates a client for a proxy that does not require tokens (for
// example in-process test stacks or auth.disabled on loopback).
func New(ctx context.Context, baseURL, model string) (*Client, error) {
	return NewWithToken(ctx, baseURL, model, "")
}

// NewWithToken creates a client that authenticates with a token issued by
// `replay-proxy token issue`. Go Common's default HTTP authentication sends it
// as `Authorization: Bearer` for Chat Completions and Responses and as
// `x-api-key` for Anthropic Messages; the proxy accepts both.
func NewWithToken(ctx context.Context, baseURL, model, token string) (*Client, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return nil, fmt.Errorf("proxy base URL is required")
	}
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}
	provider := config.ProviderConfig{
		ID: providerID, Name: "Inference replay proxy", Adapter: config.ProviderAdapterHTTP,
		DefaultAPI: apishape.Responses, HTTP: config.HTTPProviderOptions{Auth: config.HTTPAuthNone},
		Endpoints: config.InferenceEndpoints{
			ChatCompletions:   baseURL + "/v1/chat/completions",
			Responses:         baseURL + "/v1/responses",
			AnthropicMessages: baseURL + "/v1/messages",
		},
	}
	cfg := genai.RuntimeFactoryConfig{
		Providers: []config.ProviderConfig{provider},
	}
	if token != "" {
		cfg.Providers[0].HTTP.Auth = ""
		cfg.Providers[0].CredentialRef = providerID + "-token"
		cfg.Credentials = []config.CredentialBinding{{Name: providerID + "-token", Secret: config.NewSecret(token)}}
	}
	if err := config.ValidateCredentialBindings(cfg.Providers, cfg.Credentials); err != nil {
		return nil, fmt.Errorf("validate Go Common provider: %w", err)
	}
	factory, err := genai.NewModelRuntimeFactory(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create Go Common runtime: %w", err)
	}
	return &Client{factory: factory, model: model, tools: make(map[proto_models.InferenceAPI]ai.Tool)}, nil
}

func (c *Client) Close() { c.factory.Close() }

func API(name string) (proto_models.InferenceAPI, error) {
	switch name {
	case "chat":
		return apishape.ChatCompletions, nil
	case "responses":
		return apishape.Responses, nil
	case "anthropic":
		return apishape.AnthropicMessages, nil
	default:
		return 0, fmt.Errorf("unknown API %q (use chat, responses, or anthropic)", name)
	}
}

func (c *Client) Text(ctx context.Context, api proto_models.InferenceAPI, prompt string) (string, error) {
	resolved, err := c.resolve(api)
	if err != nil {
		return "", err
	}
	response, err := genkit.Generate(ctx, resolved.Genkit,
		ai.WithModelName(resolved.ModelName), ai.WithPrompt(prompt))
	if err != nil {
		return "", err
	}
	return response.Text(), nil
}

func (c *Client) StreamingText(ctx context.Context, api proto_models.InferenceAPI, prompt string) (string, error) {
	resolved, err := c.resolve(api)
	if err != nil {
		return "", err
	}
	var streamed strings.Builder
	response, err := genkit.Generate(ctx, resolved.Genkit, ai.WithModelName(resolved.ModelName), ai.WithPrompt(prompt),
		ai.WithStreaming(func(_ context.Context, chunk *ai.ModelResponseChunk) error {
			streamed.WriteString(chunk.Text())
			return nil
		}))
	if err != nil {
		return "", err
	}
	if got := streamed.String(); got != "" {
		return got, nil
	}
	return response.Text(), nil
}

// ToolRoundTrip makes two inference requests. The tool result is produced by the
// application between them; the proxy records inference, not local side effects.
func (c *Client) ToolRoundTrip(ctx context.Context, api proto_models.InferenceAPI, prompt string) (string, error) {
	return c.ToolRoundTripWithResult(ctx, api, prompt, "sunny, 21 C")
}

func (c *Client) ToolRoundTripWithResult(ctx context.Context, api proto_models.InferenceAPI, prompt, toolResult string) (string, error) {
	return c.toolRoundTrip(ctx, api, prompt, toolResult, false)
}

func (c *Client) StreamingToolRoundTrip(ctx context.Context, api proto_models.InferenceAPI, prompt, toolResult string) (string, error) {
	return c.toolRoundTrip(ctx, api, prompt, toolResult, true)
}

func (c *Client) toolRoundTrip(ctx context.Context, api proto_models.InferenceAPI, prompt, toolResult string, streaming bool) (string, error) {
	resolved, err := c.resolve(api)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	tool := c.tools[api]
	if tool == nil {
		tool = genkit.DefineTool(resolved.Genkit, "lookup_weather", "Look up deterministic demo weather",
			func(*ai.ToolContext, struct {
				City string `json:"city"`
			}) (string, error) {
				return "sunny, 21 C", nil
			})
		c.tools[api] = tool
	}
	c.mu.Unlock()
	firstOptions := []ai.GenerateOption{ai.WithModelName(resolved.ModelName), ai.WithPrompt(prompt), ai.WithTools(tool), ai.WithReturnToolRequests(true)}
	if streaming {
		firstOptions = append(firstOptions, ai.WithStreaming(func(context.Context, *ai.ModelResponseChunk) error { return nil }))
	}
	first, err := genkit.Generate(ctx, resolved.Genkit, firstOptions...)
	if err != nil {
		return "", fmt.Errorf("request tool call: %w", err)
	}
	requests := first.ToolRequests()
	if len(requests) != 1 || requests[0].ToolRequest == nil {
		return "", fmt.Errorf("expected exactly one tool request, got %d", len(requests))
	}
	call := requests[0].ToolRequest
	// This fixed demo output intentionally represents an application-owned side effect.
	toolMessage := &ai.Message{Role: ai.RoleTool, Content: []*ai.Part{ai.NewToolResponsePart(&ai.ToolResponse{
		Name: call.Name, Ref: call.Ref, Output: toolResult,
	})}}
	finalOptions := []ai.GenerateOption{ai.WithModelName(resolved.ModelName), ai.WithMessages(first.Message, toolMessage), ai.WithTools(tool), ai.WithReturnToolRequests(true)}
	var streamed strings.Builder
	if streaming {
		finalOptions = append(finalOptions, ai.WithStreaming(func(_ context.Context, chunk *ai.ModelResponseChunk) error {
			streamed.WriteString(chunk.Text())
			return nil
		}))
	}
	final, err := genkit.Generate(ctx, resolved.Genkit, finalOptions...)
	if err != nil {
		return "", fmt.Errorf("submit tool result: %w", err)
	}
	if streamed.Len() > 0 {
		return streamed.String(), nil
	}
	return final.Text(), nil
}

func (c *Client) resolve(api proto_models.InferenceAPI) (*genai.ResolvedModel, error) {
	return c.factory.ResolveModelForAPI(&common_service_models.InferenceModel{
		ProviderID: providerID, ProviderModelID: c.model,
	}, api)
}
