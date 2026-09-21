<img width="2172" height="724" alt="MinCode" src="https://github.com/user-attachments/assets/cd956ac4-26fb-449d-9436-068fd06801d6" />

# Min Code

Min Code Agent 是一个面向学习、实验和研究的轻量级 Code Agent Runtime。

它的目标不是复制 Claude Code、Codex CLI、Cursor Agent 或 OpenCode，也不是追求功能数量，而是通过一个结构清晰、行为透明、可观测、可扩展的实现，理解现代 Code Agent 的核心工作机制。

**当前状态：Roadmap Phase 0–12 已完成**，并包含 Hardening 修复、MVP 验收测试、Parallel Tool Calls，以及 **W1 Local Web Inspector**。

## 核心目标

项目围绕三个关键词设计：

- **Build**：实现一个最小但完整的 Code Agent Runtime。
- **Observe**：清楚看到 Agent 在运行过程中发生了什么。
- **Experiment**：方便替换模型、Prompt、Tool、Context 策略和 Agent 策略，并比较不同方案。

因此，本项目不仅是一个 Code Agent，也是一个：

> Code Agent 实验平台 + 调试器 + 学习工具。

## 设计原则

1. **可理解优先**  
   架构、调用链和模块边界必须清晰，不为了“工程高级感”引入过度抽象。

2. **可观测性是一等公民**  
   Agent、LLM、Context、Tool、Permission、Session、Compression 等核心行为都应产生结构化事件。

3. **模型只是组件**  
   Agent 能力来自 LLM、Context Engineering、Tool Design、Agent Loop、环境反馈、安全边界和可观测性的组合。Provider 必须可替换。

4. **默认安全**  
   模型输出视为不可信输入。文件、Shell、Git 等能力必须受 workspace 和权限策略约束。

5. **小步演进**  
   先完成最小闭环，再逐步增加修改、验证、持久化、Context Compression、Skill、Memory 等能力。

## 非目标

初期明确不做：

- 完整 IDE 或 Cursor 类编辑器
- 云端 SaaS
- 多用户或企业权限系统
- 完整 MCP Host
- IDE 插件
- 复杂 Multi-Agent
- Computer Use
- 通用 AST 重构系统
- 一次性支持所有 LLM Provider
- 长时间完全自主运行的 Agent

## 技术栈

```text
Language        Go
CLI             标准 flag
Config          YAML
Storage         JSON / JSONL
Session         JSON
Trace           JSONL
Testing         Go testing
LLM             OpenAI-compatible API
```

## 已实现能力（Phase 0–12）

```text
CLI REPL / 单次执行 / --continue / replay

OpenAI-compatible + Fake Provider

Agent Loop（状态机、loop detection、取消）

Tools:
  read_file / list_dir / glob / grep
  write_file / edit_file / shell
  memory_add

Parallel Tool Calls（连续只读工具并行；写/Shell 保持串行）

Workspace 沙箱 + Permission（文件路径逃逸、危险 shell 拒绝）

Context:
  token budget / snapshot / compression
  Instructions (AGENTS.md 层级) / Skills / Memory

Session 持久化、Trace JSONL、Timeline、Metrics

Plan Mode（/plan 草稿 → approve → 逐步执行）

Experiment Framework（run / list / show / compare，含 min/median/avg/max）

会话导出 Markdown（/export）
```

与普通练习型 Code Agent 最大的区别：从第一天起就包含完整的 **Observation Layer**。

## 快速开始

```bash
# 配置
cp mincode.example.yaml mincode.yaml

# 编辑 model / base_url；API Key 用环境变量
export OPENAI_API_KEY=sk-...
# 或 MINCODE_API_KEY / MINCODE_BASE_URL / MINCODE_MODEL

# Web UI 静态资源（embed 用；仓库不提交 dist/）
cd web && npm install && npm run build && cd ..

go build -o mincode ./cmd/mincode

./mincode ./my-project
```

完整配置项见 `mincode.example.yaml`。

REPL 内常用命令：

```text
/help
/timeline          执行链
/context           最近一次模型看到了什么
/instructions      已加载的 AGENTS.md
/skills  /skill <name>
/memory  /memory add <fact>
/plan <goal>       草稿计划 → /plan approve
/export [path]     导出会话 Markdown
/metrics
/trace [n]
/exit
```

单次执行与回放：

```bash
mincode -p "分析这个项目"
mincode --continue
mincode replay <session-id>
```

本地 Web Inspector（W1）：

```bash
mincode web              # 默认 http://127.0.0.1:8080
mincode web . --addr 127.0.0.1:9090
```

浏览器打开后左侧对话、右侧实时 Timeline / Context / Metrics。

前端为 **Vue 3 + TypeScript**（`web/`），生产资源由 Vite 构建到 `web/dist/`，再 `embed` 进二进制：

```bash
cd web
npm install
npm run build    # 产出 dist/，随后 go build 使用 embed
```

改前端后需重新 `npm run build && go build`。开发调试可用 `cd web && npm run dev`（代理 `/api` 到 8080）。

后续计划含 **LLM 流式输出**（边生成边展示、取消更早中断），详见 `roadmap.md`。

Sessions / traces / experiments 默认写在：

```text
{user_home}/.mincode/projects/{project-id}/
  project-id = {目录名}-{SHA256(绝对路径)前8位}
```

`data.location: workspace` 可改到项目内 `.mincode/`。**只使用当前布局**，不合并读取另一种目录。详见 `mincode.example.yaml`。

实验对比：

```bash
mincode experiment run --name model-a --task "说明 CLI 到 tool 的调用链" --model model-a --repeat 5
mincode experiment run --name model-b --task "说明 CLI 到 tool 的调用链" --model model-b --repeat 5
mincode experiment compare model-a model-b
```

结果写入 `<workspace>/.mincode/experiments/<name>/`。对比时优先看 **median**（均值易被 outlier 拉偏）。

## 典型执行流程

```text
用户输入
    ↓
Prompt / Instructions / Skills / Memory
    ↓
Context Construction
    ↓
LLM Request
    ↓
Tool Selection
    ↓
Environment Feedback
    ↓
Context Update
    ↓
下一轮决策
    ↓
代码修改 / 测试验证
    ↓
最终回答
```

Min Code Agent 的目标不是只让 Agent “能工作”，而是让开发者能够理解：

- 模型这一轮到底看到了什么
- Agent 为什么调用某个 Tool
- Tool 对下一轮 Context 产生了什么影响
- Token 如何增长
- Context 何时被裁剪或压缩
- 失败后 Agent 如何恢复
- 不同模型和策略之间的差异

## 文档

- [架构设计](architecture.md)
- [可观测性设计](observability.md)
- [开发路线图](roadmap.md)
- [Agent 开发约束](AGENTS.md)

## 验收

Roadmap 中的 MVP Test 1–6 已固化为自动化测试：

```bash
go test ./internal/cli/ -run TestMVP
```

覆盖：仓库分析、TODO 搜索、改代码、失败恢复、workspace 逃逸拒绝、危险命令拒绝。

## 开发

```bash
gofmt -w .
go vet ./...
go test ./...
```
