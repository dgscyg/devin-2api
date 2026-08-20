// 本文件定义服务启动配置及其 YAML 加载和校验逻辑。
//
// Package config 负责加载和校验服务启动配置。
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config 保存服务启动所需的全部配置；服务运行期间不会热更新。
type Config struct {
	// Server 保存 HTTP 服务配置。
	Server ServerConfig `yaml:"server"`
	// Devin 保存 Devin Connect 上游配置。
	Devin DevinConfig `yaml:"devin"`
	// Debug 保存仅用于本地诊断的日志配置。
	Debug DebugConfig `yaml:"debug"`
	// Dashboard 保存管理面板配置。
	Dashboard DashboardConfig `yaml:"dashboard"`
	// Auth 保存对外 OpenAI 兼容接口的访问控制配置。
	Auth AuthConfig `yaml:"auth"`
	// Models 保存模型目录的访问控制配置。
	Models ModelsConfig `yaml:"models"`
}

// ServerConfig 保存 HTTP 服务监听配置。
type ServerConfig struct {
	// Listen 是 HTTP 服务监听地址。
	Listen string `yaml:"listen"`
	// MaxConcurrency 是同时处理的 /v1/* 请求数上限；0 表示使用默认值。
	MaxConcurrency int `yaml:"max_concurrency"`
}

// DevinConfig 保存 Devin Connect 上游调用配置。
type DevinConfig struct {
	// BaseURL 是 Devin Connect 服务的基础地址。
	BaseURL string `yaml:"base_url"`
	// Token 是 Devin session token；不会写入日志。
	Token string `yaml:"token"`
	// Model 是 Devin chat model UID。
	Model string `yaml:"model"`
	// Proxy 是可选的 HTTP/HTTPS/SOCKS5 代理地址；为空时直连或走系统环境变量。
	Proxy string `yaml:"proxy"`
	// ForceHTTP1 为 true 时强制使用 HTTP/1.1，每请求独立 TCP 连接，
	// 避免 HTTP/2 单连接多 stream 复用导致的上游并发瓶颈（首字延迟飙升/卡住）。
	// 行为对齐 Devin 客户端多窗口各自独立连接的模式。默认 true。
	ForceHTTP1 *bool `yaml:"force_http1"`
}

// DebugConfig 保存请求级调试日志配置。
type DebugConfig struct {
	// Enabled 表示是否在配置文件同目录的 logs 下写入请求调试日志。
	Enabled bool `yaml:"enabled"`
}

// DashboardConfig 保存管理面板配置。
type DashboardConfig struct {
	// Password 是面板访问密码；为空则不要求登录，直接进入面板。
	Password string `yaml:"password"`
}

// AuthConfig 保存对外 OpenAI 兼容接口的访问控制配置。
type AuthConfig struct {
	// APIKey 是客户端访问 /v1/* 接口所需的密钥；为空时不启用鉴权。
	APIKey string `yaml:"api_key"`
}

// ModelsConfig 保存模型目录的访问控制配置。
//
// 上下文 token 上限和思考等级默认使用服务端返回的每个模型自己的配置，
// 而不是在本地写死一份全局值；下列字段只是额外上限覆盖。
type ModelsConfig struct {
	// FreeOnly 为 true 时仅允许访问 free 成本层级的模型。
	FreeOnly bool `yaml:"free_only"`
	// AllowedModels 是允许访问的模型 UID 白名单；留空表示不限制。
	// 与 FreeOnly 叠加时取交集。
	AllowedModels []string `yaml:"allowed_models"`
	// BlockedModels 是禁止访问的模型 UID 黑名单。
	BlockedModels []string `yaml:"blocked_models"`
	// MaxContextTokens 是对服务端模型 max_tokens 的额外上限；0 表示使用服务端值。
	MaxContextTokens int `yaml:"max_context_tokens"`
	// MaxThinkingEffort 是对服务端模型思考等级的额外上限；留空表示使用服务端值。
	MaxThinkingEffort string `yaml:"max_thinking_effort"`
}

// RestrictsAccess 表示是否启用了会拒绝部分模型的访问控制。
func (config ModelsConfig) RestrictsAccess() bool {
	return config.FreeOnly || len(config.AllowedModels) > 0 || len(config.BlockedModels) > 0
}

// Load 从 YAML 文件读取并校验配置。
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config %q: %w", path, err)
	}
	defer file.Close()

	var config Config
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config %q: %w", path, err)
	}
	applyEnvOverrides(&config)
	if err := config.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return config, nil
}

// applyEnvOverrides 用环境变量覆盖 YAML 中的对应字段；环境变量优先级更高。
func applyEnvOverrides(config *Config) {
	if value, ok := os.LookupEnv("DEVIN_MODELS_FREE_ONLY"); ok {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			parsed = strings.EqualFold(strings.TrimSpace(value), "true")
		}
		config.Models.FreeOnly = parsed
	}
	if value, ok := os.LookupEnv("DEVIN_MODELS_ALLOWED"); ok {
		config.Models.AllowedModels = splitCSV(value)
	}
	if value, ok := os.LookupEnv("DEVIN_MODELS_BLOCKED"); ok {
		config.Models.BlockedModels = splitCSV(value)
	}
	if value, ok := os.LookupEnv("DEVIN_MODELS_MAX_CONTEXT_TOKENS"); ok {
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			config.Models.MaxContextTokens = parsed
		}
	}
	if value, ok := os.LookupEnv("DEVIN_MODELS_MAX_THINKING_EFFORT"); ok {
		config.Models.MaxThinkingEffort = strings.TrimSpace(value)
	}
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Validate 检查配置中的必填项，并设置默认值。
func (config *Config) Validate() error {
	if config.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	if config.Server.MaxConcurrency <= 0 {
		config.Server.MaxConcurrency = 1024
	}
	// ForceHTTP1 默认开启：HTTP/2 单连接多 stream 复用是并发首字延迟飙升的根因。
	if config.Devin.ForceHTTP1 == nil {
		force := true
		config.Devin.ForceHTTP1 = &force
	}
	if config.Models.MaxContextTokens < 0 {
		return errors.New("models.max_context_tokens must be >= 0")
	}
	config.Models.MaxThinkingEffort = strings.ToLower(strings.TrimSpace(config.Models.MaxThinkingEffort))
	return nil
}
