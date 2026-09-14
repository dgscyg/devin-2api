// 本文件验证逐单元探测的文本收集、请求体替换与拦截判定。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// blockedSentence 模拟被上游整句指纹拦截的文本。
const blockedSentence = "For clear communication with the user the assistant MUST avoid using emojis."

func TestCollectUnitsSplitsSystemBlocksAndMessages(t *testing.T) {
	body := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "第一行文本内容够长\n第二行文本内容也够长"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "用户消息文本内容够长"},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": []any{
					map[string]any{"type": "text", "text": "工具结果文本内容够长"},
				}},
			}},
		},
		"tools": []any{
			map[string]any{"name": "Read", "description": "读取文件的工具说明文本够长"},
		},
	}

	systemUnits := collectUnits(body, "system", "line", 8)
	if len(systemUnits) != 2 || systemUnits[0].Source != "system" {
		t.Fatalf("system units = %#v", systemUnits)
	}

	messageUnits := collectUnits(body, "messages", "line", 8)
	if len(messageUnits) != 2 {
		t.Fatalf("message units = %#v", messageUnits)
	}
	if messageUnits[1].Source != "messages[1].tool_result" || messageUnits[1].Text != "工具结果文本内容够长" {
		t.Fatalf("tool_result unit = %#v", messageUnits[1])
	}

	toolUnits := collectUnits(body, "tools", "line", 8)
	if len(toolUnits) != 1 || !strings.Contains(toolUnits[0].Source, "Read") {
		t.Fatalf("tool units = %#v", toolUnits)
	}

	if short := collectUnits(body, "system", "line", 1000); len(short) != 0 {
		t.Fatalf("min-len should filter short units, got %#v", short)
	}
}

func TestBuildProbeBodyReplacesTargetAndForcesNonStream(t *testing.T) {
	base := map[string]any{
		"model":      "m",
		"stream":     true,
		"max_tokens": 32000,
		"system":     "原始 system",
		"messages":   []any{map[string]any{"role": "user", "content": "原始消息"}},
		"tools":      []any{map[string]any{"name": "Read"}},
	}

	systemProbe := buildProbeBody(base, "system", "探测文本", 32)
	if systemProbe["system"] != "探测文本" {
		t.Fatalf("system probe = %#v", systemProbe["system"])
	}
	if systemProbe["stream"] != false || systemProbe["max_tokens"] != 32 {
		t.Fatalf("probe should force non-stream and small max_tokens: %#v", systemProbe)
	}
	if base["stream"] != true || base["max_tokens"] != 32000 {
		t.Fatalf("base body must not be mutated: %#v", base)
	}

	messageProbe := buildProbeBody(base, "messages", "探测文本", 32)
	if messages := messageProbe["messages"].([]any); len(messages) != 1 {
		t.Fatalf("message probe = %#v", messageProbe["messages"])
	}

	toolProbe := buildProbeBody(base, "tools", "探测文本", 32)
	if _, exists := toolProbe["tools"]; exists {
		t.Fatalf("tool probe should drop tools: %#v", toolProbe)
	}
	if toolProbe["system"] != "探测文本" {
		t.Fatalf("tool probe system = %#v", toolProbe["system"])
	}
}

// TestRunReportsBlockedUnit 的测试动机是确保工具能把被整句指纹拦截的单元从整份请求里挑出来，
// 并且探测请求本身强制为非流式小输出。
func TestRunReportsBlockedUnit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if body["stream"] != false || body["max_tokens"] != float64(16) {
			writer.WriteHeader(http.StatusTeapot)
			_, _ = writer.Write([]byte(`{"error":{"message":"probe did not force non-stream small output"}}`))
			return
		}
		system, _ := body["system"].(string)
		if strings.Contains(system, blockedSentence) {
			writer.WriteHeader(http.StatusForbidden)
			_, _ = writer.Write([]byte(`{"error":{"message":"permission_denied: Your request was blocked by our content policy."}}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer server.Close()

	body := map[string]any{
		"model":      "glm-5-2",
		"stream":     true,
		"max_tokens": 32000,
		"system":     "第一行是安全文本内容\n" + blockedSentence + "\n第三行也是安全文本内容",
		"messages":   []any{map[string]any{"role": "user", "content": "任务描述文本内容够长"}},
	}

	var out bytes.Buffer
	found, err := run(context.Background(), options{
		url: server.URL, target: "system", unit: "line", minLength: 8,
		concurrency: 2, timeout: 5 * time.Second, maxTokens: 16,
	}, body, &out)
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	if !found {
		t.Fatalf("expected a blocked unit, output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "BLOCKED") || !strings.Contains(out.String(), "被拦截单元: 1/3") {
		t.Fatalf("unexpected output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "permission_denied") {
		t.Fatalf("error message should be reported:\n%s", out.String())
	}
}

func TestRunWithoutUnitsFails(t *testing.T) {
	body := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if _, err := run(context.Background(), options{url: "http://127.0.0.1:1", target: "tools", unit: "line", minLength: 8}, body, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error when target has no units")
	}
}
