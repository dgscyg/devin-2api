// 本文件验证 Anthropic Messages 请求能保留系统提示、多模态输入、工具和工具结果。
package messages

import (
	"testing"

	"github.com/dgscyg/devin-2api/internal/llm"
)

// TestDecodeRequestBuildsConversationContext 验证 system、image、tool_use 和 tool_result 的保留。
func TestDecodeRequestBuildsConversationContext(t *testing.T) {
	data := []byte(`{
  "model": "claude-test",
  "system": "你是一个谨慎的助手。",
  "messages": [
    {"role": "user", "content": [
      {"type": "text", "text": "读取这个文件"},
      {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}}
    ]},
    {"role": "assistant", "content": [{"type": "tool_use", "id": "call-1", "name": "read_file", "input": {"path": "a.txt"}}]},
    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call-1", "content": "内容"}]}
  ],
  "max_tokens": 256,
  "tools": [{"name": "read_file", "description": "读取文件", "input_schema": {"type": "object"}}]
}`)

	request, err := DecodeRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	if request.Context.Model != "claude-test" {
		t.Fatalf("Model = %q, want claude-test", request.Context.Model)
	}
	if request.Options.MaxOutputTokens != 256 {
		t.Fatalf("MaxOutputTokens = %d, want 256", request.Options.MaxOutputTokens)
	}
	if request.Context.SystemPrompt != "你是一个谨慎的助手。" {
		t.Fatalf("SystemPrompt = %q", request.Context.SystemPrompt)
	}
	if len(request.Context.Messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(request.Context.Messages))
	}
	if _, ok := request.Context.Messages[0].(llm.UserMessage); !ok {
		t.Fatalf("message[0] type = %T, want llm.UserMessage", request.Context.Messages[0])
	}
	if _, ok := request.Context.Messages[1].(llm.AssistantMessage); !ok {
		t.Fatalf("message[1] type = %T, want llm.AssistantMessage", request.Context.Messages[1])
	}
	if tool, ok := request.Context.Messages[1].(llm.AssistantMessage); ok {
		if call, ok2 := tool.Content[0].(llm.ToolCall); !ok2 || call.Name != "read_file" {
			t.Fatalf("assistant content = %#v", tool.Content)
		}
	}
	if _, ok := request.Context.Messages[2].(llm.ToolResultMessage); !ok {
		t.Fatalf("message[2] type = %T, want llm.ToolResultMessage", request.Context.Messages[2])
	}
	if len(request.Context.Tools) != 1 || request.Context.Tools[0].Name != "read_file" {
		t.Fatalf("tools = %#v", request.Context.Tools)
	}
	if err := request.Context.Validate(); err != nil {
		t.Fatalf("context validation error = %v", err)
	}
}

// TestDecodeRequestAcceptsStringContent 验证简短字符串输入会转换为用户文字消息。
func TestDecodeRequestAcceptsStringContent(t *testing.T) {
	request, err := DecodeRequest([]byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}],"max_tokens":256}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Context.Messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(request.Context.Messages))
	}
	message := request.Context.Messages[0].(llm.UserMessage)
	if message.Content[0].(llm.TextContent).Text != "hello" {
		t.Fatalf("message content = %#v", message.Content)
	}
}
