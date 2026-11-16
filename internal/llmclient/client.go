package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultTimeout   = 8 * time.Second
	maxRequestBytes  = 32 << 10
	maxResponseBytes = 64 << 10
)

type Completer interface {
	Complete(ctx context.Context, input Request) (Response, error)
}

type Request struct {
	SystemPrompt string
	UserPrompt   string
}

type Response struct {
	Content string
}

type Client struct {
	config Config
	http   *http.Client
}

func (*Client) String() string   { return "llmclient.Client" }
func (*Client) GoString() string { return "llmclient.Client" }

func NewClient(cfg Config) (*Client, error) {
	if !cfg.Enabled() {
		return nil, ErrDisabled
	}
	return &Client{
		config: cfg,
		http: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				IdleConnTimeout:   90 * time.Second,
				ForceAttemptHTTP2: true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
}

type completionResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content      string            `json:"content"`
			ToolCalls    []json.RawMessage `json:"tool_calls"`
			FunctionCall json.RawMessage   `json:"function_call"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *Client) Complete(ctx context.Context, input Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if !c.config.Enabled() {
		return Response{}, ErrDisabled
	}
	// Bound the input before marshaling, then check the actual JSON bytes.
	available := maxRequestBytes - len(c.config.model)
	if len(input.SystemPrompt) > available || len(input.UserPrompt) > available-len(input.SystemPrompt) {
		return Response{}, ErrModelRequestTooLarge
	}
	body, err := json.Marshal(completionRequest{
		Model: c.config.model,
		Messages: []message{
			{Role: "system", Content: input.SystemPrompt},
			{Role: "user", Content: input.UserPrompt},
		},
	})
	if err != nil {
		return Response{}, ErrInvalidConfig
	}
	if len(body) > maxRequestBytes {
		return Response{}, ErrModelRequestTooLarge
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, ErrInvalidConfig
	}
	// Prevent Transport from replaying a failed POST on a reused connection.
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return Response{}, classifyHTTPError(ctx, err)
	}
	defer resp.Body.Close()
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, &HTTPStatusError{StatusCode: resp.StatusCode}
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Response{}, classifyHTTPError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if len(payload) > maxResponseBytes {
		return Response{}, ErrModelResponseTooLarge
	}
	return parseCompletion(payload)
}

func parseCompletion(payload []byte) (Response, error) {
	var result completionResponse
	// Strict JSON decoding rejects duplicate keys that could hide a tool call.
	if jsonv2.Unmarshal(payload, &result) != nil || len(result.Choices) == 0 {
		return Response{}, ErrModelInvalidResponse
	}
	for _, choice := range result.Choices {
		functionCall := choice.Message.FunctionCall
		if len(choice.Message.ToolCalls) != 0 ||
			(len(functionCall) != 0 && !bytes.Equal(bytes.TrimSpace(functionCall), []byte("null"))) {
			return Response{}, ErrModelInvalidResponse
		}
	}
	first := result.Choices[0]
	if first.FinishReason != "stop" || strings.TrimSpace(first.Message.Content) == "" {
		return Response{}, ErrModelInvalidResponse
	}
	return Response{Content: first.Message.Content}, nil
}
