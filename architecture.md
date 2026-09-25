# Architecture

## 1. 总览

Min Code Agent 采用分层架构：

```text
┌──────────────────────────────┐
│             CLI              │
│ REPL / Commands / Inspector  │
└───────────────┬──────────────┘
                │
                ▼
┌──────────────────────────────┐
│         Agent Runtime        │
│                              │
│ Agent Loop                   │
│ Context Manager              │
│ Tool Dispatcher              │
│ Permission Manager           │
│ Session Manager              │
└───────┬────────┬─────────────┘
        │        │
        ▼        ▼
┌────────────┐ ┌──────────────┐
│ LLM        │ │ Tool System  │
│ Provider   │ │              │
└────────────┘ └──────────────┘

        所有核心模块
             │
             ▼
┌──────────────────────────────┐
│      Observation Layer       │
│                              │
│ Event Bus                    │
│ Trace Recorder               │
│ Context Inspector            │
│ Tool Inspector               │
│ Token Metrics                │
│ Timeline                     │
│ Replay                       │
└──────────────────────────────┘
```

## 2. 推荐目录结构

```text
mincode/
├── cmd/
│   └── mincode/
│       └── main.go
│
├── internal/
│   ├── agent/
│   │   ├── agent.go
│   │   ├── loop.go
│   │   ├── state.go
│   │   └── result.go
│   │
│   ├── llm/
│   │   ├── provider.go
│   │   ├── message.go
│   │   ├── request.go
│   │   ├── response.go
│   │   └── compatible.go
│   │
│   ├── tools/
│   │   ├── tool.go
│   │   ├── registry.go
│   │   ├── read_file.go
│   │   ├── write_file.go
│   │   ├── edit_file.go
│   │   ├── list_dir.go
│   │   ├── glob.go
│   │   ├── grep.go
│   │   └── shell.go
│   │
│   ├── context/
│   │   ├── manager.go
│   │   ├── builder.go
│   │   ├── budget.go
│   │   ├── compressor.go
│   │   └── snapshot.go
│   │
│   ├── prompt/
│   │   ├── system.go
│   │   ├── instructions.go
│   │   └── template.go
│   │
│   ├── permission/
│   │   ├── policy.go
│   │   └── approval.go
│   │
│   ├── session/
│   │   ├── session.go
│   │   ├── storage.go
│   │   └── history.go
│   │
│   ├── observability/
│   │   ├── event.go
│   │   ├── bus.go
│   │   ├── recorder.go
│   │   ├── metrics.go
│   │   └── replay.go
│   │
│   ├── inspector/
│   │   ├── timeline.go
│   │   ├── context.go
│   │   ├── tool.go
│   │   ├── llm.go
│   │   └── session.go
│   │
│   ├── skill/
│   │   ├── skill.go
│   │   └── loader.go
│   │
│   └── config/
│       ├── config.go
│       └── loader.go
│
├── prompts/
│   └── system.md
├── skills/
├── docs/
├── AGENTS.md
├── mincode.yaml
├── go.mod
└── README.md
```

## 3. Agent Runtime

Agent 是协调器，而不是能力实现者。

建议结构：

```go
type Agent struct {
    Provider   llm.Provider
    Tools      *tools.Registry
    Context    *context.Manager
    Permission permission.Policy
    Session    *session.Session
    Events     observability.Bus
}
```

Agent 不应该：

- 直接访问文件系统
- 直接运行 Shell
- 直接调用具体模型 API
- 直接保存 JSONL Trace
- 直接操作持久化格式

## 4. Agent 状态机

建议状态：

```text
IDLE
  ↓
BUILDING_CONTEXT
  ↓
CALLING_LLM
  ↓
PROCESSING_RESPONSE
  ↓
EXECUTING_TOOL
  ↓
WAITING_APPROVAL
  ↓
CALLING_LLM
  ↓
FINISHED
```

异常状态：

```text
FAILED
CANCELLED
MAX_STEPS_REACHED
```

状态变化必须产生结构化 Event。

## 5. Agent Loop

核心伪代码：

```go
for step := 0; step < maxSteps; step++ {
    emit(StateBuildingContext)

    req := context.Build()
    emit(ContextBuilt)

    emit(StateCallingLLM)
    resp, err := provider.Chat(ctx, req)
    if err != nil {
        return err
    }

    emit(LLMResponseReceived)

    if resp.Final != "" {
        emit(AgentFinished)
        return resp.Final
    }

    for _, call := range resp.ToolCalls {
        emit(ToolCallRequested)

        result := registry.Execute(call)

        emit(ToolCallFinished)

        context.AppendToolResult(call.ID, result)
    }
}
```

连续只读工具（read_file / list_dir / glob / grep）可并行执行；
写操作与 shell 保持串行。结果仍按原 tool_call 顺序写回 Context。

必须支持：

- max steps
- timeout
- context cancellation
- provider failure
- tool failure
- permission rejection
- basic loop detection
- parallel read-only tool batches（可配置开关与并发上限）

## 6. Message Model

内部统一消息结构，避免 Runtime 绑定 Provider SDK：

```go
type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)
```

```go
type Message struct {
    Role       Role
    Content    string
    ToolCalls  []ToolCall
    ToolCallID string
}
```

Provider 负责：

```text
Internal Message
      ↓
Provider-specific Request
```

## 7. LLM Provider

接口：

```go
type Provider interface {
    Chat(
        ctx context.Context,
        req ChatRequest,
    ) (*ChatResponse, error)
}
```

MVP 只实现 OpenAI-compatible API。

必须支持 Fake Provider，便于测试：

```text
LLM → Tool Call
Tool → Result
LLM → Final
```

## 8. Tool System

统一接口：

```go
type Tool interface {
    Name() string
    Description() string
    Schema() JSONSchema
    Execute(
        ctx context.Context,
        args json.RawMessage,
    ) (Result, error)
}
```

MVP Tool：

```text
read_file
list_dir
glob
grep
edit_file
write_file
shell
```

### read_file

支持：

```text
path
start_line
end_line
```

避免默认读取超大文件。

### grep

支持：

```text
pattern
path
file_pattern
```

### edit_file

第一版采用：

```text
old_text → new_text
```

要求唯一匹配，否则失败。

### shell

支持：

```text
command
working_directory
timeout
```

返回：

```text
stdout
stderr
exit_code
duration
```

## 9. Permission System

基础权限：

```go
type PermissionLevel int

const (
    Allow PermissionLevel = iota
    Ask
    Deny
)
```

默认策略：

```text
read_file   allow
list_dir    allow
glob        allow
grep        allow

write_file  ask
edit_file   ask
shell       ask
```

后续可针对命令模式配置：

```yaml
permissions:
  shell:
    "go test *": allow
    "git status": allow
    "git diff *": allow
    "rm *": deny
```

## 10. Workspace Sandbox

Workspace 是所有文件类 Tool 的安全边界。

必须防止：

```text
../ traversal
absolute path escape
symlink escape
```

路径必须经过规范化和边界检查。

## 11. Context Manager

Context Manager 决定每一轮模型实际看到什么。

候选组成：

```text
System Prompt
Project Instructions
Skills
Conversation Summary
Recent History
Tool Results
Pinned Context
Current User Input
```

禁止无上限地将完整历史不断追加。

## 12. Context Snapshot

每次调用 LLM 前生成 Context Snapshot。

例如：

```text
Context Snapshot #12

System Prompt          2,341 tokens
Instructions           1,104 tokens
Skills                   812 tokens
Summary                 3,221 tokens
History                 7,922 tokens
Tool Results           12,843 tokens
Current Input             141 tokens
-----------------------------------
Total                  28,384 tokens
```

Snapshot 至少应记录：

```text
source
token_count
included
excluded
truncated
compressed
pinned
```

## 13. Context Compression

第一版采用结构化摘要：

```text
Old Messages
    ↓
LLM Summary
    ↓
Structured Summary
```

Summary 建议保存：

```text
Goal
Current State
Important Files
Changes Made
Decisions
Errors
Constraints
Pending Work
```

## 14. Session

Session 保存：

```text
messages
tool calls
tool results
context snapshots
events
metrics
model
workspace
timestamps
```

目录：

```text
.mincode/
├── sessions/
└── traces/
```

支持：

```bash
mincode --continue
```

## 15. Instructions

支持 `AGENTS.md`。

可沿工作目录层级加载：

```text
workspace/AGENTS.md
workspace/backend/AGENTS.md
```

越具体目录的 Instruction 优先级越高。

内容可描述：

```text
Coding Style
Architecture Constraints
Testing Rules
Build Commands
Forbidden Changes
```

## 16. Skills

Skill 是按需加载的任务知识。

例如（Skill 由 workspace 提供，仓库不内置示例）：

```text
<workspace>/skills/
└── <name>/
    └── SKILL.md
```

初期手动激活：

```text
/skill <name>
```

后续再研究自动选择。

## 17. Error Handling

建议分类：

```text
ProviderError
ToolError
PermissionError
ContextError
UserCancelled
MaxStepsExceeded
```

可恢复的 Tool Error 应返回给 Agent，而不是直接终止进程。

## 18. Cancellation

所有长操作传播：

```go
context.Context
```

覆盖：

- Agent Loop
- LLM Request
- Shell
- Long-running Tool

Ctrl+C 至少能够取消当前操作。
