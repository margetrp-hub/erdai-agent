package main

import (
	"net/http"
	"sort"
)

// providerDriver describes the protocol contract, not a vendor account.
// Connections and model endpoints remain user-owned data in SQLite.
type providerDriver struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Description    string   `json:"description"`
	Capabilities   []string `json:"capabilities"`
	ExecutionKinds []string `json:"executionKinds"`
	ProbePath      string   `json:"probePath"`
}

func providerDriverCatalog() []providerDriver {
	values := []providerDriver{
		{ID: "openai_chat_completion", Label: "OpenAI Chat Completions", Description: "兼容 /chat/completions 的文本、视觉和工具调用", Capabilities: []string{"chat", "vision", "tool_calling", "json_output"}, ExecutionKinds: []string{"llm", "tool"}, ProbePath: "/chat/completions"},
		{ID: "xai_responses", Label: "xAI Responses", Description: "兼容 /responses，可挂 web_search 等原生工具", Capabilities: []string{"chat", "web_search", "tool_calling"}, ExecutionKinds: []string{"llm", "tool"}, ProbePath: "/models"},
		{ID: "openai_compatible", Label: "OpenAI Compatible", Description: "通用 OpenAI 风格网关，也可承载图像和视频端点", Capabilities: []string{"chat", "vision", "tool_calling", "image_generation", "video_generation"}, ExecutionKinds: []string{"llm", "tool", "media"}, ProbePath: "/models"},
		{ID: "openai_embeddings", Label: "OpenAI Embeddings", Description: "Embedding 向量服务", Capabilities: []string{"embedding"}, ExecutionKinds: []string{"tool"}, ProbePath: "/embeddings"},
		{ID: "openai_chat_rerank", Label: "OpenAI Chat Rerank", Description: "用聊天协议完成候选重排", Capabilities: []string{"rerank"}, ExecutionKinds: []string{"tool"}, ProbePath: "/chat/completions"},
		{ID: "cohere_rerank", Label: "Cohere Rerank", Description: "Cohere 风格 rerank 接口", Capabilities: []string{"rerank"}, ExecutionKinds: []string{"tool"}, ProbePath: "/rerank"},
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values
}

func (s *coreConfigStore) handleProviderDrivers(w http.ResponseWriter, r *http.Request, path string) error {
	if path != "/api/v1/provider-drivers" || r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	mgmtWriteData(w, http.StatusOK, providerDriverCatalog())
	return nil
}
