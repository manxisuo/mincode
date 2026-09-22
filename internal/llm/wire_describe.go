package llm

// wirePreviewMax limits per-message content preview in wire summaries.
const wirePreviewMax = 240

// WireMessageInfo is one message in a provider-bound request summary.
type WireMessageInfo struct {
	Index          int      `json:"index"`
	Role           string   `json:"role"`
	ContentLen     int      `json:"content_len"`
	ContentPreview string   `json:"content_preview,omitempty"`
	ToolCallID     string   `json:"tool_call_id,omitempty"`
	ToolCalls      []string `json:"tool_calls,omitempty"`
}

// WireToolInfo is one tool schema offered on the wire.
type WireToolInfo struct {
	Name           string `json:"name"`
	SchemaBytes    int    `json:"schema_bytes"`
	DescriptionLen int    `json:"description_len,omitempty"`
}

// RequestSummary describes what will be (or was) sent to the provider.
// It is observational metadata — not a change to the request itself.
type RequestSummary struct {
	Provider       string            `json:"provider"`
	Model          string            `json:"model"`
	Stream         bool              `json:"stream"`
	Messages       []WireMessageInfo `json:"messages"`
	Tools          []WireToolInfo    `json:"tools"`
	MessageCount   int               `json:"message_count"`
	ToolCount      int               `json:"tool_count"`
	ContentBytes   int               `json:"content_bytes"`
	SchemaBytes    int               `json:"schema_bytes"`
	HasTemperature bool              `json:"has_temperature,omitempty"`
	Temperature    *float64          `json:"temperature,omitempty"`
	MaxTokens      *int              `json:"max_tokens,omitempty"`
}

// DescribeRequest builds a bounded summary of a ChatRequest for wire view.
// model/provider may be empty; ChatRequest.Model wins when set.
func DescribeRequest(req ChatRequest, provider, model string) RequestSummary {
	m := req.Model
	if m == "" {
		m = model
	}
	sum := RequestSummary{
		Provider: provider,
		Model:    m,
		Messages: make([]WireMessageInfo, 0, len(req.Messages)),
		Tools:    make([]WireToolInfo, 0, len(req.Tools)),
	}
	if req.Temperature != nil {
		t := *req.Temperature
		sum.Temperature = &t
		sum.HasTemperature = true
	}
	if req.MaxTokens != nil {
		mt := *req.MaxTokens
		sum.MaxTokens = &mt
	}
	for i, msg := range req.Messages {
		info := WireMessageInfo{
			Index:      i,
			Role:       string(msg.Role),
			ContentLen: len(msg.Content),
			ToolCallID: msg.ToolCallID,
		}
		if msg.Content != "" {
			info.ContentPreview = truncateRunes(msg.Content, wirePreviewMax)
		}
		for _, tc := range msg.ToolCalls {
			info.ToolCalls = append(info.ToolCalls, tc.Name)
		}
		sum.ContentBytes += len(msg.Content)
		sum.Messages = append(sum.Messages, info)
	}
	for _, td := range req.Tools {
		sum.Tools = append(sum.Tools, WireToolInfo{
			Name:           td.Name,
			SchemaBytes:    len(td.Parameters),
			DescriptionLen: len(td.Description),
		})
		sum.SchemaBytes += len(td.Parameters) + len(td.Description)
	}
	sum.MessageCount = len(req.Messages)
	sum.ToolCount = len(req.Tools)
	return sum
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
