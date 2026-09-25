# Roadmap

> **状态：Phase 0–12 已全部完成**（含 Hardening 修复、MVP 验收测试、Experiment 分布统计）。
> 高级方向中 **Parallel Tool Calls**、**Web Inspector / 流式**、**Transparency Deepening（Wire View / Context Diff / Provenance / Decision Trace / Historical Replay / Tool Result / Config Resolution）**、**Plan-and-Execute**、**Web Search / Web Fetch**、**Reflection**、**Repository Map** 均已落地。

## 总体原则

路线图遵循：

```text
Build
→ Observe
→ Execute
→ Validate
→ Persist
→ Compress
→ Extend
→ Experiment
```

Observability 从 Phase 0 开始，不允许拖到项目后期再补。

---

## Phase 0：Foundation

### 目标

建立项目骨架和结构化事件基础。

### 实现

```text
Go Project
CLI
Config
Logging
Event Model
Event Bus
Trace Recorder
```

### 完成标准

```bash
mincode --help
```

能够运行。

能够生成最基本 JSONL Trace。

---

## Phase 1：LLM Chat + Trace

### 实现

```text
Message Model
Provider Interface
OpenAI-compatible Provider
Basic REPL
LLM Request Trace
LLM Response Trace
Duration
Token Metrics
Fake Provider
```

### 完成标准

```text
User → LLM → Answer
```

并能够查看：

```text
LLM Request
LLM Response
Token Usage
Duration
```

### 本阶段禁止

```text
Tool
Agent Loop
Skill
Memory
Context Compression
TUI
```

---

## Phase 2：Read-only Agent

### 实现

```text
Agent Loop
Tool Registry

read_file
list_dir
glob
grep

Agent State
Timeline
Tool Trace
```

### 完成标准

Agent 可以分析真实仓库，例如：

```text
分析这个项目的入口和整体架构。
```

并且 Timeline 能展示完整执行链。

---

## Phase 3：Context Inspector

### 实现

```text
Context Manager
Context Snapshot
Token Budget
Context Inspector
```

### 完成标准

开发者可以查看：

> 每一次 LLM 调用到底包含哪些内容。

至少区分：

```text
System
Instructions
History
Tool Results
Current Input
```

并记录 token 数量。

---

## Phase 4：Code Modification

### 实现

```text
write_file
edit_file
permission approval
diff

File Change Trace
Permission Trace
```

### 完成标准

Agent 可以修改代码，但修改必须可见、可审查。

---

## Phase 5：Shell + Validation

### 实现

```text
shell
timeout
cancellation
safe command policy
```

Inspector 展示：

```text
command
stdout
stderr
exit code
duration
```

### 完成标准

Agent 可以：

```text
go test ./...
npm test
cargo test
```

并根据失败结果继续处理。

---

## Phase 6：Session + Replay

### 实现

```text
session persistence
--continue
trace replay
step navigation
```

### 完成标准

能够：

```bash
mincode --continue
```

以及：

```bash
mincode replay <session-id>
```

此阶段后，项目开始具备真正的 Agent Debugger 属性。

---

## Phase 7：Context Compression

### 实现

```text
token threshold
structured summary
history compression
```

Inspector 必须显示：

```text
Before
After
Dropped
Compressed
Preserved
Pinned
```

### 完成标准

长对话不会无限增长，且压缩行为可解释、可回放。

---

## Phase 8：Project Instructions

### 实现

```text
AGENTS.md
directory hierarchy
instruction loader
instruction inspector
```

### 完成标准

Agent 能遵守项目级和目录级规则。

Inspector 可以显示：

```text
加载了哪些 Instructions
它们来自哪里
它们进入了哪次 Context
```

---

## Phase 9：Skills

### 实现

```text
skills/
SKILL.md
/skill
```

### 第一版

只支持手工激活：

```text
/skill <name>
```

### Inspector

显示：

```text
当前加载了哪些 Skill
来自哪里
为什么进入 Context
```

---

## Phase 10：Plan Mode

### 实现

```text
/plan
task list
progress
approval
```

流程：

```text
理解任务
→ 输出计划
→ 用户确认
→ 执行
```

暂不做复杂 Multi-Agent Planner。

---

## Phase 11：Memory

### 第一版

```text
MEMORY.md
```

用途：

```text
跨 Session 的项目事实
稳定约束
长期决策
```

### 后续实验

```text
SQLite
Embedding
Semantic Retrieval
```

---

## Phase 12：Experiment Framework

### 目标

让 Min Code Agent 从学习项目升级为 Agent 实验平台。

支持对比：

```text
Provider A vs Provider B
Prompt A vs Prompt B
Context Strategy A vs B
Tool Design A vs B
```

记录：

```text
Steps
Tokens
Runtime
Success
Tool Calls
Compactions
Failures
```

---

# 后续高级实验方向

基础版本稳定后再考虑：

```text
Parallel Tool Calls     ✅ 已实现（Phase 13）
Repository Map          ✅ 已实现（启动注入 + repo_map 工具）
Semantic Code Search
RAG
Context Caching
Reflection             ✅ 已实现（有界自省 + reflection.* 事件）
Sub-Agent
MCP
Long-term Memory
Adaptive Tool Selection
Plan-and-Execute       ✅ 已实现（/plan auto 命令 + 失败重规划）
Local Web Inspector     ✅ W1 已实现（HTTP/SSE + web/）
LLM Streaming Output    ✅ 已实现（SSE 增量 + TTFT + CLI/Web 展示）
Transparency Deepening  ✅ 已实现（T-obs-1~7）
Web Search              ✅ 已实现（Provider: fake / Tavily / SearxNG + web_search 工具）
Web Fetch               ✅ 已实现（web_fetch 工具 + SSRF 防护）
```

任何新增能力都必须同步设计对应可观测能力。

---

## Transparency Deepening（透明性深化 · 已实现）

设计原则见 `README.md` 设计原则 §2 与 `AGENTS.md` §8：  
**优先解释 Runtime 显式决策与数据血缘；对 LLM 只展示 action rationale，禁止把推测推理当事实。**

透明层级与完整 DoD 见 `observability.md` §19–21。

### 两条主链（必须先打通）

```text
① Context Builder → trim/sanitize → Provider Adapter → Wire HTTP Request
② File/Tool Source → Context Item → LLM Request → Next Action
```

### 优先级（按序交付，一次只做一个子项）

| 序 | 子项 | 层级 | 说明 |
|---|---|---|---|
| T-obs-1 | **Wire Request View** ✅ | L2→L3 | 真正发给 Provider 的 messages/tools；estimated vs `prompt_tokens` 与 delta |
| T-obs-2 | **Context Diff + 排除/压缩原因** ✅ | L3 | 快照间 ±；policy/reason/节省 tokens |
| T-obs-3 | **Provenance / 数据血缘** ✅ | L4 | item ← tool.call_id ← 路径/行 ← step；transform |
| T-obs-4 | **Runtime Decision Trace** ✅ | L3 | permission 策略链、并行原因、loop detection |
| T-obs-5 | **Historical Snapshot Replay** ✅ | L5 | Step N 的完整 Runtime 状态回放 |
| T-obs-6 | Tool 全文结果 / 失败详情 ✅ | L2 | 按 call_id 显式拉取完整输出 |
| T-obs-7 | Config Resolution Inspector ✅ | L3 | Default → Global → Project → Env → CLI 覆盖链 |

### 明确不做（本系列）

- 展示或伪造 LLM「隐藏思维链」并当作事实
- 无事件/API 支撑的「口头解释」UI
- 一次做完全部层级（必须按上表小步交付）

---

## LLM Streaming Output（已实现）

### 动机

非流式取消时只断开 HTTP；UI 需等整段响应；TTFT 不可测。

### 已实现

```text
internal/llm: StreamingProvider + CompatibleProvider.ChatStream（OpenAI SSE）
              FakeProvider.ChatStream（分块回放）
Agent: Stream 开关；delta 仅发 Event（llm.stream_delta），CLI/Web 订阅 Bus
              tool_call 增量聚合后交给原 loop
事件: llm.stream_started / stream_delta / stream_finished
      llm.request_finished 带 streamed / ttft_ms
Metrics: Stream Calls / Stream Deltas / Last TTFT
CLI: 回合内流式打印；已流式输出则不重复打印 Final
Web: SSE delta 合并进助手气泡（~40ms 刷新）
配置: provider.stream（默认 true）
```

### 明确不做（第一版）

```text
流式 tool_call 增量解析进 Agent 中途执行（仍聚合完再执行）
多 Provider 流协议统一
Token 级计费对账
```

### 配置

```yaml
provider:
  stream: true
```

---

## W1：Local Web Inspector（已实现）

### 目标

本机单用户：`mincode web` 启动 HTTP + SSE，浏览器查看对话与 Runtime 可观测数据。

### 实现

```text
cmd: mincode web [workspace] [--addr]
internal/server: /api/session|chat|cancel|events(SSE)|context|metrics|timeline
web/: Vue3 + TS（Vite）→ dist/ → go:embed
Bus.Subscribe → SSE fan-out（不侵入 Agent Loop）
```

### W1 边界

```text
仅本地单会话
Ask 级写操作自动批准（危险 shell 仍拒绝）
无登录 / 无远程 workspace / 无 Vue 工程
```

### 后续可选

```text
网页 Permission 审批          ← 暂缓
Experiment Dashboard          ✅ W2 已实现（列表 / 分布 / runs）
Timeline 图形化（并行 batch 分叉）
LLM 流式输出写入聊天区        ✅ 已实现
Trace Explorer                ✅ 并入 Timeline History（方案 1）
```

---

## Timeline History（方案 1）

```text
GET /api/traces           列出 <traceDir>/*.jsonl
GET /api/traces/{id}      读 JSONL；?type=前缀过滤 &limit=
Web Timeline: Live | History
  History: 下拉选 session、type 过滤、可选原始 JSON
  与 Live 共用同一列表渲染，避免第二套事件产品
```

---

## W2：Experiment Dashboard（已实现）

```text
GET /api/experiments           列表 + Aggregate（含 min/median/max）
GET /api/experiments/{name}    详情 + runs
Web: Inspector | Experiments 页签
     左列表 med duration 条；右分布表 + run 明细
```

---

## Phase 13：Parallel Tool Calls（已实现）

### 目标

模型在一次响应中返回多个只读工具调用时并发执行，缩短探索路径耗时。

### 实现要点

```text
连续 read_file / list_dir / glob / grep → 并行 batch
write_file / edit_file / shell / unknown → 保持串行
需要交互审批的调用 → 回退串行
结果按原 tool_call 顺序写回 Context
```

### 可观测

```text
tool.batch_started / tool.batch_finished
tool.* 事件带 parallel / call_id / index
/metrics: Parallel Batches / Parallel Tools
Timeline: Batch Start / Batch Done
```

### 配置

```yaml
agent:
  parallel_tools: true
  max_parallel: 4
```

---

## Web Search / Web Fetch（已实现）

### 目标

让 Agent 能获取工作区之外的信息：先「发现 URL」，再「读取正文」。

### 实现

```text
internal/websearch   Provider 接口（Search）+ FakeProvider + HTTPProvider(Tavily) + SearxNGProvider
internal/webfetch    Fetcher 接口（Fetch）+ HTTPFetcher + FakeFetcher
tools                web_search / web_fetch
```

- **web_search**：默认关闭，配置后置才注册
  - `fake`（离线）/ `tavily`（商业 API）/ `searxng`（自托管）
  - 结果条数 / snippet / 总输出均限长；结果视为不可信内容
- **web_fetch**：无需配置，始终注册
  - 纯标准库；HTML→文本（去 script/style/注释，提取标题）；5 MiB 读取上限；最多 5 次重定向
  - SSRF 防护：按实际连接 IP 拦截 loopback / private / link-local / CGNAT，仅允许 http(s)，禁用代理

### 可观测

```text
复用 tool.requested / tool.started / tool.finished
Meta: query / count / duration_ms（search）；url / final_url / status / title / bytes（fetch）
```

### 配置

```yaml
websearch:
  type: ""          # "" 禁用 | fake | tavily | searxng
  # base_url / api_key / max_results / timeout_sec
```

### 明确不做

```text
浏览器渲染（不执行 JS；纯 JS 渲染的页面可能取不到正文）
```

---

## Repository Map（已实现）

### 目标

让 Agent 先看到仓库的紧凑结构（文件 + 顶层符号），再决定读哪些文件，
从而改善代码导航并降低 context 浪费（少做盲目 glob/grep/read）。

### 实现

```text
internal/repomap   Build(ctx, workspace, Options) → ranked + budgeted Map
  Go 文件: 标准库 go/ast 提取 func / method / type / struct / interface / const / var
  其他语言: 仅列路径 + 语言（不引第三方解析器）
  排名: 导出符号 + 跨文件引用计数（均封顶）+ focus 加权 + 测试惩罚
  预算: MaxTokens 截断；跳过 vendor/node_modules/.git/.mincode/dist 等与生成文件
  缓存: Cache（abs path + mtime + size）复用符号与标识符统计，跨调用增量
```

- **启动注入**：`agent.repo_map`（默认 true）构建一次，作为 pinned system
  context（`source=repo_map`），预算 `agent.repo_map_tokens`（默认 1500）。
- **repo_map 工具**：只读、可并行；参数 `path`（限定子目录）、`focus`
  （加权关键词）、`max_tokens`（硬上限 4000），用于按需刷新子图。
- **数据血缘/增量**：`FileEntry.Rank` 带 `defs/exported/refs/focus/path_hit/symbol_hits/test_penalty`，
  排名可解释；Agent 工具与 Web API 共享同一 `Cache`。
- `/repomap` 命令查看当前注入的 map 与统计。

### Web Inspector

```text
GET /api/repomap[?path=&focus=&max_tokens=]
Web: Repo Map 页签 — 排名表（得分 + 依据 chips）、符号明细、原始地图文本
     支持子目录/聚焦词刷新；复用 Agent 的增量缓存，二次构建 ~0ms
```

### 可观测

```text
repo_map.built（root/subpath/focus/files/scanned/skipped/symbols/tokens/build_ms/truncated/reason）
Context snapshot: source=repo_map（pinned）
启动 reason=startup；工具调用 reason=agent tool
API 响应含 cache_hits/cache_misses（本次构建的增量命中）
```

### 配置

```yaml
agent:
  repo_map: true
  repo_map_tokens: 1500
```

### 明确不做（第一版）

```text
tree-sitter / 多语言精确符号（非 Go 仅列路径）
语义 / embedding 排序（后续 Semantic Code Search）
缓存落盘 / 跨进程共享（当前为进程内缓存，快速路径为 mtime+size）
```

---

## Reflection（自省 · 已实现）

### 目标

让 Agent 在最终回答前自审一次，发现遗漏 / 错误则继续迭代，提升结果可靠性。

### 实现

```text
Agent.Run 在「无 tool_calls」准备收尾时触发：
  发一条 user 指令要求自审，模型须回答
    VERDICT: DONE            → 接受当前回答
    VERDICT: CONTINUE + 问题  → 追加 [self-reflection] 消息并继续循环
```

- 有界：`agent.reflection`（默认 false）+ `agent.max_reflections`（默认 2），且受 `max_steps` 约束
- **仅当本轮用过工具时触发**：纯对话 / 创作类回合跳过，避免与严格格式输出冲突、也省一次调用
- 自审指令仅针对「最新一条回答」，显式禁止回顾/重做更早回合
- 自审调用不流式；失败按 best-effort 处理（接受当前回答，不中断回合）
- 默认关闭：开启后每个「用过工具的完成回合」多一次 LLM 调用

### 可观测

```text
reflection.started / reflection.finished（verdict / issue_count / critique_preview / duration_ms）
decision.recorded（domain=reflection, action=continue|done）
Context: 自省消息来源标记为 reflection（区别于 user_input）
```

### 配置

```yaml
agent:
  reflection: true
  max_reflections: 2
```

### 明确不做

```text
长期 / 跨会话的反思记忆（属 Memory）
让模型输出隐藏思维链
```

---

# MVP 验收任务

## Test 1：仓库分析

```text
分析这个项目的入口和架构。
```

验证：

```text
read_file
glob
grep
context build
timeline
```

---

## Test 2：搜索任务

```text
找出所有 TODO。
```

验证：

```text
grep request
grep result
context impact
```

---

## Test 3：修改代码

```text
新增 /health，并补测试。
```

验证：

```text
read
search
edit
permission
shell
final
```

---

## Test 4：失败恢复

故意制造一个测试失败。

要求 Agent：

```text
执行测试
读取错误
定位问题
修改
再次测试
```

通过 Timeline 检查完整反馈回路。

---

## Test 5：Workspace 安全

```text
读取 ../../etc/passwd
```

必须失败。

---

## Test 6：危险命令

```text
rm -rf .
```

必须被 Permission System 拒绝。

---

# 第一轮开发任务

第一轮只做 Phase 0 和 Phase 1。

建议直接给开发 Agent：

```text
请完整阅读 README.md、AGENTS.md、docs/architecture.md、
docs/observability.md 和 docs/roadmap.md。

本轮只完成 Phase 0 和 Phase 1。

目标：

1. 初始化 Go 项目。
2. 创建基础目录结构。
3. 实现 CLI REPL。
4. 定义内部 Message 模型。
5. 定义 Provider interface。
6. 实现 OpenAI-compatible Provider。
7. 支持 YAML 配置和环境变量。
8. 定义 Event 模型。
9. 实现最小 Event Bus。
10. 实现 JSONL Trace Recorder。
11. 记录 LLM request / response / duration / token usage。
12. 实现基础 /trace 和 /metrics 命令。
13. 提供 Fake Provider 和必要测试。

明确禁止：

- 不实现 Tool。
- 不实现 Agent Loop。
- 不实现 Skill。
- 不实现 Memory。
- 不实现 Context Compression。
- 不实现 TUI。
- 不提前实现 Phase 2。

完成后必须执行：

gofmt -w .
go vet ./...
go test ./...

最后汇报：

- 新增文件
- 核心架构
- Event 流程
- Trace 示例
- 测试结果
- 当前已知限制
- 下一阶段建议
```
