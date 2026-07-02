package agent

import (
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
)

func testChatTool(name string) chat.Tool {
	return chat.Tool{
		Type: "function",
		Function: chat.FunctionDef{
			Name: name,
		},
	}
}

func TestAllowModelParallelToolCalls(t *testing.T) {
	tests := []struct {
		name  string
		tools []chat.Tool
		want  bool
	}{
		{
			name: "data analysis toolset disables model parallel tool calls",
			tools: []chat.Tool{
				testChatTool(agenttools.ToolDataSchema),
				testChatTool(agenttools.ToolDataAnalysis),
			},
			want: false,
		},
		{
			name: "general rag toolset keeps historical default",
			tools: []chat.Tool{
				testChatTool(agenttools.ToolKnowledgeSearch),
				testChatTool(agenttools.ToolGrepChunks),
			},
			want: true,
		},
		{
			name: "mixed data analysis and other tools keeps historical default",
			tools: []chat.Tool{
				testChatTool(agenttools.ToolDataSchema),
				testChatTool(agenttools.ToolDataAnalysis),
				testChatTool(agenttools.ToolKnowledgeSearch),
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := allowModelParallelToolCalls(tt.tools); got != tt.want {
				t.Fatalf("allowModelParallelToolCalls() = %v, want %v", got, tt.want)
			}
		})
	}
}
