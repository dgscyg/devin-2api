// 本文件提供尚未配置具体供应商时使用的适配器占位实现。
package adapter

import (
	"context"
	"errors"

	"github.com/dgscyg/devin-2api/internal/llm"
)

// Unavailable 是尚未配置具体供应商时使用的占位适配器。
type Unavailable struct {
	// Reason 是返回给调用方的未配置原因。
	Reason string
}

// Stream 返回未配置供应商适配器的错误。
func (unavailable Unavailable) Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error) {
	if unavailable.Reason == "" {
		return nil, errors.New("provider adapter is not implemented")
	}
	return nil, errors.New(unavailable.Reason)
}

// ListModels 返回未配置供应商适配器的错误。
func (unavailable Unavailable) ListModels(context.Context) ([]ModelInfo, error) {
	if unavailable.Reason == "" {
		return nil, errors.New("provider adapter is not implemented")
	}
	return nil, errors.New(unavailable.Reason)
}
