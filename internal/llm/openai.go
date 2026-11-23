package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAIConfig 描述一个兼容 OpenAI Responses API 的端点。
type OpenAIConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

// OpenAI 是基于 Responses API 的 Client 实现。
// 对话与摘要都使用非流式请求；每轮由本地上下文决定模型看到什么。
type OpenAI struct {
	client openai.Client
	model  string
}

func NewOpenAI(cfg OpenAIConfig) *OpenAI {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithMaxRetries(2),
	}
	if base := strings.TrimRight(cfg.BaseURL, "/"); base != "" {
		opts = append(opts, option.WithBaseURL(base))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(cfg.Timeout))
	}
	return &OpenAI{client: openai.NewClient(opts...), model: cfg.Model}
}

// Complete 发起一次非流式模型请求，并聚合思考、正文与工具调用。
func (o *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	out, err := o.client.Responses.New(ctx, o.params(req))
	if err != nil {
		return nil, fmt.Errorf("模型请求失败: %w", err)
	}
	return parseResponse(out), nil
}

// parseResponse 把 Responses API 的 output items 转成内部结构：
// reasoning 汇总为 Reasoning（只用于展示），正文交给 SDK 的 OutputText，
// function_call 转成 ToolCalls。
func parseResponse(out *responses.Response) *Response {
	resp := &Response{}
	var thoughts []string

	for _, item := range out.Output {
		switch item.Type {
		case string(ItemReasoning):
			reasoning := item.AsReasoning()
			// 多数模型只给 summary；部分模型把原文放在 content。
			if len(reasoning.Summary) > 0 {
				for _, part := range reasoning.Summary {
					if part.Text != "" {
						thoughts = append(thoughts, part.Text)
					}
				}
				continue
			}
			for _, part := range reasoning.Content {
				if part.Text != "" {
					thoughts = append(thoughts, part.Text)
				}
			}

		case string(ItemFunctionCall):
			call := item.AsFunctionCall()
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:        call.CallID,
				Name:      call.Name,
				Arguments: call.Arguments,
			})
		}
	}

	resp.Content = out.OutputText()
	resp.Reasoning = strings.Join(thoughts, "\n")
	return resp
}

func (o *OpenAI) params(req Request) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(o.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: inputItems(req.Items),
		},
		// 上下文完全由本地维护，不使用服务端存储，避免无法压缩。
		Store: param.NewOpt(false),
	}
	if len(req.Tools) > 0 {
		tools := make([]responses.ToolUnionParam, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, responses.ToolUnionParam{
				OfFunction: &responses.FunctionToolParam{
					Name:        tool.Name,
					Description: param.NewOpt(tool.Description),
					Parameters:  tool.Parameters,
				},
			})
		}
		params.Tools = tools
	}
	return params
}

func inputItems(items []Item) responses.ResponseInputParam {
	out := make(responses.ResponseInputParam, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case ItemMessage:
			out = append(out, responses.ResponseInputItemParamOfMessage(item.Text, inputRole(item.Role)))
		case ItemFunctionCall:
			out = append(out, responses.ResponseInputItemUnionParam{
				OfFunctionCall: &responses.ResponseFunctionToolCallParam{
					CallID:    item.CallID,
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			})
		case ItemFunctionCallOutput:
			out = append(out, responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: openai.String(item.CallID),
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						OfString: openai.String(item.Output),
					},
				},
			})
		}
		// reasoning 只用于展示，不回传给模型。
	}
	return out
}

func inputRole(role Role) responses.EasyInputMessageRole {
	switch role {
	case RoleSystem:
		return responses.EasyInputMessageRoleSystem
	case RoleAssistant:
		return responses.EasyInputMessageRoleAssistant
	default:
		return responses.EasyInputMessageRoleUser
	}
}
