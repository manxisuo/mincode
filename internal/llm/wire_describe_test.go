package llm

import "testing"

func TestDescribeRequest(t *testing.T) {
	temp := 0.2
	maxTok := 100
	req := ChatRequest{
		Model: "m-test",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys line1\nsys line2"},
			{Role: RoleUser, Content: "hello"},
			{
				Role:      RoleAssistant,
				Content:   "",
				ToolCalls: []ToolCall{{ID: "c1", Name: "grep", Arguments: `{"pattern":"x"}`}},
			},
			{Role: RoleTool, Content: "result body", ToolCallID: "c1"},
		},
		Tools: []ToolDefinition{
			{Name: "grep", Description: "search", Parameters: []byte(`{"type":"object"}`)},
		},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	}
	sum := DescribeRequest(req, "openai-compatible", "")
	if sum.Model != "m-test" {
		t.Fatalf("model=%q", sum.Model)
	}
	if sum.MessageCount != 4 || sum.ToolCount != 1 {
		t.Fatalf("counts msg=%d tools=%d", sum.MessageCount, sum.ToolCount)
	}
	if sum.Messages[2].ToolCalls == nil || sum.Messages[2].ToolCalls[0] != "grep" {
		t.Fatalf("assistant tool calls=%v", sum.Messages[2].ToolCalls)
	}
	if sum.Messages[3].ToolCallID != "c1" {
		t.Fatalf("tool_call_id=%q", sum.Messages[3].ToolCallID)
	}
	if sum.ContentBytes <= 0 || sum.SchemaBytes <= 0 {
		t.Fatalf("bytes content=%d schema=%d", sum.ContentBytes, sum.SchemaBytes)
	}
	if sum.Temperature == nil || *sum.Temperature != 0.2 {
		t.Fatalf("temp=%v", sum.Temperature)
	}
}

func TestDescribeRequestPreviewCap(t *testing.T) {
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'a'
	}
	sum := DescribeRequest(ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: string(long)}},
	}, "p", "m")
	if len([]rune(sum.Messages[0].ContentPreview)) > wirePreviewMax+1 {
		t.Fatalf("preview not capped: %d", len([]rune(sum.Messages[0].ContentPreview)))
	}
	if sum.Messages[0].ContentLen != 500 {
		t.Fatalf("content_len=%d", sum.Messages[0].ContentLen)
	}
}
