// 本文件验证模型目录过滤与上限帽盖逻辑。
package adapter

import "testing"

// TestFilterFreeOnly 验证 free_only 过滤只保留 free 层级模型。
func TestFilterFreeOnly(t *testing.T) {
	models := []ModelInfo{
		{ID: "free-model", CostTier: ModelCostTierFree, MaxTokens: 64000, ThinkingEffort: "low"},
		{ID: "low-model", CostTier: ModelCostTierLow},
		{ID: "unknown-tier", CostTier: ""},
		{ID: "high-model", CostTier: ModelCostTierHigh},
	}

	freeOnly := FilterFreeOnly(models, true)
	if len(freeOnly) != 1 || freeOnly[0].ID != "free-model" {
		t.Fatalf("free-only filtered = %#v, want only free-model", freeOnly)
	}

	all := FilterFreeOnly(models, false)
	if len(all) != len(models) {
		t.Fatalf("unfiltered = %#v, want all %d models", all, len(models))
	}
}

// TestModelPolicyApplyIntersectsAndCaps 验证白名单/黑名单与 per-model 上限帽盖。
func TestModelPolicyApplyIntersectsAndCaps(t *testing.T) {
	models := []ModelInfo{
		{ID: "glm-5-2", CostTier: ModelCostTierFree, MaxTokens: 128000, ThinkingEffort: "high"},
		{ID: "swe-1-7", CostTier: ModelCostTierFree, MaxTokens: 32000, ThinkingEffort: "low"},
		{ID: "gpt-4o", CostTier: ModelCostTierHigh, MaxTokens: 128000, ThinkingEffort: "high"},
	}
	policy := ModelPolicy{
		FreeOnly:          true,
		Allowed:           []string{"glm-5-2", "gpt-4o"},
		Blocked:           []string{"swe-1-7"},
		MaxContextTokens:  64000,
		MaxThinkingEffort: "low",
	}
	got := policy.Apply(models)
	if len(got) != 1 || got[0].ID != "glm-5-2" {
		t.Fatalf("Apply = %#v, want only glm-5-2", got)
	}
	if got[0].MaxTokens != 64000 {
		t.Fatalf("MaxTokens = %d, want min(128000, 64000)=64000", got[0].MaxTokens)
	}
	if got[0].ThinkingEffort != "low" {
		t.Fatalf("ThinkingEffort = %q, want low", got[0].ThinkingEffort)
	}
}

// TestRequestMaxTokensUsesServerThenCap 验证发往上游的 MaxTokens 取自模型目录。
func TestRequestMaxTokensUsesServerThenCap(t *testing.T) {
	policy := ModelPolicy{FreeOnly: true, MaxContextTokens: 0}
	info := ModelInfo{ID: "glm-5-2", CostTier: ModelCostTierFree, MaxTokens: 32000}
	if got := policy.RequestMaxTokens(info, true); got != 32000 {
		t.Fatalf("RequestMaxTokens = %d, want 32000 from server", got)
	}

	policy.MaxContextTokens = 16000
	if got := policy.RequestMaxTokens(info, true); got != 16000 {
		t.Fatalf("RequestMaxTokens = %d, want 16000 cap", got)
	}

	if got := policy.RequestMaxTokens(ModelInfo{}, false); got != 16000 {
		t.Fatalf("missing catalog with cap = %d, want 16000", got)
	}

	open := ModelPolicy{}
	if got := open.RequestMaxTokens(ModelInfo{}, false); got != DefaultMaxTokens {
		t.Fatalf("unrestricted missing catalog = %d, want default %d", got, DefaultMaxTokens)
	}

	if got := (ModelPolicy{FreeOnly: true}).RequestMaxTokens(ModelInfo{}, false); got != 0 {
		t.Fatalf("free_only missing catalog = %d, want 0 (omit)", got)
	}
}
