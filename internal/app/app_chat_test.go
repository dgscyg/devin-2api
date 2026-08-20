// 本文件验证 /v1/chat/completions 路由能被正确解码、适配和编码。
package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dgscyg/devin-2api/internal/config"
	"github.com/dgscyg/devin-2api/internal/llm"
)

// TestChatCompletionsHandlerStreamsSSE 验证 chat 流式返回 data-only SSE。
func TestChatCompletionsHandlerStreamsSSE(t *testing.T) {
	final := &llm.AssistantMessage{ResponseID: "chat-1", ResponseModel: "gpt-test", Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonStop}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{ResponseID: "chat-1", StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextStart, ContentIndex: 0, Partial: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 0, Delta: "hello", Partial: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextEnd, ContentIndex: 0, Content: "hello", Partial: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, `data: {`) || !strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("body missing expected data: %s", body)
	}
	if len(fake.lastRequest.Messages) != 1 {
		t.Fatalf("adapter message count = %d, want 1", len(fake.lastRequest.Messages))
	}
}

// TestChatCompletionsHandlerReturnsJSON 验证 chat 非流式返回完整 JSON。
func TestChatCompletionsHandlerReturnsJSON(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{
		Type:    llm.ResponseEventDone,
		Reason:  llm.StopReasonStop,
		Message: &llm.AssistantMessage{ResponseID: "chat-1", ResponseModel: "gpt-test", StopReason: llm.StopReasonStop},
	}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("object = %v, want chat.completion", parsed["object"])
	}
}

// TestChatCompletionsHandlerStreamError 验证 chat 流式中途错误返回 error chunk 且不发 [DONE]。
func TestChatCompletionsHandlerStreamError(t *testing.T) {
	text := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}
	failed := &llm.AssistantMessage{Provider: "devin", StopReason: llm.StopReasonError, ErrorMessage: "resource_exhausted: rate limit exceeded"}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextStart, ContentIndex: 0, Partial: text},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 0, Delta: "hello", Partial: text},
		{Type: llm.ResponseEventError, Reason: llm.StopReasonError, Error: failed},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, `data: {`) {
		t.Fatalf("body missing SSE data: %s", body)
	}
	if !strings.Contains(body, `"error"`) {
		t.Fatalf("body missing error field: %s", body)
	}
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Fatalf("body missing error type: %s", body)
	}
	if strings.Contains(body, `data: [DONE]`) {
		t.Fatalf("error stream should not contain [DONE]: %s", body)
	}
}

// TestChatCompletionsHandlerStreamsThinking 验证 chat 流式下思考走 reasoning_content、正文走 content。
func TestChatCompletionsHandlerStreamsThinking(t *testing.T) {
	partial := &llm.AssistantMessage{
		Content:    []llm.Content{llm.ThinkingContent{Thinking: "think"}, llm.TextContent{Text: "hello"}},
		StopReason: llm.StopReasonPending,
	}
	final := &llm.AssistantMessage{
		Content:    []llm.Content{llm.ThinkingContent{Thinking: "think"}, llm.TextContent{Text: "hello"}},
		StopReason: llm.StopReasonStop,
	}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventThinkingStart, ContentIndex: 0, Partial: partial},
		{Type: llm.ResponseEventThinkingDelta, ContentIndex: 0, Delta: "think", Partial: partial},
		{Type: llm.ResponseEventThinkingEnd, ContentIndex: 0, Content: "think", Partial: partial},
		{Type: llm.ResponseEventTextStart, ContentIndex: 1, Partial: partial},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 1, Delta: "hello", Partial: partial},
		{Type: llm.ResponseEventTextEnd, ContentIndex: 1, Content: "hello", Partial: partial},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, `"reasoning_content":"think"`) {
		t.Fatalf("body missing reasoning_content: %s", body)
	}
	if !strings.Contains(body, `"content":"hello"`) {
		t.Fatalf("body missing text content: %s", body)
	}
}
