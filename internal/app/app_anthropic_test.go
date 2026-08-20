// 本文件验证 /v1/messages 路由能被正确解码、适配和编码。
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

// TestMessagesHandlerStreamsSSE 验证 Anthropic 流式返回 event + data 的 SSE。
func TestMessagesHandlerStreamsSSE(t *testing.T) {
	final := &llm.AssistantMessage{ResponseID: "msg-1", ResponseModel: "claude-test", Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonStop}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{ResponseID: "msg-1", StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextStart, ContentIndex: 0, Partial: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 0, Delta: "hello", Partial: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextEnd, ContentIndex: 0, Content: "hello", Partial: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hi"}],"max_tokens":256,"stream":true}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: message_start") || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("body missing expected events: %s", body)
	}
	if len(fake.lastRequest.Messages) != 1 {
		t.Fatalf("adapter message count = %d, want 1", len(fake.lastRequest.Messages))
	}
}

// TestMessagesHandlerReturnsJSON 验证 Anthropic 非流式返回完整 JSON。
func TestMessagesHandlerReturnsJSON(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{
		Type:    llm.ResponseEventDone,
		Reason:  llm.StopReasonStop,
		Message: &llm.AssistantMessage{ResponseID: "msg-1", ResponseModel: "claude-test", StopReason: llm.StopReasonStop},
	}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hi"}],"max_tokens":256}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var parsed map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["type"] != "message" {
		t.Fatalf("type = %v, want message", parsed["type"])
	}
}

// TestMessagesHandlerStreamError 验证 Anthropic 流式中途错误返回 event: error 且不发 message_stop。
func TestMessagesHandlerStreamError(t *testing.T) {
	text := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}
	failed := &llm.AssistantMessage{Provider: "devin", StopReason: llm.StopReasonError, ErrorMessage: "resource_exhausted: rate limit exceeded"}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextStart, ContentIndex: 0, Partial: text},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 0, Delta: "hello", Partial: text},
		{Type: llm.ResponseEventError, Reason: llm.StopReasonError, Error: failed},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hi"}],"max_tokens":256,"stream":true}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: error") {
		t.Fatalf("body missing error event: %s", body)
	}
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Fatalf("body missing error type: %s", body)
	}
	if strings.Contains(body, "event: message_stop") {
		t.Fatalf("error stream should not contain message_stop: %s", body)
	}
}
