# AGENTS.md

本文件定义 AI Code Agent 在开发 Min Code Agent 项目时必须遵守的工程约束。

目标是保证项目始终保持：

- 可理解
- 可测试
- 可观测
- 可回滚
- 小步演进
- 不过度设计

## 1. 开发边界

每次只完成当前 Roadmap 中明确指定的 Phase 或子任务。

禁止一次性实现整个 Roadmap。

如果当前任务属于 Phase 2，则不要顺手实现 Phase 3、Phase 4 或更后的能力。

禁止因为“以后可能有用”提前引入复杂抽象。

## 2. 核心原则

### 2.1 不做无关重构

修改必须与当前任务直接相关。

不要：

- 重命名大量无关文件
- 改变未涉及模块的 API
- 顺手迁移目录结构
- 顺手替换依赖
- 顺手修改格式之外的大量代码

### 2.2 优先标准库

除非第三方库能显著降低复杂度，否则优先使用 Go 标准库。

不要引入重量级 Agent Framework。

Agent Runtime 必须自行实现，以便学习其内部机制。

### 2.3 Provider 必须可替换

核心 Runtime 不得依赖某个具体模型或 SDK。

应依赖内部抽象：

```go
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}
```

必须提供 Fake Provider，确保 Agent Loop 可以在不访问真实模型的情况下测试。

### 2.4 所有 Tool 必须可测试

任何新增 Tool 都必须有单元测试。

至少覆盖：

- 正常情况
- 参数错误
- 边界条件
- 超时或取消（适用时）
- 安全限制（适用时）

文件类 Tool 必须额外考虑：

- `../` path traversal
- absolute path escape
- symlink escape
- missing file
- permission denied
- large file / large output

### 2.5 可观测性是一等公民

新增任何核心能力时，都要同时考虑：

```text
它发生时产生什么 Event？
需要记录哪些关键输入？
需要记录哪些关键输出？
耗时如何记录？
Token / Context 如何变化？
Inspector 如何展示？
Replay 是否需要知道它？
```

如果某项行为无法被观测，应明确说明原因。

不要只使用散乱日志代替结构化事件。

## 3. 依赖方向

期望依赖关系：

```text
CLI
 ↓
Agent Runtime
 ↓
Interfaces
 ↓
Implementations
```

正确：

```text
Agent → Provider interface
Agent → Tool Registry
Agent → Context Manager
Agent → Permission Policy
Agent → Event Bus
```

避免：

```text
Tool → Agent
Provider → Agent
Context → CLI
Observability → 业务实现细节
```

## 4. Agent Runtime 规则

Agent 本身只负责协调流程，不直接：

- 读写文件
- 执行 Shell
- 调用 Git 命令
- 保存 Trace
- 实现 Provider API
- 操作具体数据库

这些行为必须通过专门模块完成。

核心流程应保持清晰：

```text
Input
→ Build Context
→ Call LLM
→ Parse Response
→ Execute Tool
→ Record Observation
→ Append Result
→ Repeat
→ Final Answer
```

## 5. Tool 设计规则

Tool 接口应保持小而稳定：

```go
type Tool interface {
    Name() string
    Description() string
    Schema() JSONSchema
    Execute(ctx context.Context, args json.RawMessage) (Result, error)
}
```

Tool 参数来自模型，应视为不可信输入。

所有路径相关 Tool 都必须限制在 Workspace 内。

### edit_file

第一版优先使用精确替换：

```text
old_text → new_text
```

要求：

- 0 次匹配：失败
- 1 次匹配：执行
- 多次匹配：失败

不要一开始实现复杂 AST patch。

## 6. Shell 规则

Shell 必须支持：

- timeout
- cancellation
- working directory
- stdout
- stderr
- exit code
- duration

危险命令不得静默执行。

默认建议：

```text
read-only command  → allow
write command      → ask
destructive command → deny
```

例如：

```text
go test ./...      allow
git status         allow
git diff           allow
rm -rf             deny
git reset --hard   deny
git clean -fd      deny
```

## 7. Context 规则

Context Manager 不能简单把全部历史无限追加给模型。

必须逐步支持：

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

每次 LLM 调用前，应能够生成 Context Snapshot。

重要字段包括：

```text
source
token count
included
excluded
truncated
compressed
pinned
```

## 8. Reasoning / Explanation 规则

透明性优先解释 **Runtime 的显式决策**（context 排除/压缩、permission 判定、并行化等真实代码分支）与 **数据血缘**（信息从哪来、经过何种变换）。

对 LLM 行为只展示可观察的 **action rationale**（动作、参数、依据证据）；**禁止把推测的内部推理当作事实展示**，不要设计成依赖模型输出完整隐藏思维链。

项目可以记录：

- Action
- Decision Summary
- Reason Summary
- Observed Evidence

推荐：

```text
Next Action:
grep

Reason:
Locate HTTP route registration before modifying code.

Evidence:
Previous read_file showed no route registration in main.go.
```

透明性层级与交付优先级见 `observability.md` §19–21 与 `roadmap.md`「Transparency Deepening」（Wire Request、Context Diff、Provenance 等按序小步实现）。

## 9. 错误处理

不要滥用 panic。

错误使用 `%w` 包装。

建议区分：

```text
ProviderError
ToolError
PermissionError
ContextError
UserCancelled
MaxStepsExceeded
```

Tool 错误如果可恢复，应返回给 Agent，使 Agent 有机会调整下一步。

## 10. Context 与取消

所有长时间操作必须传播：

```go
context.Context
```

至少包括：

- LLM Request
- Agent Loop
- Shell
- Long-running Tool

Ctrl+C 应能够取消当前操作。

## 11. 测试与验证

每个阶段完成后必须运行：

```bash
gofmt -w .
go vet ./...
go test ./...
```

不得声称命令成功，除非实际执行结果确认成功。

## 12. Git 规范

每个 Commit 只解决一个逻辑问题。

推荐：

```text
feat(core): add event bus
feat(trace): add jsonl recorder
feat(llm): add provider abstraction
feat(agent): implement agent loop
feat(tools): add read-only tools
feat(context): add context snapshots
feat(inspector): add timeline view
feat(shell): add command execution
feat(session): add replay
```

避免：

```text
feat: implement everything
```

## 13. 当前优先级

默认开发顺序：

```text
Foundation
↓
LLM
↓
Read-only Agent
↓
Context Inspector
↓
Code Modification
↓
Shell
↓
Session / Replay
↓
Context Compression
↓
Instructions
↓
Skills
↓
Planning
↓
Memory
↓
Experiment Framework
```

不要从 TUI、MCP、Multi-Agent 或插件系统开始。

## 14. 每轮完成后必须汇报

每个任务完成后输出：

- 新增或修改文件
- 核心实现说明
- 新增 Event / Trace 行为
- 测试结果
- 已知限制
- 是否存在架构折衷
- 下一阶段建议

如果测试失败，不要隐藏失败结果。
