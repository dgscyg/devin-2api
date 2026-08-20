// 本文件验证 Devin 工具说明注入和 Schema 清理不会改变调用方提供的工具语义。
package devin

import (
	"encoding/json"
	"testing"

	"github.com/dgscyg/devin-2api/internal/llm"
)

// TestWithToolDescriptionsNumbersProseAndPreservesCode 的测试动机是避免连续能力声明触发上游策略误判，同时保持代码示例完整。
func TestWithToolDescriptionsNumbersProseAndPreservesCode(t *testing.T) {
	prompt := withToolDescriptions("", []llm.ToolDefinition{{
		Name: "read&inspect",
		Description: `Read the contents of a file. Supports text files and images (jpg, png).

` + "```json\n" + `{"path":"a&b.txt"}` + "\n```",
	}})
	want := `# tools descriptions
<tool name="read&amp;inspect">
1. Read the contents of a file.
2. Supports text files and images (jpg, png).

` + "```json\n" + `{"path":"a&amp;b.txt"}` + "\n```\n" + `</tool>`
	if prompt != want {
		t.Fatalf("prompt = %q, want %q", prompt, want)
	}
}

// TestFormatToolDescriptionHandlesChineseAndJSON 的测试动机是覆盖无空格中文句界，同时防止 JSON 示例被误拆为自然语言条目。
func TestFormatToolDescriptionHandlesChineseAndJSON(t *testing.T) {
	description := "读取文件。支持图片。\n\n{\"example\":\"Keep. Together.\"}"
	want := "1. 读取文件。\n2. 支持图片。\n\n{\"example\":\"Keep. Together.\"}"
	if formatted := formatToolDescription(description); formatted != want {
		t.Fatalf("formatted = %q, want %q", formatted, want)
	}
}

// TestFormatToolDescriptionRenumbersExistingLists 的测试动机是防止客户端已有的列表编号被当成句末标点拆散。
func TestFormatToolDescriptionRenumbersExistingLists(t *testing.T) {
	description := "Usage:\n1. Read a file.\n- Supports images."
	want := "1. Usage:\n2. Read a file.\n3. Supports images."
	if formatted := formatToolDescription(description); formatted != want {
		t.Fatalf("formatted = %q, want %q", formatted, want)
	}
}

// TestConvertToolDefinitionStripsAnnotationsButKeepsSchema 的测试动机是防止清理自然语言时破坏业务字段和输入约束。
func TestConvertToolDefinitionStripsAnnotationsButKeepsSchema(t *testing.T) {
	converted, err := convertToolDefinition(llm.ToolDefinition{
		Name:        "search",
		Description: "Search an MCP server with arbitrary arguments.",
		InputSchema: json.RawMessage(`{
				"type":"object",
				"title":"top title annotation",
				"description":"top annotation",
				"x-description":"extension annotation",
				"properties":{
					"description":{"type":"string","description":"business field annotation"},
					"title":{"type":"string","title":"business field title annotation"},
					"mode":{"type":"string","enum":["fast","deep"],"default":"fast"},
					"metadata":{"type":"object","default":{"description":"literal business value"}}
				},
				"required":["description","title"],
				"additionalProperties":false
			}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if converted.GetName() != "search" || converted.GetDescription() != "search" {
		t.Fatalf("identity = %q/%q", converted.GetName(), converted.GetDescription())
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(converted.GetJsonSchemaString()), &schema); err != nil {
		t.Fatal(err)
	}
	for _, annotation := range []string{"description", "title", "x-description"} {
		if _, exists := schema[annotation]; exists {
			t.Fatalf("top-level annotation %q was not removed: %#v", annotation, schema)
		}
	}
	properties := schema["properties"].(map[string]any)
	descriptionField := properties["description"].(map[string]any)
	if descriptionField["type"] != "string" {
		t.Fatalf("business description field = %#v", descriptionField)
	}
	if _, exists := descriptionField["description"]; exists {
		t.Fatalf("nested annotation was not removed: %#v", descriptionField)
	}
	titleField := properties["title"].(map[string]any)
	if titleField["type"] != "string" {
		t.Fatalf("business title field = %#v", titleField)
	}
	if _, exists := titleField["title"]; exists {
		t.Fatalf("nested title annotation was not removed: %#v", titleField)
	}
	mode := properties["mode"].(map[string]any)
	if mode["default"] != "fast" || schema["additionalProperties"] != false {
		t.Fatalf("schema constraints were changed: %#v", schema)
	}
	metadata := properties["metadata"].(map[string]any)
	defaultValue := metadata["default"].(map[string]any)
	if defaultValue["description"] != "literal business value" {
		t.Fatalf("schema literal was changed: %#v", defaultValue)
	}
}
