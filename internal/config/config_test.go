// 本文件验证配置未知字段拒绝和时间字段解析行为。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadRejectsUnknownFields 验证未知配置字段会被严格拒绝。
func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: ':8080'\n  typo: true\n")
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want unknown field error")
	}
}

// TestLoadParsesListenAddress 验证服务监听地址来自 YAML 配置。
func TestLoadParsesListenAddress(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: ':9090'\n")
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.Listen != ":9090" {
		t.Fatalf("Listen = %q, want :9090", config.Server.Listen)
	}
}

// TestLoadDisablesDebugLoggingByDefault 的测试动机是保证生产配置未显式开启时不会写入请求内容。
func TestLoadDisablesDebugLoggingByDefault(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: ':9090'\n")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Debug.Enabled {
		t.Fatal("Debug.Enabled = true, want disabled by default")
	}
}

// TestLoadParsesAuthAPIKey 验证可选的 API Key 可从配置中读取。
func TestLoadParsesAuthAPIKey(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: ':9090'\nauth:\n  api_key: 'my-secret-key'\n")
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Auth.APIKey != "my-secret-key" {
		t.Fatalf("Auth.APIKey = %q, want my-secret-key", config.Auth.APIKey)
	}
}

// TestLoadEnablesDebugLoggingExplicitly 的测试动机是保留排查协议问题时主动开启日志的能力。
func TestLoadEnablesDebugLoggingExplicitly(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: ':9090'\ndebug:\n  enabled: true\n")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Debug.Enabled {
		t.Fatal("Debug.Enabled = false, want explicitly enabled")
	}
}

// TestLoadParsesModelsConfig 验证 models 段的全部已知字段均可解析。
func TestLoadParsesModelsConfig(t *testing.T) {
	path := writeConfig(t, `server:
  listen: ':9090'
models:
  free_only: true
  allowed_models:
    - glm-5-2
    - swe-1-7
  blocked_models:
    - gpt-4o
  max_context_tokens: 64000
  max_thinking_effort: "low"
`)
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Models.FreeOnly {
		t.Fatal("FreeOnly = false, want true")
	}
	if got := loaded.Models.AllowedModels; len(got) != 2 || got[0] != "glm-5-2" || got[1] != "swe-1-7" {
		t.Fatalf("AllowedModels = %#v", got)
	}
	if got := loaded.Models.BlockedModels; len(got) != 1 || got[0] != "gpt-4o" {
		t.Fatalf("BlockedModels = %#v", got)
	}
	if loaded.Models.MaxContextTokens != 64000 {
		t.Fatalf("MaxContextTokens = %d, want 64000", loaded.Models.MaxContextTokens)
	}
	if loaded.Models.MaxThinkingEffort != "low" {
		t.Fatalf("MaxThinkingEffort = %q, want low", loaded.Models.MaxThinkingEffort)
	}
	if !loaded.Models.RestrictsAccess() {
		t.Fatal("RestrictsAccess = false, want true")
	}
}

// TestLoadAcceptsProductionModelsYAML 验证线上正在使用的 models 段不会因未知字段崩溃。
func TestLoadAcceptsProductionModelsYAML(t *testing.T) {
	path := writeConfig(t, `server:
  listen: ":28180"
models:
  free_only: true
  max_context_tokens: 0
  max_thinking_effort: ""
`)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for production models yaml", err)
	}
	if !loaded.Models.FreeOnly {
		t.Fatal("FreeOnly = false, want true")
	}
	if loaded.Models.MaxContextTokens != 0 {
		t.Fatalf("MaxContextTokens = %d, want 0", loaded.Models.MaxContextTokens)
	}
	if loaded.Models.MaxThinkingEffort != "" {
		t.Fatalf("MaxThinkingEffort = %q, want empty", loaded.Models.MaxThinkingEffort)
	}
}

// TestModelsConfigDefaults 验证未配置 models 段时的默认行为：不限制。
func TestModelsConfigDefaults(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: ':9090'\n")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Models.FreeOnly || loaded.Models.RestrictsAccess() {
		t.Fatal("models policy should be unrestricted by default")
	}
	if loaded.Models.MaxContextTokens != 0 || loaded.Models.MaxThinkingEffort != "" {
		t.Fatalf("default caps = %+v, want empty", loaded.Models)
	}
}

// TestLoadModelsEnvOverrides 验证环境变量可覆盖 models 段。
func TestLoadModelsEnvOverrides(t *testing.T) {
	path := writeConfig(t, `server:
  listen: ':9090'
models:
  free_only: false
  max_context_tokens: 0
  max_thinking_effort: ""
`)
	t.Setenv("DEVIN_MODELS_FREE_ONLY", "true")
	t.Setenv("DEVIN_MODELS_ALLOWED", "glm-5-2, swe-1-7")
	t.Setenv("DEVIN_MODELS_BLOCKED", "gpt-4o")
	t.Setenv("DEVIN_MODELS_MAX_CONTEXT_TOKENS", "32000")
	t.Setenv("DEVIN_MODELS_MAX_THINKING_EFFORT", "LOW")
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Models.FreeOnly {
		t.Fatal("FreeOnly = false, want env override true")
	}
	if got := loaded.Models.AllowedModels; len(got) != 2 || got[0] != "glm-5-2" || got[1] != "swe-1-7" {
		t.Fatalf("AllowedModels = %#v", got)
	}
	if got := loaded.Models.BlockedModels; len(got) != 1 || got[0] != "gpt-4o" {
		t.Fatalf("BlockedModels = %#v", got)
	}
	if loaded.Models.MaxContextTokens != 32000 {
		t.Fatalf("MaxContextTokens = %d, want 32000", loaded.Models.MaxContextTokens)
	}
	if loaded.Models.MaxThinkingEffort != "low" {
		t.Fatalf("MaxThinkingEffort = %q, want low", loaded.Models.MaxThinkingEffort)
	}
}

// TestLoadExampleYAML 验证仓库示例配置可被严格解码。
func TestLoadExampleYAML(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skip(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("load config.example.yaml: %v", err)
	}
}
