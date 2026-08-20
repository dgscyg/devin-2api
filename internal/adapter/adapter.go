// 本文件定义供应商适配器与中间 LLM 模型之间的边界。
//
// Package adapter 定义供应商适配器与中间 LLM 模型之间的边界。
package adapter

import (
	"context"
	"strings"

	"github.com/dgscyg/devin-2api/internal/llm"
)

// ModelInfo 是对外暴露的模型目录条目（OpenAI /v1/models 形状）。
type ModelInfo struct {
	// ID 是模型标识（OpenAI model id / Devin model_uid）。
	ID string
	// Created 是目录条目的 Unix 秒时间戳；未知时可为 0。
	Created int64
	// OwnedBy 是模型归属方展示名。
	OwnedBy string
	// SupportsImages 表示该模型是否支持多模态图片输入；目录未知时为 false。
	SupportsImages bool
	// CostTier 是模型所属成本层级；空字符串表示上游未提供（不参与 free_only 过滤）。
	CostTier string
	// MaxTokens 是服务端返回的该模型上下文/输出 token 上限；0 表示未知。
	MaxTokens int32
	// ThinkingEffort 是服务端返回的该模型思考等级；空字符串表示未知。
	ThinkingEffort string
}

// ModelCostTier 定义模型成本层级常量。
const (
	// ModelCostTierFree 是免费成本层级。
	ModelCostTierFree = "free"
	// ModelCostTierLow 是低成本层级。
	ModelCostTierLow = "low"
	// ModelCostTierMedium 是中等成本层级。
	ModelCostTierMedium = "medium"
	// ModelCostTierHigh 是高成本层级。
	ModelCostTierHigh = "high"
)

// DefaultMaxTokens 是目录未提供 max_tokens 且未启用访问限制时的请求回退值。
const DefaultMaxTokens uint64 = 128000

// ModelPolicy 描述模型目录的访问控制与额外上限。
// 上下文和思考等级默认使用每个模型自己的服务端配置，下列字段只是额外帽盖。
type ModelPolicy struct {
	// FreeOnly 为 true 时仅允许 free 成本层级模型。
	FreeOnly bool
	// Allowed 是模型 UID 白名单；为空表示不限制。
	Allowed []string
	// Blocked 是模型 UID 黑名单。
	Blocked []string
	// MaxContextTokens 是对服务端 max_tokens 的额外上限；0 表示不覆盖。
	MaxContextTokens int
	// MaxThinkingEffort 是对服务端思考等级的额外上限；空表示不覆盖。
	MaxThinkingEffort string
}

// RestrictsAccess 表示该策略会拒绝部分模型。
func (policy ModelPolicy) RestrictsAccess() bool {
	return policy.FreeOnly || len(policy.Allowed) > 0 || len(policy.Blocked) > 0
}

// Allows 判断单个模型是否可通过访问控制。
func (policy ModelPolicy) Allows(model ModelInfo) bool {
	if policy.FreeOnly && model.CostTier != ModelCostTierFree {
		return false
	}
	if len(policy.Allowed) > 0 && !containsFold(policy.Allowed, model.ID) {
		return false
	}
	if containsFold(policy.Blocked, model.ID) {
		return false
	}
	return true
}

// Apply 过滤目录并写入每个模型的有效上限（服务端值与配置帽盖取更严者）。
func (policy ModelPolicy) Apply(models []ModelInfo) []ModelInfo {
	filtered := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		if !policy.Allows(model) {
			continue
		}
		model.MaxTokens = policy.CapMaxTokens(model.MaxTokens)
		model.ThinkingEffort = policy.CapThinkingEffort(model.ThinkingEffort)
		filtered = append(filtered, model)
	}
	return filtered
}

// CapMaxTokens 返回 min(服务端 max_tokens, 配置覆盖)；任一侧为 0 则使用另一侧。
func (policy ModelPolicy) CapMaxTokens(server int32) int32 {
	if policy.MaxContextTokens <= 0 {
		return server
	}
	capTokens := int32(policy.MaxContextTokens)
	if server <= 0 || capTokens < server {
		return capTokens
	}
	return server
}

// CapThinkingEffort 返回服务端思考等级与配置上限中更低的一侧。
func (policy ModelPolicy) CapThinkingEffort(server string) string {
	cap := strings.ToLower(strings.TrimSpace(policy.MaxThinkingEffort))
	server = strings.ToLower(strings.TrimSpace(server))
	if cap == "" {
		return server
	}
	if server == "" || thinkingRank(cap) < thinkingRank(server) {
		return cap
	}
	return server
}

// RequestMaxTokens 计算发往上游的 MaxTokens。
// 优先使用该模型服务端上限（再叠加配置帽盖）；未知时：
// 有访问限制则返回 0（省略字段，让服务端按模型默认），否则回退 DefaultMaxTokens。
func (policy ModelPolicy) RequestMaxTokens(info ModelInfo, found bool) uint64 {
	if found && info.MaxTokens > 0 {
		return uint64(policy.CapMaxTokens(info.MaxTokens))
	}
	if policy.MaxContextTokens > 0 {
		return uint64(policy.MaxContextTokens)
	}
	if policy.RestrictsAccess() {
		return 0
	}
	return DefaultMaxTokens
}

func thinkingRank(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "none", "off", "disable", "disabled":
		return 0
	case "minimal", "min", "low":
		return 1
	case "medium", "default", "normal":
		return 2
	case "high":
		return 3
	case "xhigh", "x-high", "max", "maximum":
		return 4
	default:
		return 2
	}
}

func containsFold(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

// FilterFreeOnly 按 free_only 策略过滤模型目录：
// 未启用时原样返回；启用时仅保留 free 层级模型。
func FilterFreeOnly(models []ModelInfo, freeOnly bool) []ModelInfo {
	return (ModelPolicy{FreeOnly: freeOnly}).Apply(models)
}

// Adapter 将供应商无关的请求上下文转换为具体供应商调用，并返回有序响应流。
type Adapter interface {
	// Stream 开始一次或多次助手响应的流式生成。
	Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error)
	// ListModels 返回当前账号可用的模型目录；失败时返回错误。
	ListModels(context.Context) ([]ModelInfo, error)
}
