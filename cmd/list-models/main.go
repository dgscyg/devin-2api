// 本工具使用 Devin Connect API 获取可用模型列表。
// 用法: go run ./cmd/list-models -config config.yaml
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/dgscyg/devin-2api/internal/config"
	devinproto "local/devinproto"
	"local/devinproto/devinprotoconnect"

	"google.golang.org/protobuf/proto"
)

const (
	clientName    = "chisel"
	clientVersion = "3000.2.17"
)

func main() {
	configPath := flag.String("config", "config.yaml", "YAML 配置文件路径")
	flag.Parse()

	absoluteConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := config.Load(absoluteConfigPath)
	if err != nil {
		log.Fatal(err)
	}
	if cfg.Devin.Token == "" || cfg.Devin.BaseURL == "" {
		log.Fatal("devin.token and devin.base_url are required in config")
	}

	token := cfg.Devin.Token
	baseURL := cfg.Devin.BaseURL

	transport := &authTransport{base: http.DefaultTransport, token: token}
	client := devinprotoconnect.NewApiServerServiceClient(&http.Client{Transport: transport, Timeout: 30 * time.Second}, baseURL)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. GetCascadeModelConfigs — cascade (agent) 模型列表
	fmt.Println("=== Cascade Model Configs ===")
	cascadeResp, err := client.GetCascadeModelConfigs(ctx, connect.NewRequest(&devinproto.GetCascadeModelConfigsRequest{
		Metadata: buildMetadata(token),
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetCascadeModelConfigs error: %v\n", err)
	} else {
		printModelConfigs(cascadeResp.Msg.GetClientModelConfigs())
	}

	// 2. GetCliModelConfigs — CLI 模型列表
	fmt.Println("\n=== CLI Model Configs ===")
	cliResp, err := client.GetCliModelConfigs(ctx, connect.NewRequest(&devinproto.GetCliModelConfigsRequest{
		Metadata: buildMetadata(token),
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetCliModelConfigs error: %v\n", err)
	} else {
		printModelConfigs(cliResp.Msg.GetClientModelConfigs())
	}

	// 3. GetModelStatuses — 模型状态
	fmt.Println("\n=== Model Statuses ===")
	statusResp, err := client.GetModelStatuses(ctx, connect.NewRequest(&devinproto.GetModelStatusesRequest{
		Metadata: buildMetadata(token),
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetModelStatuses error: %v\n", err)
	} else {
		for _, s := range statusResp.Msg.GetModelStatusInfos() {
			uid := s.GetModelUid()
			if uid == "" {
				uid = fmt.Sprintf("model_enum_%d", s.GetModel())
			}
			fmt.Printf("  %s — status: %s\n", uid, s.GetStatus())
		}
	}

	// 4. GetModelProviders — 模型供应商
	fmt.Println("\n=== Model Providers ===")
	providerResp, err := client.GetModelProviders(ctx, connect.NewRequest(&devinproto.GetModelProvidersRequest{}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetModelProviders error: %v\n", err)
	} else {
		for _, p := range providerResp.Msg.GetModelProviders() {
			fmt.Printf("  provider=%d name=%s\n", p.GetProvider(), p.GetDisplayName())
		}
	}
}

func printModelConfigs(configs []*devinproto.ExaCodeiumCommonPb_ClientModelConfig) {
	if len(configs) == 0 {
		fmt.Println("  (none)")
		return
	}
	for i, c := range configs {
		uid := c.GetModelUid()
		label := c.GetLabel()
		if uid == "" && c.GetModelOrAlias() != nil {
			uid = c.GetModelOrAlias().GetModelUid()
		}
		disabled := ""
		if c.GetDisabled() {
			disabled = " [DISABLED]"
		}
		premium := ""
		if c.GetIsPremium() {
			premium = " [PREMIUM]"
		}
		beta := ""
		if c.GetIsBeta() {
			beta = " [BETA]"
		}
		img := ""
		if c.GetSupportsImages() {
			img = " [img]"
		}
		// 定价信息
		costTier := ""
		switch c.GetModelCostTier() {
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_FREE:
			costTier = " [FREE]"
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_LOW:
			costTier = " [LOW]"
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_MEDIUM:
			costTier = " [MEDIUM]"
		case devinproto.ExaCodeiumCommonPb_ModelCostTier_ExaCodeiumCommonPb_ModelCostTier_MODEL_COST_TIER_HIGH:
			costTier = " [HIGH]"
		}
		// 促销/优惠
		promo := ""
		if ps := c.GetPromoStatus(); ps != nil && ps.GetIsActive() {
			promo = " [PROMO]"
		}
		// credit 倍率
		credit := ""
		if cm := c.GetCreditMultiplier(); cm != 0 && cm != 1 {
			credit = fmt.Sprintf(" [x%.1f]", cm)
		}
		// fast 标记
		fast := ""
		if fs := c.GetFastStatus(); fs != nil && fs.GetIsActive() {
			fast = " [FAST]"
		}
		fmt.Printf("  %3d. %-40s %-30s%s%s%s%s%s%s%s%s\n", i+1, uid, label, costTier, promo, credit, fast, disabled, premium, beta, img)
	}
}

func buildMetadata(token string) *devinproto.ExaCodeiumCommonPb_Metadata {
	fingerprint, _ := randomHex(366)
	return &devinproto.ExaCodeiumCommonPb_Metadata{
		ApiKey:           proto.String(token),
		ExtensionName:    proto.String(clientName),
		ExtensionVersion: proto.String(clientVersion),
		IdeName:          proto.String(clientName),
		IdeVersion:       proto.String(clientVersion),
		Locale:           proto.String("en"),
		Os:               proto.String("win"),
		F:                proto.String(fingerprint),
	}
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Basic "+t.token+"-"+t.token)
	return t.base.RoundTrip(clone)
}

func randomHex(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
