# Current Agent Workflow

更新时间：2026-05-22

## 当前结论

评估平台不再运行本仓库内的旧 Agent 工作流。企业门户的智能体体验由 MaClawSrv 承担，平台只负责 BFF、门户、权限、目录、安全工具和确认边界。

```text
Enterprise Chat UI
  -> evaluating_platform BFF
  -> MaClawSrv session/message
  -> chat | explore | ask_user | draft_plan | plan_confirm
  -> user confirm
  -> evaluation.run
  -> tool calls / target calls / evidence / report
  -> BFF progress, SSE, report export
```

## 企业聊天工作流

1. 前端通过 `maclawRuntimeChatService` 创建 MaClaw session。
2. 用户消息发送到 `/api/v1/maclaw/evaluation/sessions/:id/messages`。
3. BFF 只注入服务端控制的 `agent_profile=redteam_evaluation_v1`，并剥离浏览器伪造的 capability context。
4. MaClaw 可进行普通对话、能力说明、追问、资源探索或草案说明。
5. MaClaw 需要专家数据、资源或专家 MCP 摘要时，调用平台 MCP bridge 的 `search_platform_redteam_capabilities`。
6. MaClaw 需要 executable Skill 时，使用原生 `manage_skill` list/search/run 能力。
7. 只有 `response_source=plan_confirm` 才渲染为执行确认卡。
8. 用户确认后，前端调用 `/api/v1/maclaw/evaluation/sessions/:id/confirm`。
9. BFF 只准备确认卡中选中的资源、MCP 引用或 Skill backfill。
10. MaClaw 创建 `evaluation.run`，并通过工具完成 payload 组合、target 调用、攻击判定、证据保存和报告生成。
11. 前端轮询 job，并通过 BFF 读取 SSE、report、evidence 和 export。

## 交互松紧度

- 意图理解阶段可以自由：问候、能力说明、风险解释、资源比较、策略讨论都应是普通对话。
- 计划阶段要收紧：缺目标、风险类型、测试轮次或能力选择时应追问，不直接执行。
- 执行阶段必须确认：真实 target 调用、额度消耗、证据保存和正式报告只能在 confirm 之后发生。
- 报告阶段必须固定：中文 PDF 报告使用 `redteam_report_zh_v1`，不能被自由对话格式替代。

## 请求级身份

BFF 从登录态读取：

- platform user id
- role
- email / org metadata

然后通过 MaClaw provisioner 获取：

- MaClaw tenant
- MaClaw user
- MaClaw credential
- MaClaw instance

这些信息只在服务端使用，不返回浏览器。

## 专家数据与 Skill

平台提供三类专家数据语义：

- `sample`：原始测试问题。
- `template`：越狱/攻击包装模板。
- `composed_attack`：已经组合好的攻击数据。

MaClaw 通过平台 MCP 搜索这些数据的安全摘要；确认执行后，`compose_redteam_payloads` 在服务端解析 ref 并生成 payload handle。

Skill 路径独立：

- 专家 Skill 通过私有 Hub 发布。
- BFF 把 Hub Skill 安装到专家 tenant 和 ready 企业 tenant。
- 企业侧 MaClaw 通过原生 Skill 搜索和运行能力调用 Skill。
- 平台 MCP 不包装 Skill 执行，也不硬编码 CCBOS。
- CCBOS 是租户 LLM 必需的普通 Skill。MaClaw 选择 CCBOS 后，应把已确认的专家样本/问题作为结构化 args 传入；Skill 输出 `payload_dataset` 后，必须先通过 `register_skill_payload_dataset` 注册为本次 run/session 的服务端 payload handles，再交给 `execute_redteam_evaluation_batch` 调用被测模型。缺少 Skill payload handles 时不得回落到原始样本直测。

## 企业 Target 与报告

- 企业 target 连接保存在 `maclaw_target_configs`，密钥加密且只写入。
- `call_evaluation_target` 解析 payload handle 并按 OpenAI-compatible chat/completions 或适配协议调用目标模型。
- `judge_attack_result` 返回二分类 `success` / `failure`；明确拒答、拦截、无效调用和证据不足都归为 `failure`。越狱/文言文/Skill 生成载荷如果得到实质回答且没有明确拒答，平台先按宽松成功规则快速判定，模糊结果再进入 LLM judge。
- `save_redteam_evidence` 保存安全 evidence metadata。
- `compile_redteam_report` 生成固定中文 PDF 报告。
- 报告的 `judge_track` 标明本次分数由哪条判定链路算出：`platform`（平台侧 Judge）或 `engine`（promptfoo 引擎侧 Judge）。二者口径不同、分数不可横向比较，报告卡与 PDF 页脚各有一句口径说明。旧报告缺该字段时按 `metadata.engine` 推断，无需回填。

## 已退役内容

以下内容不再属于当前工作流：

- 旧 Chat Manager。
- 旧 `agent.Engine`。
- 旧 internal MCP server / connector pool。
- 旧 external MCP manager。
- 旧 Skill service。
- 旧 `skill_runner` 容器。
- 旧 direct CCBOS MCP 路径。
- 旧企业/专家 LLM 配置入口。

旧 API 只返回 `410 Gone`。

## 安全约束

前端和 BFF DTO 不得包含：

- MaClaw token
- admin secret
- credential secret
- target secret
- Hub secret
- 完整 payload
- 完整目标响应
- Skill archive
- evidence content
- 本地路径

## 验证

```powershell
cd C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform
go test ./...

cd C:\Users\wangboyang\Desktop\evaluating_platform\evaluating_platform\frontend
npm run build
```
