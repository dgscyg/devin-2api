// 本文件验证 app 能把 OpenAI HTTP 请求交给 adapter，并按顺序输出 Responses SSE。
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dgscyg/devin-2api/internal/adapter"
	"github.com/dgscyg/devin-2api/internal/config"
	"github.com/dgscyg/devin-2api/internal/debuglog"
	"github.com/dgscyg/devin-2api/internal/llm"
)

type fakeAdapter struct {
	lastRequest llm.RequestMessages
	events      []llm.ResponseEvent
	models      []adapter.ModelInfo
	modelsErr   error
}

func (fake *fakeAdapter) Stream(_ context.Context, request llm.RequestMessages) (llm.ResponseStream, error) {
	fake.lastRequest = request
	return &fakeStream{events: fake.events}, nil
}

func (fake *fakeAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	if fake.modelsErr != nil {
		return nil, fake.modelsErr
	}
	if fake.models != nil {
		return fake.models, nil
	}
	return []adapter.ModelInfo{{ID: "gpt-test", Created: 1, OwnedBy: "test"}}, nil
}

type fakeStream struct {
	events []llm.ResponseEvent
	index  int
}

func (stream *fakeStream) Recv(_ context.Context) (llm.ResponseEvent, error) {
	if stream.index >= len(stream.events) {
		return llm.ResponseEvent{}, io.EOF
	}
	event := stream.events[stream.index]
	stream.index++
	return event, nil
}

// concurrentFakeAdapter 为每个 model 返回独立的事件流，用于并发隔离测试。
type concurrentFakeAdapter struct {
	mu     sync.Mutex
	events map[string][]llm.ResponseEvent
}

func (c *concurrentFakeAdapter) Stream(_ context.Context, request llm.RequestMessages) (llm.ResponseStream, error) {
	c.mu.Lock()
	events := c.events[request.Model]
	c.mu.Unlock()
	return &fakeStream{events: events}, nil
}

func (c *concurrentFakeAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "model-a", Created: 1, OwnedBy: "test"}, {ID: "model-b", Created: 1, OwnedBy: "test"}, {ID: "model-c", Created: 1, OwnedBy: "test"}}, nil
}

// TestResponsesHandlerStreamsOrderedEvents 验证请求路由和 SSE 事件顺序。
func TestResponsesHandlerStreamsOrderedEvents(t *testing.T) {
	final := &llm.AssistantMessage{
		ResponseID:    "resp-1",
		ResponseModel: "gpt-test",
		Content:       []llm.Content{llm.TextContent{Text: "hello"}},
		StopReason:    llm.StopReasonStop,
	}
	partial := &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "hello"}}, StopReason: llm.StopReasonPending}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart, Partial: &llm.AssistantMessage{ResponseID: "resp-1", StopReason: llm.StopReasonPending}},
		{Type: llm.ResponseEventTextStart, ContentIndex: 0, Partial: partial},
		{Type: llm.ResponseEventTextDelta, ContentIndex: 0, Delta: "hello", Partial: partial},
		{Type: llm.ResponseEventTextEnd, ContentIndex: 0, Content: "hello", Partial: partial},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","stream":true,"input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header().Get("Content-Type"))
	}
	body := response.Body.String()
	if strings.Index(body, "response.created") > strings.Index(body, "response.output_item.added") || strings.Index(body, "response.output_text.delta") > strings.Index(body, "response.completed") {
		t.Fatalf("events are out of order: %s", body)
	}
	for _, expected := range []string{"response.in_progress", "response.content_part.added", "response.content_part.done", "response.output_item.done", `"output":[{"content"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body = %s, want %s", body, expected)
		}
	}
	if len(fake.lastRequest.Messages) != 1 {
		t.Fatalf("adapter message count = %d, want 1", len(fake.lastRequest.Messages))
	}
}

// TestResponsesHandlerReturnsJSONForNonStream 验证非流式请求返回最终 JSON。
func TestResponsesHandlerReturnsJSONForNonStream(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{
		Type:   llm.ResponseEventDone,
		Reason: llm.StopReasonStop,
		Message: &llm.AssistantMessage{
			ResponseID: "resp-1", ResponseModel: "gpt-test", StopReason: llm.StopReasonStop,
		},
	}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"object":"response"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

// TestResponsesHandlerConsumesAllAssistantRounds 的测试动机是保证非流式模式消费完整事件流并返回最后一轮助手内容。
func TestResponsesHandlerConsumesAllAssistantRounds(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonToolUse, Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "first"}}, ResponseModel: "gpt-test", StopReason: llm.StopReasonToolUse}},
		{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{Content: []llm.Content{llm.TextContent{Text: "last"}}, ResponseModel: "gpt-test", StopReason: llm.StopReasonStop}},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"text":"last"`) || strings.Contains(response.Body.String(), `"text":"first"`) {
		t.Fatalf("body = %s, want only last assistant round", response.Body.String())
	}
}

// TestResponsesHandlerWritesStageLogs 的测试动机是保证 HTTP 边界和中间响应事件可以按一次请求完整回放。
func TestResponsesHandlerWritesStageLogs(t *testing.T) {
	final := &llm.AssistantMessage{
		Provider: "devin", ResponseID: "resp-1", ResponseModel: "model", StopReason: llm.StopReasonStop,
	}
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final}}}
	root := filepath.Join(t.TempDir(), "logs")
	application := New(fake, config.ServerConfig{Listen: ":0"}, debuglog.NewManager(root))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("request directory count = %d, want 1", len(entries))
	}
	directory := filepath.Join(root, entries[0].Name())
	for _, name := range []string{"meta.json", "01-http-request.json", "02-request-messages.json", "05-response-events.jsonl", "06-http-response.jsonl"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	meta, err := os.ReadFile(filepath.Join(directory, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), `"result": "completed"`) || !strings.Contains(string(meta), `"provider": "devin"`) {
		t.Fatalf("meta = %s", meta)
	}
}

// TestResponsesHandlerMarksStreamError 的测试动机是避免已输出失败 SSE 的请求被误记为成功。
func TestResponsesHandlerMarksStreamError(t *testing.T) {
	failed := &llm.AssistantMessage{Provider: "devin", StopReason: llm.StopReasonError, ErrorMessage: "upstream failed"}
	fake := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.ResponseEventStart},
		{Type: llm.ResponseEventError, Reason: llm.StopReasonError, Error: failed},
	}}
	root := filepath.Join(t.TempDir(), "logs")
	application := New(fake, config.ServerConfig{Listen: ":0"}, debuglog.NewManager(root))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","stream":true,"input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, entries[0].Name())
	meta, err := os.ReadFile(filepath.Join(directory, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), `"result": "failed"`) {
		t.Fatalf("meta = %s", meta)
	}
	errorLog, err := os.ReadFile(filepath.Join(directory, "error.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(errorLog), `"stage": "http_stream"`) {
		t.Fatalf("error = %s", errorLog)
	}
}

// TestResponsesHandlerRejectsMissingAPIKey 验证未提供密钥时 /v1/* 返回 401。
func TestResponsesHandlerRejectsMissingAPIKey(t *testing.T) {
	fake := &fakeAdapter{}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"type":"unauthenticated"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

// TestResponsesHandlerRejectsInvalidAPIKey 验证错误密钥无法通过鉴权。
func TestResponsesHandlerRejectsInvalidAPIKey(t *testing.T) {
	fake := &fakeAdapter{}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Authorization", "Bearer wrong-key")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerAcceptsBearerAPIKey 验证 Authorization: Bearer <key> 通用格式可用。
func TestResponsesHandlerAcceptsBearerAPIKey(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{ResponseID: "resp-1", ResponseModel: "gpt-test", StopReason: llm.StopReasonStop}}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer secret-key")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerAcceptsXApiKeyHeader 验证兼容头 X-Api-Key 也可用。
func TestResponsesHandlerAcceptsXApiKeyHeader(t *testing.T) {
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{ResponseID: "resp-1", ResponseModel: "gpt-test", StopReason: llm.StopReasonStop}}}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "secret-key")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerHealthIsUnprotected 验证 /healthz 不受 API Key 保护。
func TestResponsesHandlerHealthIsUnprotected(t *testing.T) {
	application := New(&fakeAdapter{}, config.ServerConfig{Listen: ":0"}, nil)
	application.SetAPIKey("secret-key")
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestResponsesHandlerIgnoresLogInitializationFailure 的测试动机是保证诊断写盘故障不会改变兼容 API 的业务结果。
func TestResponsesHandlerIgnoresLogInitializationFailure(t *testing.T) {
	final := &llm.AssistantMessage{ResponseID: "resp-1", ResponseModel: "model", StopReason: llm.StopReasonStop}
	fake := &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: final}}}
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	application := New(fake, config.ServerConfig{Listen: ":0"}, debuglog.NewManager(blockedRoot))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","input":"hi"}`))
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
}

// TestConcurrentResponsesDoNotInterleave 验证高并发下每个请求的响应流互不串扰。
func TestConcurrentResponsesDoNotInterleave(t *testing.T) {
	models := []string{"model-a", "model-b", "model-c"}
	events := make(map[string][]llm.ResponseEvent)
	for _, m := range models {
		events[m] = []llm.ResponseEvent{{
			Type:   llm.ResponseEventDone,
			Reason: llm.StopReasonStop,
			Message: &llm.AssistantMessage{
				ResponseID:    "resp-" + m,
				ResponseModel: m,
				Content:       []llm.Content{llm.TextContent{Text: "unique-response-for-" + m}},
				StopReason:    llm.StopReasonStop,
			},
		}}
	}
	fake := &concurrentFakeAdapter{events: events}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, m := range models {
			wg.Add(1)
			go func(model string) {
				defer wg.Done()
				body := fmt.Sprintf(`{"model":"%s","input":"hi"}`, model)
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				application.Router().ServeHTTP(response, request)
				if response.Code != http.StatusOK {
					t.Errorf("status = %d for model %s: %s", response.Code, model, response.Body.String())
					return
				}
				want := "unique-response-for-" + model
				bodyStr := response.Body.String()
				if !strings.Contains(bodyStr, want) {
					t.Errorf("response for %s missing %q: %s", model, want, bodyStr)
				}
				// 同时确认没有其它 model 的标记串入。
				for _, other := range models {
					if other == model {
						continue
					}
					if strings.Contains(bodyStr, "unique-response-for-"+other) {
						t.Errorf("response for %s contains marker of %s: %s", model, other, bodyStr)
					}
				}
			}(m)
		}
	}
	wg.Wait()
}

// TestFreeOnlyFilterListsAndBlocks 验证 free_only 过滤：
// /v1/models 只返回 free 模型，非 free 模型的生成请求被拒绝。
func TestFreeOnlyFilterListsAndBlocks(t *testing.T) {
	fake := &fakeAdapter{
		events: []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{
			ResponseID: "resp-1", ResponseModel: "free-model", StopReason: llm.StopReasonStop,
		}}},
	}
	fake.models = []adapter.ModelInfo{
		{ID: "free-model", CostTier: adapter.ModelCostTierFree},
		{ID: "paid-model", CostTier: adapter.ModelCostTierHigh},
		{ID: "unknown-model", CostTier: ""},
	}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetModelsFilter(func(models []adapter.ModelInfo) []adapter.ModelInfo {
		return adapter.FilterFreeOnly(models, true)
	})
	application.SetRestrictModels(true)

	// /v1/models 只保留 free 模型。
	listRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	listResponse := httptest.NewRecorder()
	application.Router().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var listed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 || listed.Data[0]["id"] != "free-model" {
		t.Fatalf("listed models = %#v, want only free-model", listed.Data)
	}

	// 非 free 模型生成请求被拒绝。
	for _, model := range []string{"paid-model", "unknown-model"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"hi"}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		application.Router().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("model %s status = %d, want 403: %s", model, response.Code, response.Body.String())
		}
	}

	// free 模型生成请求放行。
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"free-model","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("free model status = %d, want 200: %s", response.Code, response.Body.String())
	}

	// /v1/models/{model} 对过滤后不存在的模型返回 404。
	detail := httptest.NewRequest(http.MethodGet, "/v1/models/paid-model", nil)
	detailResponse := httptest.NewRecorder()
	application.Router().ServeHTTP(detailResponse, detail)
	if detailResponse.Code != http.StatusNotFound {
		t.Fatalf("model detail status = %d, want 404", detailResponse.Code)
	}

	if listed.Data[0]["cost_tier"] != adapter.ModelCostTierFree {
		t.Fatalf("listed cost_tier = %v, want free", listed.Data[0]["cost_tier"])
	}
}

// TestRestrictModelsFailsClosedOnCatalogError 验证启用访问限制后，目录拉取失败会拒绝生成请求。
func TestRestrictModelsFailsClosedOnCatalogError(t *testing.T) {
	fake := &fakeAdapter{
		events:    []llm.ResponseEvent{{Type: llm.ResponseEventDone, Reason: llm.StopReasonStop, Message: &llm.AssistantMessage{ResponseID: "resp-1", StopReason: llm.StopReasonStop}}},
		modelsErr: fmt.Errorf("upstream timeout"),
	}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetModelsFilter(func(models []adapter.ModelInfo) []adapter.ModelInfo {
		return adapter.FilterFreeOnly(models, true)
	})
	application.SetRestrictModels(true)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"free-model","input":"hi"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
	}
}

// TestModelsListExposesCatalogLimits 验证 /v1/models 暴露服务端 max_tokens 与思考等级。
func TestModelsListExposesCatalogLimits(t *testing.T) {
	fake := &fakeAdapter{models: []adapter.ModelInfo{
		{ID: "glm-5-2", CostTier: adapter.ModelCostTierFree, MaxTokens: 32000, ThinkingEffort: "low", SupportsImages: false},
	}}
	application := New(fake, config.ServerConfig{Listen: ":0"}, nil)
	application.SetModelsFilter((adapter.ModelPolicy{FreeOnly: true}).Apply)

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()
	application.Router().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var listed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Data) != 1 {
		t.Fatalf("listed = %#v", listed.Data)
	}
	entry := listed.Data[0]
	if entry["id"] != "glm-5-2" || entry["cost_tier"] != "free" || entry["max_thinking_effort"] != "low" {
		t.Fatalf("entry = %#v", entry)
	}
	if tokens, ok := entry["max_tokens"].(float64); !ok || tokens != 32000 {
		t.Fatalf("max_tokens = %v", entry["max_tokens"])
	}
}
