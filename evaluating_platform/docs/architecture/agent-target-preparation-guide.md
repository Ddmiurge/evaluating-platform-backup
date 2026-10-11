# 被测 Agent 准备指引

> 适用版本：target `kind=agent`，自定义 HTTP 端点。§2.1 多轮会话为U9 新增。
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
| 请求头模板 | JSON 对象；值中可写 `{{api_key}}` 占位符（多轮还可写 `{{conversation_id}}`），服务端替换 |
| 请求体模板 | JSON；用 `{{prompt}}` 占位攻击提示（每条用例替换一次）、`{{api_key}}` 占位密钥、**`{{conversation_id}}` 占位会话 id（多轮，见 §2.1）**。留空默认 `{"prompt":"{{prompt}}"}` |
| 响应提取路径 | 从 JSON 响应提取回复文本的点分路径，如 `reply.text`、`choices[0].message.content`；**多轮时同一路径用于取回会话 id** |
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

---

## 2.1 多轮会话（`{{conversation_id}}`）

> 对应平台决策 `06` D-04：以 **Dify** 为实际被测对象，Dify 提供
> `POST /v1/chat-messages`（chat 型）/ `POST /v1/completion-messages`（completion 型），
> Bearer 鉴权（`app-*`），返回体含 `answer` 与 `conversation_id`。

### 什么时候需要配

**默认仍是单轮**：每条攻击用例独立发一次请求，彼此无上下文。只有当你要用
**多轮诱导策略**（`crescendo` / `goat` / `hydra`）时，才需要下面这一步。

不配置 = 完全等价于 v1 单轮行为，平台不会替你猜。

### 怎么配（三项，缺一不可）

| # | 配置 | 值 |
|---|------|-----|
| 1 | 请求体模板含会话占位符 | `{"query":"{{prompt}}","conversation_id":"{{conversation_id}}"}` |
| 2 | 响应提取路径 | `conversation_id`（会话 id 在响应里的字段名） |
| 3 | 本次评测选择多轮策略 | `crescendo` / `goat` / `hydra` |

Dify chat 型的完整模板：

```json
// 请求头模板
{"Authorization":"Bearer {{api_key}}","Content-Type":"application/json"}
// 请求体模板
{"inputs":{},"query":"{{prompt}}","response_mode":"blocking","user":"redteam-eval","conversation_id":"{{conversation_id}}"}
// 响应提取路径
conversation_id
```

### 实际发生了什么

```
第 1 轮  POST /v1/chat-messages   {"query":"<攻击用例1>","conversation_id":""}
        ← 200 {"answer":"...","conversation_id":"真实会话ID"}
第 2 轮  POST /v1/chat-messages   {"query":"<攻击用例2>","conversation_id":"真实会话ID"}   ← 同一段对话
第 3 轮  POST /v1/chat-messages   {"query":"<攻击用例3>","conversation_id":"真实会话ID"}   ← 仍是同一段
```

会话 id 由**你的 Agent 服务端**持有（这正是 Dify 的工作方式），平台只负责
"这一轮取回、下一轮回传"。平台不存对话内容，也不需要知道你的 Agent 内部
怎么实现多轮。

### 三项缺一不可，缺了会被明确拒绝

平台**不会**把一个注定跑不成多轮的配置跑成一份"多轮"报告——那种报告的
探针数、成功率都和真多轮一样，只有上下文是假的：

| 缺什么 | 平台行为 | 错误码 |
|--------|----------|--------|
| 响应提取路径 | 保存被测对象时即拒绝（前端 + 后端同时校验） | — |
| 多轮策略 | 运行时拒绝该次运行 | `agent_session_requires_multiturn_strategy` |
| 响应提取路径（运行期兜底） | 运行时拒绝 | `agent_session_requires_response_path` |
| 显式写了 `stateful: false` | 运行时拒绝（二者矛盾） | `agent_session_stateful_false_contradiction` |

### 副作用隔离（多轮尤其重要）

多轮会话意味着**一次评测会在同一段对话里连续问很多轮**，比单轮更容易触发
真实副作用（连续下单、连续发消息、连续改数据）。因此：

- **必须**给评测提供沙箱/mock 端点。同一段对话里 agent 的上下文会累积，
  副作用也会累积——单轮"看起来没事"不代表多轮安全。
- 若只能用生产端点：确保会话 `user`（Dify 的 `user` 字段）指向一个**专用评测
  账号**，该账号下的工具调用只读或幂等；评测结束后在Dify 侧删除该会话。
- 平台在引擎侧会统计"目标响应里出现工具调用信号"的探针数，在报告里以
  `tool_call_signals` 给出（**只有计数，不含任何工具名或参数**）。它的作用
  是让"打到生产端点"和"打到沙箱"在报告上可区分，**不是**"检测到了工具就拦"
  ——平台不会、也无法阻断你的 Agent 内部行为。
- `{{conversation_id}}` **不要**放在对话端点 URL 的查询参数里：URL 会进
  接入方的访问日志。平台只支持放在请求体或请求头。

---

## 3. 边界与限制

1. **默认单轮，多轮需显式开启**：每个攻击用例独立发起一次请求（v1 行为）。要让多轮策略形成真实多轮上下文，需按 §2.1 配置 `{{conversation_id}}`。
2. **纯文本**：Agent 目标不支持图文（多模态）载荷；含图用例会安全失败（`target_multimodal_not_supported`），不会调到你的端点。**这一条不因多轮而改变。**
3. **协议范围**：只支持"HTTP 对话端点"。OpenAI Assistants API 这类有状态协议（thread 模型）、MCP Agent 直连属于后续候选，本期按 `06` D-04 的 **YAGNI** 裁定不做。
4. **评测有效期**：连通性探测默认 GET 对话端点（或单独配置的 `health_url`）。

## 4. 副作用与沙箱（重要）

安全评测会向你的 Agent 发送**对抗性攻击提示**。为避免评测触发真实副作用（发消息、下单、改数据、调用真实工具）：

- **首选**：给评测提供一个**沙箱/mock 端点**（同一对话逻辑、但工具与数据全部指向测试环境）。
- 若无法提供沙箱：确保 Agent 的工具调用在评测账号下**只读或幂等**，并在评测前挂起有副作用的集成。
- **多轮场景下这条更重要**：同一段对话里 agent 的上下文与副作用都会累积，见 §2.1「副作用隔离」。
- 平台不会替你隔离 Agent 的内部副作用；端点行为完全由接入方负责。引擎侧只做**可见性**（`tool_call_signals` 计数），不做阻断。

## 5. 接入步骤（企业门户）

1. 聊天页底部 → 「被测模型连接」→「被测对象类型」选择**智能体（Agent）**。
2. 填写名称、对话端点 URL、HTTP 方法（默认 POST）、请求头模板（可选）、请求体模板（可选）、响应提取路径（可选）、API Key（可选，write-only）。
   - 要跑多轮：请求体模板里加 `{{conversation_id}}`，并填写响应提取路径。
3. 点击「保存并测试」：保存后自动做连通性探测（GET 端点；2xx=健康）。
4. 发起评测（AI 对话确认卡片或自选评测页均可），平台将按模板逐条调用你的 Agent 并出报告。多轮需在策略里选 `crescendo` / `goat` / `hydra`。

## 6. 安全边界（平台侧承诺）

- Agent 凭据与 target secret 同级：加密存储（`maclaw_target_configs`）、write-only、只在服务端调用与引擎一次性物化时使用。
- 浏览器响应、SSE、job progress、报告 DTO、`engine_runs` 表中**不会**出现 agent 密钥或 `{{api_key}}` 替换后的值。
- **会话 id（`conversation_id`）同样受此约束**：它只在一次运行内存活（挂在 promptfoo 单次调用上下文里，随该次调用结束即释放），**不落库、不进报告、不进浏览器响应、不进日志**。
- 请求体/请求头模板中允许的占位符只有 `{{prompt}}`、`{{api_key}}`、`{{conversation_id}}` 三个；其余原样保留。
- 攻击用例原文、目标回复原文只存在于服务端 evidence handle 与 PDF 报告（脱敏规则同现有报告管线）。

## 7. 路线图（后续候选）

- OpenAI Assistants API 等有状态 thread 协议适配（`06` D-04 列为远期可选）。
- MCP Agent 直连。
- Agent 工具调用的带外声明式 mock 清单（当前只有 §2.1 的可见性计数）。
