// 本工具逐单元探测请求体中被上游内容策略拦截的文本。
//
// 背景：Devin/Windsurf 对"竞品提示词"按整句指纹做拦截，命中时返回
// permission_denied: Your request was blocked by our content policy。肉眼从几万字的
// 请求体里找不出是哪一句，本工具把指定文本源拆成单元（默认按行）逐个单独发给上游，
// 直接报出被拦截的单元。
//
// 用法：
//
//	go run ./cmd/probe -body request.json -url http://127.0.0.1:8080/v1/messages -key <api_key>
//
// 输入是 Anthropic Messages 形状的请求体（客户端抓到的 body，例如 axonhub 导出的
// request-body-*.json）。先用整份 body 探一次作为基线：基线非 200 时工具会提示结果
// 可能被污染；随后每个单元都会替换掉 -target 指定的字段单独发送。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// unit 是一个待探测的文本单元及其来源位置。
type unit struct {
	// Source 描述单元来自哪里，例如 system、messages[3].tool_result。
	Source string `json:"source"`
	// Index 是单元在该来源内的顺序号，从 1 开始。
	Index int `json:"index"`
	// Text 是单元的完整文本。
	Text string `json:"text"`
}

// result 是一次探测的结果。
type result struct {
	// Unit 是被探测的单元。
	Unit unit `json:"unit"`
	// Status 是上游返回的 HTTP 状态码；0 表示请求本身失败。
	Status int `json:"status"`
	// Message 是错误响应里的 error.message（若有）。
	Message string `json:"message,omitempty"`
	// Error 是网络层错误。
	Error string `json:"error,omitempty"`
}

// blocked 表示该单元被上游拦截（非 200）。
func (r result) blocked() bool { return r.Status != http.StatusOK }

// report 是整次探测的完整报告。
type report struct {
	URL      string   `json:"url"`
	Target   string   `json:"target"`
	Unit     string   `json:"unit"`
	Baseline result   `json:"baseline"`
	Units    []result `json:"units"`
}

// headerList 支持重复传入 -header "K: V"。
type headerList []string

func (h *headerList) String() string { return strings.Join(*h, ", ") }

func (h *headerList) Set(value string) error {
	*h = append(*h, value)
	return nil
}

// options 保存命令行参数。
type options struct {
	bodyPath    string
	url         string
	apiKey      string
	target      string
	unit        string
	minLength   int
	concurrency int
	timeout     time.Duration
	maxTokens   int
	outPath     string
	headers     headerList
}

func main() {
	var opts options
	flag.StringVar(&opts.bodyPath, "body", "", "请求体 JSON 路径（必填）")
	flag.StringVar(&opts.url, "url", "", "上游 /v1/messages 地址（必填）")
	flag.StringVar(&opts.apiKey, "key", "", "x-api-key 头；留空则不发送")
	flag.StringVar(&opts.target, "target", "system", "探测的文本源：system | messages | tools")
	flag.StringVar(&opts.unit, "unit", "line", "拆分粒度：line | paragraph")
	flag.IntVar(&opts.minLength, "min-len", 16, "跳过短于该长度的单元")
	flag.IntVar(&opts.concurrency, "c", 1, "并发探测数")
	flag.DurationVar(&opts.timeout, "timeout", 60*time.Second, "单次探测超时")
	flag.IntVar(&opts.maxTokens, "max-tokens", 64, "探测请求的 max_tokens（越小越快）")
	flag.StringVar(&opts.outPath, "out", "", "可选：把完整报告写入该 JSON 文件")
	flag.Var(&opts.headers, "header", "附加请求头，可重复：-header \"K: V\"")
	flag.Parse()

	if opts.bodyPath == "" || opts.url == "" {
		flag.Usage()
		os.Exit(2)
	}

	raw, err := os.ReadFile(opts.bodyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取请求体失败: %v\n", err)
		os.Exit(1)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		fmt.Fprintf(os.Stderr, "解析请求体失败: %v\n", err)
		os.Exit(1)
	}

	found, err := run(context.Background(), opts, body, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "探测失败: %v\n", err)
		os.Exit(1)
	}
	if found {
		os.Exit(1)
	}
}

// run 执行整次探测，返回是否存在被拦截的单元。
func run(ctx context.Context, opts options, body map[string]any, out io.Writer) (bool, error) {
	units := collectUnits(body, opts.target, opts.unit, opts.minLength)
	if len(units) == 0 {
		return false, fmt.Errorf("-target %s 中没有可探测的文本单元", opts.target)
	}

	client := &http.Client{Timeout: opts.timeout}
	headers := buildHeaders(opts)

	fmt.Fprintf(out, "上游: %s\n目标: %s  粒度: %s  单元数: %d\n\n", opts.url, opts.target, opts.unit, len(units))

	baseline := probe(ctx, client, opts.url, headers, buildProbeBody(body, "", "", opts.maxTokens))
	fmt.Fprintf(out, "基线（完整请求体）: %s\n", describe(baseline))
	if baseline.blocked() {
		fmt.Fprintf(out, "警告: 基线已被拦截，逐单元结果可能被污染，建议先按单元探测 system。\n")
	}
	fmt.Fprintln(out)

	results := probeAll(ctx, client, opts.url, headers, body, units, opts)

	fmt.Fprintln(out, "逐单元结果:")
	blockedCount := 0
	for _, item := range results {
		status := "ok"
		if item.blocked() {
			status = "BLOCKED"
			blockedCount++
		}
		fmt.Fprintf(out, "  %-8s [%-6s #%d] %s\n", status, item.Unit.Source, item.Unit.Index, head(item.Unit.Text, 90))
		if item.blocked() && item.Message != "" {
			fmt.Fprintf(out, "            ↳ %s\n", item.Message)
		}
	}

	report := report{URL: opts.url, Target: opts.target, Unit: opts.unit, Baseline: baseline, Units: results}
	if opts.outPath != "" {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return blockedCount > 0, err
		}
		if err := os.WriteFile(opts.outPath, encoded, 0o644); err != nil {
			return blockedCount > 0, err
		}
		fmt.Fprintf(out, "\n报告已写入 %s\n", opts.outPath)
	}

	fmt.Fprintf(out, "\n被拦截单元: %d/%d\n", blockedCount, len(results))
	return blockedCount > 0, nil
}

// probeAll 按并发度依次探测所有单元，返回与输入顺序一致的稳定结果。
func probeAll(ctx context.Context, client *http.Client, url string, headers http.Header, body map[string]any, units []unit, opts options) []result {
	results := make([]result, len(units))
	concurrency := opts.concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	var wait sync.WaitGroup
	semaphore := make(chan struct{}, concurrency)
	for index, item := range units {
		wait.Add(1)
		go func(index int, item unit) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			results[index] = probe(ctx, client, url, headers, buildProbeBody(body, opts.target, item.Text, opts.maxTokens))
			results[index].Unit = item
		}(index, item)
	}
	wait.Wait()
	return results
}

// probe 发送一次探测请求，只保留状态码与错误文案。
func probe(ctx context.Context, client *http.Client, url string, headers http.Header, body map[string]any) result {
	encoded, err := json.Marshal(body)
	if err != nil {
		return result{Error: err.Error()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return result{Error: err.Error()}
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return result{Error: err.Error()}
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	return result{Status: response.StatusCode, Message: errorMessage(payload)}
}

// buildProbeBody 复制请求体并把目标字段替换成单个单元，强制非流式与小 max_tokens。
func buildProbeBody(base map[string]any, target string, text string, maxTokens int) map[string]any {
	probe := make(map[string]any, len(base))
	for key, value := range base {
		probe[key] = value
	}
	if text != "" {
		switch target {
		case "messages":
			probe["messages"] = []any{map[string]any{"role": "user", "content": text}}
		case "tools":
			delete(probe, "tools")
			probe["system"] = text
		default:
			probe["system"] = text
		}
	}
	probe["stream"] = false
	probe["max_tokens"] = maxTokens
	return probe
}

// buildHeaders 组装请求头：默认 anthropic-version + x-api-key，再叠加 -header。
func buildHeaders(opts options) http.Header {
	headers := http.Header{}
	headers.Set("content-type", "application/json")
	headers.Set("anthropic-version", "2023-06-01")
	if opts.apiKey != "" {
		headers.Set("x-api-key", opts.apiKey)
	}
	for _, raw := range opts.headers {
		name, value, found := strings.Cut(raw, ":")
		if !found {
			continue
		}
		headers.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	return headers
}

// collectUnits 按目标字段与粒度收集待探测单元。
func collectUnits(body map[string]any, target string, unitKind string, minLength int) []unit {
	var units []unit
	appendText := func(source string, index int, text string) {
		for _, piece := range splitUnits(text, unitKind) {
			if len([]rune(piece)) < minLength {
				continue
			}
			units = append(units, unit{Source: source, Index: index, Text: piece})
			index++
		}
	}

	switch target {
	case "messages":
		messages, _ := body["messages"].([]any)
		for index, raw := range messages {
			message, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			content := message["content"]
			if text, ok := content.(string); ok {
				appendText(fmt.Sprintf("messages[%d]", index), 1, text)
				continue
			}
			for blockIndex, rawBlock := range toBlocks(content) {
				block, ok := rawBlock.(map[string]any)
				if !ok {
					continue
				}
				kind, _ := block["type"].(string)
				label := fmt.Sprintf("messages[%d].%s", index, kind)
				switch kind {
				case "text":
					appendText(label, blockIndex+1, asString(block["text"]))
				case "thinking":
					appendText(label, blockIndex+1, asString(block["thinking"]))
				case "tool_result":
					appendText(label, blockIndex+1, toolResultText(block["content"]))
				}
			}
		}
	case "tools":
		tools, _ := body["tools"].([]any)
		for index, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := tool["name"].(string)
			appendText(fmt.Sprintf("tools[%d] %s", index, name), 1, asString(tool["description"]))
		}
	default:
		appendText("system", 1, systemText(body["system"]))
	}
	return units
}

// splitUnits 按行或空行段落拆分文本。
func splitUnits(text string, unitKind string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if unitKind == "paragraph" {
		parts := strings.Split(text, "\n\n")
		trimmed := make([]string, 0, len(parts))
		for _, part := range parts {
			if value := strings.TrimSpace(part); value != "" {
				trimmed = append(trimmed, value)
			}
		}
		return trimmed
	}
	lines := strings.Split(text, "\n")
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		if value := strings.TrimSpace(line); value != "" {
			trimmed = append(trimmed, value)
		}
	}
	return trimmed
}

// systemText 兼容 system 字段的字符串与文本块数组两种形态。
func systemText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var builder strings.Builder
	for _, rawBlock := range toBlocks(value) {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] != "text" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(asString(block["text"]))
	}
	return builder.String()
}

// toolResultText 兼容 tool_result 的字符串与文本块数组两种内容形态。
func toolResultText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	var builder strings.Builder
	for _, rawBlock := range toBlocks(value) {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			continue
		}
		if text := asString(block["text"]); text != "" {
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func toBlocks(value any) []any {
	blocks, _ := value.([]any)
	return blocks
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

// errorMessage 从错误响应中取出 error.message。
func errorMessage(payload []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return ""
	}
	return parsed.Error.Message
}

func describe(item result) string {
	if item.Error != "" {
		return "请求失败: " + item.Error
	}
	if item.Message != "" {
		return fmt.Sprintf("%d %s", item.Status, item.Message)
	}
	return fmt.Sprintf("%d", item.Status)
}

func head(text string, size int) string {
	runes := []rune(text)
	if len(runes) <= size {
		return string(runes)
	}
	return string(runes[:size]) + "…"
}
