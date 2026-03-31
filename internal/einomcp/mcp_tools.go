package einomcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/agent"
	"cyberstrike-ai/internal/security"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
)

// ExecutionRecorder 可选，在 MCP 工具成功返回且带有 execution id 时回调（用于汇总 mcpExecutionIds）。
type ExecutionRecorder func(executionID string)

// ToolErrorPrefix 用于把内部 MCP 执行结果中的 IsError 标记传递到多代理上层。
// Eino 工具通道目前只支持返回字符串，因此通过前缀标识，随后在多代理 runner 中解析为 success/isError。
const ToolErrorPrefix = "__CYBERSTRIKE_AI_TOOL_ERROR__\n"

// ToolsFromDefinitions 将单 Agent 使用的 OpenAI 风格工具定义转为 Eino InvokableTool，执行时走 Agent 的 MCP 路径。
func ToolsFromDefinitions(
	ag *agent.Agent,
	holder *ConversationHolder,
	defs []agent.Tool,
	rec ExecutionRecorder,
	toolOutputChunk func(toolName, toolCallID, chunk string),
) ([]tool.BaseTool, error) {
	out := make([]tool.BaseTool, 0, len(defs))
	for _, d := range defs {
		if d.Type != "function" || d.Function.Name == "" {
			continue
		}
		info, err := toolInfoFromDefinition(d)
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", d.Function.Name, err)
		}
		out = append(out, &mcpBridgeTool{
			info:   info,
			name:   d.Function.Name,
			agent:  ag,
			holder: holder,
			record: rec,
			chunk:  toolOutputChunk,
		})
	}
	return out, nil
}

func toolInfoFromDefinition(d agent.Tool) (*schema.ToolInfo, error) {
	fn := d.Function
	raw, err := json.Marshal(fn.Parameters)
	if err != nil {
		return nil, err
	}
	var js jsonschema.Schema
	if len(raw) > 0 && string(raw) != "null" && string(raw) != "{}" {
		if err := json.Unmarshal(raw, &js); err != nil {
			return nil, err
		}
	}
	if js.Type == "" {
		js.Type = string(schema.Object)
	}
	if js.Properties == nil && js.Type == string(schema.Object) {
		// 空参数对象
	}
	return &schema.ToolInfo{
		Name:        fn.Name,
		Desc:        fn.Description,
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&js),
	}, nil
}

type mcpBridgeTool struct {
	info   *schema.ToolInfo
	name   string
	agent  *agent.Agent
	holder *ConversationHolder
	record ExecutionRecorder
	chunk  func(toolName, toolCallID, chunk string)
}

func (m *mcpBridgeTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	_ = ctx
	return m.info, nil
}

func (m *mcpBridgeTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	_ = opts
	var args map[string]interface{}
	if argumentsInJSON != "" && argumentsInJSON != "null" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", fmt.Errorf("invalid tool arguments JSON: %w", err)
		}
	}
	if args == nil {
		args = map[string]interface{}{}
	}

	// Stream tool output (stdout/stderr) to upper layer via security.Executor's callback.
	// This enables multi-agent mode to show execution progress on the frontend.
	if m.chunk != nil {
		toolCallID := compose.GetToolCallID(ctx)
		if toolCallID != "" {
			if existing, ok := ctx.Value(security.ToolOutputCallbackCtxKey).(security.ToolOutputCallback); ok && existing != nil {
				// Chain existing callback (if any) + our progress forwarder.
				ctx = context.WithValue(ctx, security.ToolOutputCallbackCtxKey, security.ToolOutputCallback(func(c string) {
					existing(c)
					if strings.TrimSpace(c) == "" {
						return
					}
					m.chunk(m.name, toolCallID, c)
				}))
			} else {
				ctx = context.WithValue(ctx, security.ToolOutputCallbackCtxKey, security.ToolOutputCallback(func(c string) {
					if strings.TrimSpace(c) == "" {
						return
					}
					m.chunk(m.name, toolCallID, c)
				}))
			}
		}
	}

	conv := m.holder.Get()
	res, err := m.agent.ExecuteMCPToolForConversation(ctx, conv, m.name, args)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", nil
	}
	if res.ExecutionID != "" && m.record != nil {
		m.record(res.ExecutionID)
	}
	if res.IsError {
		return ToolErrorPrefix + res.Result, nil
	}
	return res.Result, nil
}
