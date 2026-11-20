// Package llm wraps the official github.com/openai/openai-go client for
// streaming chat completions with tool calling.
package llm

import (
	"context"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// Message is the message union used in a chat request.
type Message = openai.ChatCompletionMessageParamUnion

// SystemMessage builds a system message.
func SystemMessage(s string) Message { return openai.SystemMessage(s) }

// UserMessage builds a user message.
func UserMessage(s string) Message { return openai.UserMessage(s) }

// ToolMessage builds a tool-result message.
func ToolMessage(content, toolCallID string) Message { return openai.ToolMessage(content, toolCallID) }

// ToolCall is one model-requested function call.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// AssistantMessage builds an assistant message, optionally with tool calls.
func AssistantMessage(content string, calls []ToolCall) Message {
	asst := &openai.ChatCompletionAssistantMessageParam{}
	if content != "" {
		asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{OfString: openai.String(content)}
	}
	for _, tc := range calls {
		asst.ToolCalls = append(asst.ToolCalls, openai.ChatCompletionMessageToolCallParam{
			ID: tc.ID,
			Function: openai.ChatCompletionMessageToolCallFunctionParam{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: asst}
}

// ChatResult is one assistant turn.
type ChatResult struct {
	Content   string
	ToolCalls []ToolCall
	Raw       Message // assistant message to append verbatim to history
}

// Client wraps the OpenAI-compatible chat completions API.
type Client struct {
	api         *openai.Client
	model       string
	temperature float64
	stream      bool
}

// New creates a client for an OpenAI-compatible endpoint.
func New(apiBase, apiKey, model string, temperature float64, stream bool) *Client {
	c := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(apiBase),
	)
	return &Client{api: &c, model: model, temperature: temperature, stream: stream}
}

// Chat runs one completion. onContent, if non-nil, receives streamed content
// deltas.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []openai.ChatCompletionToolParam, onContent func(string)) (*ChatResult, error) {
	params := openai.ChatCompletionNewParams{
		Model:       openai.ChatModel(c.model),
		Messages:    messages,
		Temperature: openai.Float(c.temperature),
	}
	if len(tools) > 0 {
		params.Tools = tools
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("auto")}
	}
	if c.stream {
		return c.chatStream(ctx, params, onContent)
	}
	return c.chatOnce(ctx, params, onContent)
}

func (c *Client) chatOnce(ctx context.Context, params openai.ChatCompletionNewParams, onContent func(string)) (*ChatResult, error) {
	resp, err := c.api.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return c.buildResult("", nil), nil
	}
	m := resp.Choices[0].Message
	var calls []ToolCall
	for _, tc := range m.ToolCalls {
		calls = append(calls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	if onContent != nil && m.Content != "" {
		onContent(m.Content)
	}
	return c.buildResult(m.Content, calls), nil
}

// toolDelta is one streamed fragment of a tool call.
type toolDelta struct {
	Index int64
	ID    string
	Name  string
	Args  string
}

// toolAccumulator merges streamed tool-call fragments into complete calls.
//
// OpenAI streams each call incrementally keyed by index: the first fragment
// carries id/name, and later fragments append arguments for the same index.
//
// Compatibility: some OpenAI-compatible endpoints omit `index` (leaving it 0
// for every call) or reuse index 0 across calls. We therefore track, per raw
// index, the accumulator slot currently receiving fragments. When a fragment
// carries an id/name that conflicts with the current slot, a new synthetic slot
// is started and becomes the current slot for that raw index, so later
// id-less/name-less continuation fragments are appended to the new call rather
// than to the previous one.
//
// Known limitation: when an endpoint omits BOTH `index` and `id`/`name` on
// every fragment there is no signal to separate calls and they are merged into
// one. Such endpoints are non-conformant; set `stream: false` for them.
type toolAccumulator struct {
	acc   map[int64]*ToolCall
	order []int64
	cur   map[int64]int64
	synth int64
}

func newToolAccumulator() *toolAccumulator {
	return &toolAccumulator{
		acc:   map[int64]*ToolCall{},
		cur:   map[int64]int64{},
		synth: int64(1 << 30),
	}
}

func (ta *toolAccumulator) add(d toolDelta) {
	slot, ok := ta.cur[d.Index]
	if !ok {
		slot = d.Index
		ta.cur[d.Index] = slot
	}
	if a := ta.acc[slot]; a != nil &&
		((d.ID != "" && a.ID != "" && d.ID != a.ID) ||
			(d.Name != "" && a.Name != "" && d.Name != a.Name)) {
		slot = ta.synth
		ta.synth++
		ta.cur[d.Index] = slot
	}
	a := ta.acc[slot]
	if a == nil {
		a = &ToolCall{}
		ta.acc[slot] = a
		ta.order = append(ta.order, slot)
	}
	if d.ID != "" {
		a.ID = d.ID
	}
	if d.Name != "" {
		a.Name = d.Name
	}
	a.Arguments += d.Args
}

func (ta *toolAccumulator) result() []ToolCall {
	out := make([]ToolCall, 0, len(ta.order))
	for _, slot := range ta.order {
		out = append(out, *ta.acc[slot])
	}
	return out
}

func (c *Client) chatStream(ctx context.Context, params openai.ChatCompletionNewParams, onContent func(string)) (*ChatResult, error) {
	stream := c.api.Chat.Completions.NewStreaming(ctx, params)
	ta := newToolAccumulator()
	var sb strings.Builder

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			sb.WriteString(d.Content)
			if onContent != nil {
				onContent(d.Content)
			}
		}
		for _, tcd := range d.ToolCalls {
			ta.add(toolDelta{
				Index: tcd.Index,
				ID:    tcd.ID,
				Name:  tcd.Function.Name,
				Args:  tcd.Function.Arguments,
			})
		}
	}
	if err := stream.Err(); err != nil {
		_ = stream.Close()
		return nil, err
	}
	_ = stream.Close()

	return c.buildResult(sb.String(), ta.result()), nil
}

func (c *Client) buildResult(content string, calls []ToolCall) *ChatResult {
	return &ChatResult{
		Content:   content,
		ToolCalls: calls,
		Raw:       AssistantMessage(content, calls),
	}
}
