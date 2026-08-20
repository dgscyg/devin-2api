// 本文件定义中间 LLM 请求和响应事件的稳定调试日志投影。
package debuglog

import (
	"github.com/dgscyg/devin-2api/internal/llm"
)

// RequestMessagesProjection 将含接口字段的 RequestMessages 转成可读 JSON 结构。
func RequestMessagesProjection(request llm.RequestMessages) map[string]any {
	messages := make([]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, messageProjection(message))
	}
	tools := make([]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, map[string]any{
			"name": tool.Name, "description": tool.Description, "input_schema": tool.InputSchema,
		})
	}
	return map[string]any{"system_prompt": request.SystemPrompt, "messages": messages, "tools": tools}
}

// ResponseEventProjection 将响应事件转成避免重复完整 Partial 的日志结构。
func ResponseEventProjection(event llm.ResponseEvent) map[string]any {
	result := map[string]any{"type": event.Type}
	switch event.Type {
	case llm.ResponseEventTextStart, llm.ResponseEventTextDelta, llm.ResponseEventTextEnd,
		llm.ResponseEventThinkingStart, llm.ResponseEventThinkingDelta, llm.ResponseEventThinkingEnd,
		llm.ResponseEventToolCallStart, llm.ResponseEventToolCallDelta, llm.ResponseEventToolCallEnd:
		result["content_index"] = event.ContentIndex
	}
	if event.Delta != "" || event.Type == llm.ResponseEventToolCallDelta {
		result["delta"] = event.Delta
	}
	if event.Content != "" {
		result["content"] = event.Content
	}
	if event.ToolCallID != "" {
		result["tool_call_id"] = event.ToolCallID
	}
	if event.ToolName != "" {
		result["tool_name"] = event.ToolName
	}
	if event.ToolCall != nil {
		result["tool_call"] = contentProjection(*event.ToolCall)
	}
	if event.Reason != "" {
		result["reason"] = event.Reason
	}
	if event.Type == llm.ResponseEventStart && event.Partial != nil {
		result["message"] = assistantProjection(*event.Partial)
	}
	if event.Message != nil {
		result["message"] = assistantProjection(*event.Message)
	}
	if event.Error != nil {
		result["error"] = assistantProjection(*event.Error)
	}
	return result
}

func messageProjection(message llm.Message) map[string]any {
	result := map[string]any{"role": message.Role()}
	switch message := message.(type) {
	case llm.UserMessage:
		result["content"] = contentListProjection(message.Content)
		result["timestamp_ms"] = message.TimestampMS
	case llm.AssistantMessage:
		for key, value := range assistantProjection(message) {
			result[key] = value
		}
	case llm.ToolResultMessage:
		result["tool_call_id"] = message.ToolCallID
		result["tool_name"] = message.ToolName
		result["content"] = contentListProjection(message.Content)
		result["details"] = message.Details
		result["usage"] = message.Usage
		result["added_tool_names"] = message.AddedToolNames
		result["is_error"] = message.IsError
		result["timestamp_ms"] = message.TimestampMS
	}
	return result
}

func assistantProjection(message llm.AssistantMessage) map[string]any {
	return map[string]any{
		"role":           llm.MessageRoleAssistant,
		"content":        contentListProjection(message.Content),
		"api":            message.API,
		"provider":       message.Provider,
		"model":          message.Model,
		"response_model": message.ResponseModel,
		"response_id":    message.ResponseID,
		"diagnostics":    message.Diagnostics,
		"usage":          message.Usage,
		"stop_reason":    message.StopReason,
		"error_message":  message.ErrorMessage,
		"timestamp_ms":   message.TimestampMS,
	}
}

func contentListProjection(content []llm.Content) []any {
	result := make([]any, 0, len(content))
	for _, block := range content {
		result = append(result, contentProjection(block))
	}
	return result
}

func contentProjection(content llm.Content) map[string]any {
	switch content := content.(type) {
	case llm.TextContent:
		return map[string]any{"type": content.ContentType(), "text": content.Text, "text_signature": content.TextSignature}
	case llm.ThinkingContent:
		return map[string]any{"type": content.ContentType(), "thinking": content.Thinking, "thinking_signature": content.ThinkingSignature, "redacted": content.Redacted}
	case llm.ImageContent:
		return map[string]any{"type": content.ContentType(), "data": content.Data, "mime_type": content.MIMEType}
	case llm.ToolCall:
		return map[string]any{"type": content.ContentType(), "id": content.ID, "name": content.Name, "arguments": content.Arguments, "thought_signature": content.ThoughtSignature}
	default:
		return map[string]any{"type": "unknown"}
	}
}
