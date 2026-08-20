// 本文件定义供应商适配器与中间 LLM 模型之间的边界。
//
// Package adapter 定义供应商适配器与中间 LLM 模型之间的边界。
package adapter

import (
	"context"

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
}

// Adapter 将供应商无关的请求上下文转换为具体供应商调用，并返回有序响应流。
type Adapter interface {
	// Stream 开始一次或多次助手响应的流式生成。
	Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error)
	// ListModels 返回当前账号可用的模型目录；失败时返回错误。
	ListModels(context.Context) ([]ModelInfo, error)
}
