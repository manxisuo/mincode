# Observability

## 1. 目标

可观测性是 Min Code Agent 的核心能力，不是日志附属功能。

项目应让开发者能够回答：

```text
Agent 当前在做什么？
这一轮模型看到了什么？
模型调用了什么 Tool？
Tool 输入输出是什么？
为什么下一步发生变化？
Context 如何增长、裁剪、压缩？
Token 消耗是多少？
失败在哪里？
执行链能否完整回放？
```

Observability 的定位是：

> Agent 黑盒飞行记录仪 + Context Debugger + Tool Inspector + Replay 系统。

## 2. Observation Layer

建议架构：

```text
Agent
Context
Provider
Tool
Permission
Session
      │
      ▼
   Event Bus
      │
 ┌────┼──────────────┐
 ▼    ▼              ▼
CLI  Recorder       Metrics
      │
      ▼
   JSONL Trace
      │
      ▼
 Inspector / Replay
```

业务模块负责产生事件，不负责决定事件如何展示或存储。

## 3. Event Model

建议：

```go
type Event struct {
    ID        string
    Time      time.Time
    SessionID string
    Step      int
    Type      EventType
    Data      any
}
```

事件应尽量结构化，而不是把所有信息塞进一条文本日志。

## 4. 核心 Event 类型

至少包括：

```text
agent.started
agent.finished
agent.failed
agent.cancelled
agent.state_changed

context.build_started
context.built
context.compaction_started
context.compacted

llm.request_started
llm.request_finished
llm.request_failed

tool.requested
tool.started
tool.finished
tool.failed

permission.requested
permission.approved
permission.denied

session.created
session.saved
session.restored

instruction.loaded
skill.loaded
skill.unloaded
memory.retrieved
memory.updated
```

后续可以扩展：

```text
loop.detected
context.item_dropped
context.item_truncated
```

## 5. Trace Recorder

每个 Session 建议保存：

```text
.mincode/traces/<session-id>.jsonl
```

一行一个 Event。

示例：

```json
{
  "type": "tool.started",
  "step": 4,
  "tool": "grep",
  "arguments": {
    "pattern": "Router"
  }
}
```

Trace 要满足：

- 可顺序读取
- 可增量写入
- 崩溃后尽可能保留已有记录
- 可用于 Replay
- 不依赖 CLI 输出格式

## 6. Timeline Inspector

核心视图应能还原整个 Agent 执行链：

```text
#1  User Input

#2  Context Built
    18.4K tokens

#3  LLM Request
    qwen-coder

#4  LLM Response
    tool_call: grep

#5  Tool
    grep "Router"
    17 matches
    31ms

#6  Context Built
    22.1K tokens

#7  LLM Request

#8  Tool
    read_file

#9  Tool
    edit_file

#10 Tool
    go test ./...

#11 Final Answer
```

Timeline 是最核心的学习界面之一。

## 7. Context Inspector

Context Inspector 的核心问题：

> 这一轮模型到底看到了什么？

应展示：

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

每项可展开，并记录：

```text
source
token count
included
excluded
truncated
compressed
pinned
```

例如：

```text
README.md

Source:
tool.read_file

Original:
11,340 tokens

Included:
4,000 tokens

Reason:
tool result budget limit
```

## 8. Context Snapshot

每次 LLM 请求前生成 Snapshot。

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

Snapshot 应成为 Trace 中的正式实体，而不是临时调试输出。

## 9. Context Compression 可视化

Compression 发生时必须记录前后变化：

```text
Before:
91K tokens

After:
43K tokens

Compressed:
history #1 - #22

Preserved:
recent 8 messages
latest tool outputs
pinned context
```

最好还能展示：

```text
Dropped
Compressed
Preserved
Pinned
```

这样可以直接研究 Context Engineering 策略。

## 10. LLM Inspector

至少展示：

```text
LLM Request #7

Provider:
openai-compatible

Model:
qwen-coder

Input Tokens:
18,432

Available Tools:
7
```

允许展开：

```text
system
messages
tool definitions
```

建议支持：

```text
Export raw request
```

用于对照不同模型 API 和 Prompt 结构。

## 11. Tool Inspector

每次 Tool 调用记录：

```text
Tool
Arguments
Start Time
End Time
Duration
Result
Result Size
Error
Permission Decision
```

例如：

```text
Tool:
shell

Command:
go test ./...

Exit:
1

Duration:
2.4s

stdout:
...

stderr:
...
```

对于文件 Tool，还应记录：

```text
path
line range
bytes read / written
context impact
```

## 12. Agent State

建议实时显示：

```text
Current State:
EXECUTING_TOOL

Current Step:
8 / 30

Current Tool:
shell

Elapsed:
13.2s
```

状态机建议包括：

```text
IDLE
BUILDING_CONTEXT
CALLING_LLM
PROCESSING_RESPONSE
EXECUTING_TOOL
WAITING_APPROVAL
FINISHED
FAILED
CANCELLED
MAX_STEPS_REACHED
```

## 13. Metrics

建议统计：

```text
LLM Calls
Tool Calls
Input Tokens
Output Tokens
Total Tokens
Agent Steps
Total Runtime
Tool Runtime
LLM Runtime
Context Compactions
```

示例：

```text
Session Metrics

Agent Steps        14
LLM Calls           9
Tool Calls          8

Input Tokens       52,120
Output Tokens       6,411

LLM Time           18.4s
Tool Time           3.2s
Total Time         23.1s
```

后续可用于模型和策略对比实验。

## 14. Inspector 模式

正常运行：

```bash
mincode
```

调试运行：

```bash
mincode --inspect
```

早期可以先使用普通终端输出，不必立即引入 TUI。

示例：

```text
┌ Agent ───────────────────────┐
│ State: EXECUTING_TOOL        │
│ Step: 8 / 30                 │
└──────────────────────────────┘

┌ Context ─────────────────────┐
│ System        2.2K           │
│ Instructions  1.3K           │
│ History       8.1K           │
│ Tools         9.7K           │
│ Total        21.3K           │
└──────────────────────────────┘

┌ Current Action ──────────────┐
│ Tool: grep                   │
│ Pattern: Router              │
└──────────────────────────────┘
```

## 15. Replay

支持：

```bash
mincode replay <session-id>
```

基础命令：

```text
next
prev
goto
summary
```

示例：

```text
Step 8 / 17

Event:
tool.finished

Tool:
grep

Result:
17 matches
```

后续可增加 Context Replay：

> 查看该 Step 当时真正发送给模型的 Context。

## 16. Reason / Decision 展示

透明性分两类，实现与 UI 必须区分清楚：

| 类型 | 展示什么 | 要求 |
|---|---|---|
| **Runtime 决策** | 策略分支、阈值、规则命中（budget 排除、shell Deny、为何并行） | 100% 可解释，与代码逻辑一致 |
| **LLM 行为** | Action、Arguments、可观察证据、简短 action rationale | **禁止**把推测的内部思维链当作事实 |

建议记录：

```text
Action
Decision Summary
Reason Summary
Observed Evidence
```

例如：

```text
Next Action:
grep

Reason:
Locate HTTP route registration before modifying code.

Evidence:
Previous read_file showed no route registration in main.go.
```

目标是理解 **Runtime 为何这样做** 与 **数据从哪来**，而不是保存长篇内部推理文本。

更完整的透明性层级与优先级（Wire Request、Context Diff、Provenance 等）见后续 Roadmap；本项目「绝对透明」以 **显式决策 + 数据血缘** 为主干。

## 17. Observability 测试

可观测性本身必须测试。

至少覆盖：

```text
event ordering
step number
trace persistence
context snapshot generation
tool duration
metrics aggregation
state transition
replay parsing
```

不要把它当作“UI 层所以不用测”。

## 18. 设计原则

每增加一个 Agent 能力，都必须同时回答：

```text
它如何被观测？
如何记录？
如何回放？
如何定位失败？
如何评估成本？
如何与其他策略比较？
```

如果不能回答这些问题，该能力的设计还不完整。

**透明性原则**：优先解释 Runtime 的显式决策与数据血缘；对 LLM 只展示可观察的 action rationale，禁止把推测的内部推理当作事实展示。

## 19. 透明性层级（Transparency Levels）

为避免 Inspector 无方向膨胀，观测能力按层验收：

| 层级 | 回答的问题 | 典型能力 | 现状 |
|---|---|---|---|
| **L1 Event** | 发生了什么？ | Timeline、状态机、metrics | 已有 |
| **L2 Data** | 输入输出是什么？ | tool args/result 预览、context snapshot、session 内容 | 部分具备 |
| **L3 Causal** | Runtime 为何这样决定？ | 排除/压缩/拒绝/并行的原因与策略链；estimated vs provider tokens | **下一阶段主攻** |
| **L4 Provenance** | 这条信息从哪来、经过什么变换？ | file → tool_result → context item → LLM request 血缘 | **下一阶段主攻** |
| **L5 Replay** | 当时完整 Runtime 状态是什么？ | 拖到 Step N 时 state+context+config+plan+memory 整体回放 | 规划中 |

**实现约束（与 §8 / §16 一致）：**

- L3/L4 针对 **Runtime 显式决策** 与 **数据变换**，必须与代码分支一致。
- LLM 相关展示仅限 action rationale + evidence，**禁止**伪造思维链。

## 20. 透明性 Roadmap（优先级）

两条必须先打通的主链：

```text
① Context Builder → sanitize/trim → Provider Adapter → Wire HTTP Request
② Source(File/Tool) → Context Item → LLM Request → Next Action
```

| 序 | 能力 | 层级 | 完成定义（DoD 摘要） |
|---|---|---|---|
| 1 | **Wire Request View** | L2→L3 | 展示 model/messages/tools 关键字段；estimated tokens vs provider `prompt_tokens` 与 delta；可追溯 Context → trim → provider 转换 → 最终请求 |
| 2 | **Context Diff + 排除/压缩原因** | L3 | 相邻 snapshot 的 ± 条目；excluded/truncated 带 policy、reason、节省 tokens |
| 3 | **Provenance / 数据血缘** | L4 | context item 可追到 tool.call_id、路径/行号、进入 context 的 step、transform（截断等） |
| 4 | **Runtime Decision Trace** | L3 | permission 策略链（规则→命中→Allow/Ask/Deny）；并行原因（ParallelSafe、MaxParallel）；loop detection 等 |
| 5 | **Historical Snapshot Replay** | L5 | Inspector 在 Step N 展示当时完整状态，而非「当前状态 + 事件列表」 |
| 6 | **Tool 全文结果 / 失败详情** | L2 | 按 call_id 可选加载完整 output 与错误（预览默认，全文显式拉取） |
| 7 | **Config Resolution Inspector** | L3 | model/base_url/budget 等的 Default → Global → Project → Env → CLI 覆盖链 |

每项均须：结构化事件或 API 可测、Web/CLI 至少一侧可展示、不依赖未记录的「口头解释」。

交付顺序按上表；实现时仍遵守 AGENTS.md「每次只做一个 Phase/子任务」。

## 21. 透明性验收问题（评审用）

新观测能力合入前，应能回答：

```text
用户能否指出这条 context 的来源？
用户能否解释这次排除/拒绝是哪条规则触发的？
用户能否看到真正发给 Provider 的请求，而不只是本地 Snapshot？
展示的「原因」是 Runtime 分支，还是被包装成事实的推测？
```
