// Package llm 定义与具体协议无关的模型调用边界。
// 当前实现基于 OpenAI Responses API；Agent 只依赖本文件的 item 模型。
package llm

import "context"

// Role 是消息的说话人，只用于 ItemMessage。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ItemType 对应 Responses API 的 input/output item 类型。
type ItemType string

const (
	// ItemMessage 是普通文本消息，Role 决定是 system/user/assistant。
	ItemMessage ItemType = "message"
	// ItemFunctionCall 是模型请求执行工具。
	ItemFunctionCall ItemType = "function_call"
	// ItemFunctionCallOutput 是工具执行结果，通过 CallID 与调用配对。
	ItemFunctionCallOutput ItemType = "function_call_output"
	// ItemReasoning 是模型的思考摘要，只用于展示，不回传给模型。
	ItemReasoning ItemType = "reasoning"
)

// Item 是会话里的最小单元，也是发给模型的 input item。
// 只保留本项目需要的字段，避免业务层直接依赖 SDK 类型。
type Item struct {
	Type      ItemType `json:"type"`
	Role      Role     `json:"role,omitempty"`
	Text      string   `json:"text,omitempty"`
	CallID    string   `json:"call_id,omitempty"`
	Name      string   `json:"name,omitempty"`
	Arguments string   `json:"arguments,omitempty"`
	Output    string   `json:"output,omitempty"`
}

// Message 构造一条文本消息。
func Message(role Role, text string) Item {
	return Item{Type: ItemMessage, Role: role, Text: text}
}

func System(text string) Item    { return Message(RoleSystem, text) }
func User(text string) Item      { return Message(RoleUser, text) }
func Assistant(text string) Item { return Message(RoleAssistant, text) }

// FunctionCall 构造一次工具调用 item。
func FunctionCall(call ToolCall) Item {
	return Item{Type: ItemFunctionCall, CallID: call.ID, Name: call.Name, Arguments: call.Arguments}
}

// FunctionOutput 构造工具结果 item，CallID 必须与对应调用一致。
func FunctionOutput(callID, output string) Item {
	return Item{Type: ItemFunctionCallOutput, CallID: callID, Output: output}
}

// Tool 是模型可见的函数定义。
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall 是模型决定执行的一次工具调用。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Request 是一次模型调用的输入。
type Request struct {
	Items []Item
	Tools []Tool
}

// Response 是模型调用的结果。
// Content 是最终自然语言回答，Reasoning 是思考摘要（只用于展示），
// ToolCalls 是需要执行的工具。
type Response struct {
	Content   string
	Reasoning string
	ToolCalls []ToolCall
}

// Client 是模型调用端口；测试可以注入假实现。
type Client interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}
