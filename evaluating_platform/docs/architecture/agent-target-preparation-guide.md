# 被测 Agent 准备指引（P3a/P3b）

> 适用版本：2026-09-28 企业端迭代（target `kind=agent`，自定义 HTTP 端点）。
> 面向：准备把自有智能体接入平台做安全评测的企业用户。

## 1. 你需要自己搭一个 Agent 吗？

**平台不要求、也不提供"平台自建 Agent"作为被测对象。** 被测 Agent 应当是你**已有的**、对外暴露对话端点的智能体（企业客服 Agent、内部 RAG 问答、MCP 工具型助手等）。平台只负责三件事：**连得上**（配置端点与凭据）、**调得动**（把攻击用例发给它并取回回复）、**判得出**（对回复做安全判定与汇总）。

如果你手上还没有现成 Agent，才需要自行搭建一个（任意框架：LangChain / LlamaIndex / 自研均可），再把它的 HTTP 对话端点接入平台。这是**被测对象的准备问题**，不是平台功能缺口。

## 2. v1 支持的接入方式

v1 采用**自定义 HTTP 端点**模式（对齐 promptfoo `http` provider 的模型）：

| 配置项 | 说明 |
|--------|------|
| 对话端点 URL | 智能体的对话 API 地址，每个攻击用例独立发起一次请求 |
| HTTP 方法 | POST（默认）/ PUT / PATCH / GET |
| 请求头模板 | JSON 对象；值中可写 `{{api_key}}` 占位符，服务端替换为已加密保存的密钥 |
| 请求体模板 | JSON；用 `{{prompt}}` 占位攻击提示（每条用例替换一次）、`{{api_key}}` 占位密钥。留空默认 `{"prompt":"{{prompt}}"}` |
| 响应提取路径 | 从 JSON 响应提取回复文本的点分路径，如 `reply.text`、`choices[0].message.content` |
| API Key / Token | write-only：加密存储、不回显、不出现在任何日志/报告/浏览器响应 |

默认行为：若密钥已配置而模板未引用 `{{api_key}}`，平台自动附带 `Authorization: Bearer <密钥>` 请求头（适配 OpenAI 兼容网关）。

示例——一个自定义客服 Agent 端点：

```json
// 请求头模板
{"X-Api-Key": "{{api_key}}"}
// 请求体模板
{"query": "{{prompt}}", "session": "eval-fixed"}
// 响应提取路径
reply.text
```

平台发出的实际请求（密钥仅在服务端替换）：

```
POST /chat
X-Api-Key: <你的密钥>
{"query": "<攻击用例文本>", "session": "eval-fixed"}
```

## 3. v1 边界与限制

1. **单轮调用**：每个攻击用例独立发起一次请求，**不维护多轮会话状态**（thread/记忆）。多轮诱导策略（multi-turn / crescendo）在 Agent 目标上暂不会形成真实多轮上下文。
2. **纯文本**：Agent 目标 v1 不支持图文（多模态）载荷；含图用例会安全失败（`target_multimodal_not_supported`），不会调到你的端点。
3. **协议范围**：只支持"无状态 HTTP 对话端点"。OpenAI Assistants API 这类有状态协议（thread 模型）、MCP Agent 直连属于阶段 2，未包含在 v1。
4. **评测有效期**：连通性探测默认 GET 对话端点（或单独配置的 `health_url`）。

## 4. 副作用与沙箱（重要）

安全评测会向你的 Agent 发送**对抗性攻击提示**。为避免评测触发真实副作用（发消息、下单、改数据、调用真实工具）：

- **首选**：给评测提供一个**沙箱/mock 端点**（同一对话逻辑、但工具与数据全部指向测试环境）。
- 若无法提供沙箱：确保 Agent 的工具调用在评测账号下**只读或幂等**，并在评测前挂起有副作用的集成。
- 平台不会替你隔离 Agent 的内部副作用；端点行为完全由接入方负责。

## 5. 接入步骤（企业门户）

1. 聊天页底部 → 「被测模型连接」→「被测对象类型」选择**智能体（Agent）**。
2. 填写名称、对话端点 URL、HTTP 方法（默认 POST）、请求头模板（可选）、请求体模板（可选）、响应提取路径（可选）、API Key（可选，write-only）。
3. 点击「保存并测试」：保存后自动做连通性探测（GET 端点；2xx=健康）。
4. 发起评测（AI 对话确认卡片或自选评测页均可），平台将按模板逐条调用你的 Agent 并出报告。

## 6. 安全边界（平台侧承诺）

- Agent 凭据与 target secret 同级：加密存储（`maclaw_target_configs`）、write-only、只在服务端调用与引擎一次性物化时使用。
- 浏览器响应、SSE、job progress、报告 DTO、`engine_runs` 表中**不会**出现 agent 密钥或 `{{api_key}}` 替换后的值。
- 模板中允许的占位符只有 `{{prompt}}` 与 `{{api_key}}` 两个；其余原样保留。
- 攻击用例原文、目标回复原文只存在于服务端 evidence handle 与 PDF 报告（脱敏规则同现有报告管线）。

## 7. 路线图（阶段 2 候选）

- OpenAI Assistants API / 有状态 thread 协议适配（多轮攻击用例按会话组织）。
- MCP Agent 直连。
- Agent 工具调用的 mock/沙箱清单（由平台提供带外声明式配置）。
- 多轮攻击策略（crescendo 等）与 Agent 会话绑定。
