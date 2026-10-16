# 23 · 聊天链路报告原文（明文）与 promptfoo 报告对齐 —— 实施计划

> 登记：2026-10-15｜性质：**契约变更 + 实施计划**。
> 上游：`16-PlanC收敛与报告原文决策.md` §5.0-A（D1=B 的三种实现强度 B-1/B-2/B-3）。
> 本文件把 D1 落定为 **B-3 全量**，并按用户要求**取消加密/脱敏这一层**。
> 二次修订（2026-10-15）：新增 §12（聊天确认卡片显示策略 + 可删单个插件/策略）；
> §11 用白话重写；§7 由 promptfoo 报告格式勘察补齐。
> **开工门槛**：P3-6 服务器验证已于 2026-10-15 完成（10/10 通过，`21-阶段三服务器验证记录-20261015.md`）。
> **2026-10-16 修订**：对照服务器验证记录修正了本文件的**两处判断错误**（§12.1b、§12.5）——
> 策略其实**已经在计划卡上**（`promptfoo_strategy:base64`，实测），
> 原先写的「跨仓依赖、要等 maclaw 写字段」不成立；§12.2 的 P1 也从代码推断升级为**实测确认**。
> 决策来源：用户 2026-10-15 裁定（原话见 §1）。

---

## 0. 状态总览（2026-10-16）

### 0.1 实施状态

| 项 | 状态 |
|---|---|
| §12.2 P1（confirm 丢弃策略 refs） | ✅ 已实现 + **服务器实测确认**（见 §9 判据 12） |
| §6.1 / §6.1b 引擎逐条原文与多轮转录 | ✅ 已实现；引擎侧**服务器实测确认**（判据 1 / 10） |
| §6.2 BFF 逐条 finding + 汇总进元数据 | ✅ |
| §6.3 `isAllowedResultField` 接线 | ✅（反证：删掉 `case_results` 声明 → 引擎报 undeclared field） |
| §6.5 清洗拆分与长度上限 | ✅（报告/证据两个函数） |
| §6.6 判定理由误打证据编号 | ✅ |
| §6.7 第 3 段列全、第 4 段照旧 | ✅ |
| §6.9 报告第 5 段渲染插件/策略汇总 | ✅ **服务器实测确认**（判据 9c） |
| §7 PDF 逐条卡片形状对齐 | ✅ **服务器实测确认**（判据 9a） |
| §6.4 前端文案与展开区 | ✅ |
| §6.8 / §3 契约文档同步 | ✅（AGENTS.md / PROJECT_GUIDE.md / maclaw-current-state.md / 两个迁移文件的注释） |
| **§12 卡片显示策略 + × 删标签** | ⏳ **未做，建议单独一批**（不影响报告正确性） |
| §11.2 / §11.4 等决策 | ✅ 已记录 |

服务器验证记录：`23-阶段四服务器验证记录-20261016.md`（12 条判据：**9 条实测通过**，
3 条以其证据缺口如实记录，逐条状态见 §9）。

### 0.2 三处口径澄清（用户 2026-10-16 裁定，勿再误读为缺口）

1. **判据 2 不算缺口。** 用户原话：「我觉得能让我看见攻击载荷就行，成功毕竟是要看方法」。
   所以验收标准是**载荷与目标回答能逐字看见**，不是「必须跑出一次攻击成功」。
   前者已实测成立（见判据 9a）；后者取决于攻击方法与靶子强度，不是这条链路的验收条件。
   本文件与记录里凡写「未出现系统提示词泄露实例」的地方，都按此口径读：**那是靶子守住了，
   不是报告没做到**。
2. **网络类失败不在本批范围。** 引擎到生成模型不可达（今早 10:11 的 `EAI_AGAIN`）
   由部署方自行处理，**不新增 confirm 前连通性预检**。原先记的这条建议已撤销。
3. **多轮策略暂不测。** 用户原话：「我目前被攻击的靶子没有弄得很好，我也不确定那个 dify
   能不能被多轮攻击到，这个是要后面进行计划才能决定，测试可以先不测试这个」。
   实现照做（§6.1b 已上线且引擎侧实测有 `turns`），但**对话链路的多轮可达性属于后续规划**，
   不作为本批的验收项 —— 见 §9 判据 10。

---

## 1. 决策（用户原话与我的解读）

用户 2026-10-15 裁定：

> 「修改之前的安全约定，确保聊天链路下 promptfoo 生成的载荷以及攻击的结果能够出现在 PDF 中
> （攻击结果并不是那种总结性的，比如我去挖系统提示词你至少要把系统提示词给我展示出来）。
> 内容直接明文，也不需要加密保密什么的，因为我看不到原文这个软件原型就没有任何意义。
> 至于自选评测导航那里先不用管，不在这一批范围之内。」

以及同日追加：

> 「我还希望聊天的时候，生成的卡片也会显示选择策略有哪些，如果可以的话，
> 策略和选项的小卡片中应该有小叉，可以决定不要某个插件或者策略」

逐条解读，**每条都是可验收的**：

| # | 用户要求 | 落点 |
|---|---|---|
| R1 | **聊天链路**下 promptfoo **生成的载荷**进 PDF | §6 的引擎链路改造（这是唯一缺原文的链路） |
| R2 | **攻击的结果**也进 PDF，且**不能是总结性的** | 目标模型的**回答正文**逐条进 PDF；验收标准就是用户举的例子：挖系统提示词的用例，PDF 里必须能看到目标吐出的**系统提示词正文** |
| R3 | **明文**，不加密、不做保密层 | 取消 `sanitizeRedteamArtifactText` 的正文替换；报告表不加密（本来就没加密，见 §3.3） |
| R4 | 自选评测导航**不在本批** | 向导链路（`POST /api/v1/maclaw/engine/runs`）本批不主动改 —— 但见 §11.1 的顺带影响 |
| R5 | PDF 尽量**对齐 promptfoo 自己的测试报告**展示方式 | §7（已由格式勘察补齐） |
| R6 | 聊天确认卡片**显示已选的攻击策略** | §12 |
| R7 | 插件/策略的小标签**能删** | §12 |

**这是对 `16` §5.0-A.4 的 B-3 选项的正式采纳**，同时明确否掉了 B-1（只显示截断摘要）
与 B-2（只在管理员门户看）——用户要的就是「PDF 里能直接看到」。

`16` §5.0-A.2 已经写明 B-3 的后果，**用户已知情并接受**：

> 内容一旦渲染进 PDF，它就是明文，加密库对它毫无办法。
> PDF 发出客户邮箱 / 存进工单系统 / 进合规归档之后，平台完全失去控制：无法撤回、无法删除、无法追踪流向。

本文件不再重开这个决策，只在 §8 把它写成一条**运行时的行为**（而不是一句警告）。

---

## 2. 现状实测：哪条链路已经有原文，哪条没有

「聊天链路」在代码里其实是**两条**，它们的现状完全相反。

### 2.1 批量链路 —— **已经有原文**（`execute_redteam_evaluation_batch`）

触发方式：聊天确认的计划卡选中专家**样本 / 模板 / 组合攻击 / Skill**，maclaw 经 MCP 调批量工具。

| 报告字段 | 内容 | 位置 |
|---|---|---|
| `finding.Title` | **攻击载荷原文** | `redteam_bridge.go:960`、`:1012` → `reportTestPrompt()`（`:1816-1821`）直接返回 `stored.Payload` |
| `finding.Description` | **目标回答原文** | `redteam_bridge.go:1015` ← `result.call.TargetResponse`（`:1396`、`:1422` 由 `ExtractTargetResponseText` 抽出） |
| 多模态图片 | 载荷图片 | `reportImages[findingID]`（`:946`）→ `pdfFindingImages` |

PDF 第 3 段「评估发现」的 findingCard 已经渲染 `测试问题：` / `模型回答：`
（`redteam_artifact_service.go:1129-1158`）。**也就是说 R1/R2 在这条链路上基本已经成立**，
本批只需要修它自己的两个小毛病（§6.5、§6.6）。

### 2.2 引擎链路 —— **完全没有原文**（`run_promptfoo_redteam_evaluation`）

触发方式：聊天确认的计划卡选中 promptfoo **插件 / 策略**，BFF 自己起引擎 run
（`engine_confirm.go:92` `confirmPromptfooEnginePlan`）。

| 层 | 现状 |
|---|---|
| promptfoo 引擎 | 跑完把结果**归约**成 `SafeRunResult`，只留 counts / severity / plugin_stats / strategy_stats / judge_sources / token_usage 等汇总 |
| 引擎模块头 | `redteamRun.ts:28`：「Raw prompts/responses never leave this module.」 |
| BFF | `EngineSafeResultFindings`（`promptfoo_engine_bridge.go:642`）**逐插件**一条：「插件 X 共执行 N 条探针，攻击成功 M 条，成功率 P%。」 |
| PDF | 第 3 段只有上面那一句汇总；**没有任何一条攻击原文，也没有任何一句目标回答** |

**用户要的「promptfoo 生成的载荷」正好落在这条链路上 —— 它是唯一缺原文的那条。**

### 2.3 一个必须知道的约束：原文只能随终态返回体出来

引擎在 run 终态会删掉 promptfoo 的库与工作目录（P2-3 的清理链，`promptfooStateCleanup.ts`
+ `promptfooDbLifecycle.ts`）。所以逐条原文**不能**在 run 结束后回读数据库，
必须在**归约的那一刻**就从 promptfoo 的结果对象里取出来、随终态返回体一起交出去。
这条决定了 §6.1 的改法。

### 2.4 两个「看着像护栏、其实没接线」的东西（本批必须处理）

| 位置 | 现状 | 为什么必须处理 |
|---|---|---|
| `promptfoo_engine/src/sanitize/redact.ts:46-63` `ALLOWED_TOP_FIELDS` + `isAllowedResultField` | 白名单只有 7 个字段，`generation_path` / `num_tests_effective` / `judge_sources` **都不在里面**；而且 `isAllowedResultField` **全仓库没有任何生产调用点**（只有它自己的单测） | 它读起来是「什么允许离开引擎」的唯一闸门，实际上什么都没拦。本批要新增逐条字段，如果只往白名单里加字段而不接线，后来者会以为已经受控；**一旦真接上，又会把 P2-6 的 `generation_path` 和 U8 的 `judge_sources` 洗掉**。必须一起做 |
| `redteam_artifact_service.go:271-299` `sanitizeRedteamArtifactText` | 只在文本**含标记短语**（`raw payload` / `raw prompt` / `raw response` / `payload body` / `target response body` / `local path` / `evidence content`）时整体替换成 `[redacted sensitive text]`；然后按 `sk-` 之类清一次密钥；最后截断到 2000 字 | 真实载荷正文不含这些词，所以**现在就没在拦**；但 2000 字截断会切掉长载荷与长回答（挖系统提示词的场景很容易超） |

---

## 3. 契约变更：逐条列出要改什么、改成什么

### 3.1 `AGENTS.md`

| 行 | 现文（摘要） | 改法 |
|---|---|---|
| **47** | `save_redteam_evidence` 与 `compile_redteam_report` **只**持久化安全摘要；原始目标提示词、原始目标回答、payload 正文、凭据、token、本地路径**一律不得**出现在这两张表 | **拆成两条**：①`maclaw_redteam_reports` **允许**逐条原文（攻击载荷、目标回答、判定理由）——这是本批的契约变更本体；②`maclaw_redteam_evidence` **维持摘要不变**（它是证据索引，不是正文载体）。凭据 / token / 本地路径仍然两表都禁 |
| **46 末** | 判定原始值 `score_0_to_5` / `refusal_detected` 永不落库、不回浏览器、不进报告 DTO / 报告表 | **保留，但补一条口径规则**（2026-10-15 用户裁定：「如果说是通过 promptfoo 来进行评分的，那就都对齐 promptfoo」）：**报告展示的判定口径跟随 `judge_track`** —— ①`judge_track=engine`（promptfoo 自己判的）→ 逐条展示 promptfoo 的 `pass` / `score` / `reason`，与 promptfoo 报告一致；②`judge_track=platform`（平台 Judge 判的）→ 只展示通过/不通过与理由，**不展示分数**（平台那套 `score_0_to_5` 仍然禁）。这样两个分数**永远不会同时出现**，禁令也就不再需要「读者自己分辨」 |
| **55** | PDF 固定中文模板章节固定；「**Do not reintroduce** assessment scope / data-capability source lists / judgement-method sections / evidence indexes / attack-path summary / impact analysis / appendices」 | **保留章节禁令**，补一句：攻击原文填进**已有的**第 3 段「评估发现」与第 4 段「攻击成功样例」，**不新增章节** —— 这正是 `16` §5.0-A.1 说的「填进已有章节就不撞那条禁令」 |
| **23** | Skill 的 payload 原文「只作服务端临时 handle，不得返回浏览器或存进报告/证据」 | 改：**不得**出现在浏览器的**即时响应**里（保持）；**报告正文允许**（本批变更） |
| **15 / 56** | 进度元数据（`current_stage` / `planned_count` / `executed_count` / `duration_ms` / `stage_durations_json`）不得含 prompts / payloads / raw responses / 凭据 / token / 本地路径 | **完全保留**。进度元数据是给前端进度卡和延迟诊断的，与报告正文是两件事；这条不在本批范围内，且不能因为「要明文」被顺手放开 |
| **45** | MCP 桥不得把 payload / 凭据 / 归档 / evidence content / 本地路径暴露给浏览器 | **保留**。这条约束的是 MCP 工具**返回值**，不是报告。报告是唯一的原文出口 |

新增一条（放在 maclaw Upgrade Boundary 一节）：

> 聊天链路的两条执行路径都把逐条原文写进报告：批量链路（`execute_redteam_evaluation_batch`）
> 用 `finding.Title` 承载攻击载荷原文、`finding.Description` 承载目标回答原文；引擎链路
> （`run_promptfoo_redteam_evaluation`）把 promptfoo 的逐条结果随终态 `SafeRunResult` 带出来，
> 由 `EngineSafeResultFindings` 转成逐条 finding。这是**有意的契约选择**（2026-10-15，
> 见 `23-聊天链路报告原文与promptfoo对齐.md`）：报告是本产品的交付物，看不到原文的报告对
> 原型没有意义。**因此 `maclaw_redteam_reports.findings` 会持有攻击载荷与目标回答的明文，
> 且会随 PDF 离开平台。** 仍然不进报告的是：凭据、token、目标 `credential_secret`、
> 生成模型 API key、maclaw 本地路径、判定数值（`score_0_to_5` / `refusal_detected`）。

### 3.2 引擎侧契约

| 位置 | 现文 | 改法 |
|---|---|---|
| `promptfoo_engine/src/runner/redteamRun.ts:28` 模块头 | 「Raw prompts/responses never leave this module.」 | 改成：「逐条原文**只**随**终态结果**离开本模块，且只走报告链路；不得进日志、不得进 error_brief、不得进进度事件。」 |
| `promptfoo_engine/src/types.ts` `SafeRunResult` 文档 | 只有汇总字段，「结构上就没有逐条原文的位置」 | 新增逐条字段（§6.1），文档里写明这是 2026-10-15 契约变更的一部分 |
| `promptfoo_engine/src/sanitize/redact.ts` 模块头 | 「Browser red line: prompts, raw target responses, credentials, local paths must never leave the engine.」 | 改成：凭据与本地路径仍然永不离开；**逐条原文随终态结果离开，且只在报告链路**。并把 `isAllowedResultField` **真正接上**（§6.3） |
| `internal/maclaw/redteam_artifact_service.go:264-290` `sanitizeRedteamArtifactText` 注释 | 声称拦 `raw payload` 等 | 注释与行为一起改（§6.5） |

### 3.3 数据库注释（**无迁移**）

| 位置 | 现文 | 改法 |
|---|---|---|
| `migrations/026_promptfoo_engine.sql:30` | `result JSONB ... -- SafeRunResult（脱敏汇总）` | 注释改为「SafeRunResult（含逐条原文）」。列不变，**不需要迁移** |
| `migrations/026_promptfoo_engine.sql:1-7` 头注释 | 「No target prompts, responses, payloads, credentials, or engine-local paths are ever stored here.」 | 改：逐条原文**会**存进 `result`；凭据与本地路径仍然不存 |
| `migrations/022_maclaw_redteam_artifacts.sql` 头注释 | 同类声明 | 改：`findings` 持有逐条原文 |
| `AGENTS.md` 提到的 `maclaw_redteam_reports` 列 | 已有 `findings JSONB` | **装得下**，无需新列 |

> 保留期与加密：用户明确说「也不需要加密保密什么的」。所以**本批不引入**密钥管理、
> 保留期清理、权限分级、导出拦截这四件事 —— 它们在 `16` §5.0-A.3 里是 B-2/B-3 的配套成本，
> 本批按用户口径不做。§8 把「不做」写成了可验证的行为。

---

## 4. 仍然不进报告的东西（写死，避免「明文」被读成「什么都可以」）

| 类别 | 例子 | 为什么仍然禁 |
|---|---|---|
| 凭据 / 密钥 | 目标 `credential_secret`、生成模型 API key、maclaw token、admin secret、Hub token | 这些是**凭据**不是**内容**。泄露是不可逆的凭据损失，与「看攻击原文」这个诉求无关，且用户没要求放开 |
| 本地路径 | `workdir`、`/home/node/.promptfoo/...`、maclaw 容器内路径 | 无信息价值，且会暴露部署结构 |
| 判定数值 | `score_0_to_5`、`refusal_detected`、`attack_score` | `AGENTS.md:46` 明确禁止，且它们不进 PDF 也不影响用户看原文 |
| 进度元数据 | `stage_durations_json` 等 | `AGENTS.md:15/56` 明确禁止，与报告正文正交 |

**判定理由文本是个例外**：它是 judge 输出的自然语言理由，报告里已经有「风险说明/建议」这一行承载它。
本批保留这个行为，但要在 `AGENTS.md:46` 写明「数值不落、理由文本进报告」的区分。

---

## 5. 范围边界

**在范围内**：

1. 聊天链路的两条子链路 —— 引擎链路（§2.2）与批量链路（§2.1）；
2. 聊天确认卡片显示攻击策略 + 可删单个插件/策略（§12，用户追加）。

**不在范围内**（用户明确排除）：自选评测导航（向导）链路。
后果要说清：向导链路本批**不会主动改造**，但它与聊天共用 `EngineSafeResultFindings`，
所以 §6.2 的改动会**顺带**让向导的报告也带上逐条原文。要不要分流见 **§11.1**（白话版）。
默认**不分流**。

---

## 6. 要做的事（逐项）

### 6.1 引擎：把逐条结果带出来

**函数**：`reduceEvalToSafeResult(evalResult, catalog?, options?)`
（`promptfoo_engine/src/runner/redteamRun.ts:312-551`）。
它现在只读 **9 个表达式**（`gradingResult.pass` / `testCase.metadata.pluginId` /
`testCase.metadata.strategyId` / `judge source metadata` / `response.raw`（只喂给
`countToolCallSignals` 数个数）/ `gradingResult.severity` / `gradingResult.reason`），
其余全部丢弃。注意 `EvalLike`（`:40-58`）是**故意收窄**的结构类型：它声明了
`response.output` 却从没读过，`prompt` / `vars` 根本没声明 —— 所以本项要先**扩类型**。

**取值优先级必须与 promptfoo 报告一致**（§7.3）：

```js
prompt = result.vars[injectVar]        // injectVar = config.redteam.injectVar ?? 'prompt'
      || result.vars.query
      || result.prompt.raw
output = result.response.output
```

新增一个逐条数组，暂名 `case_results`：

| 字段 | 内容 | 来源（promptfoo 结果对象） | 备注 |
|---|---|---|---|
| `index` | 第几条探针 | 遍历序 | |
| `plugin_id` | 插件 id | `metadata.pluginId` → `testCase.metadata.pluginId` | promptfoo 报告读的是前者，我们的归约器读的是后者，**实现时要在真 run 上确认哪个非空** |
| `strategy_id` | 策略 id | `metadata.strategyId` → `testCase.metadata.strategyId` → `'basic'` | 与 promptfoo 同一套回退链 |
| `severity` | 严重度 | `gradingResult.severity` | 沿用现有 `sanitizeSeverity` |
| `passed` | 该条是否攻击成功 | `gradingResult.pass` | 与现有 `attack_success` 同极性 |
| `score` | promptfoo 自己的判分（0~1） | `gradingResult.score` | **只在 `judge_track=engine` 时渲染**（§7.4） |
| `prompt` | **攻击载荷原文** | 上面的 `prompt` 链 | 不过 `redactReason`，不截断到 200 字 |
| `response` | **目标回答原文** | `result.response.output` | 同上 |
| `judge_reason` | 判定理由文本 | `gradingResult.reason` | **新走一条不经 `redactReason` 的路径**：现在它被截到 200 字、被模式清洗、**只对成功行保留、每插件只留 3 条**（`:440-444`）—— 那三件事都是「摘要」口径，逐条字段不能用 |

约束：
- 逐条**不进日志**、不进 `error_brief`、不进进度事件（`AGENTS.md:15` 仍然生效）。
  特别是 `createDiagnosticLogCollector`（`nativeRedteam.ts:858-892`）这条
  「把 promptfoo 日志行塞进错误消息」的通道 **必须保持关闭** —— 它是引擎里唯一一条
  文本外泄通道，本批不得因为「要明文」而放松它（那是错误诊断，不是报告正文）。
- 量级：`num_tests` 上限 50，最坏 50 ×（载荷 + 回答），几百 KB 量级，`JSONB` 装得下。
- 归约时必须**已经**取到原文 —— 终态之后 promptfoo 的库与 workdir 都被删了（§2.3）。
  勘察确认：promptfoo 自己会把 `prompt.raw` / `response.output` / `grading_result.reason`
  写进 `~/.promptfoo/promptfoo.db` 的 `eval_results`，把攻击载荷写进 `<workdir>/redteam.yaml`，
  两者都在终态被 `cleanupPromptfooRunArtifacts` 删掉（清理失败会把成功的 run 降级成
  `terminal_cleanup_failed`）。**所以我们不能靠「事后回读」，只能靠终态返回体。**

### 6.1b 引擎：多轮对话转录（与 promptfoo `Messages` 对齐）

多轮策略（`crescendo` / `goat` / `hydra` / `iterative*`）下，逐条卡片里的
`prompt` / `response` 只是**最后一轮**。promptfoo 的 `Risk Categories` 抽屉会渲染
**逐轮对话**，数据来自：

```js
result.metadata.redteamHistory      // [{prompt, output}, …]
result.metadata.redteamTreeHistory  // 树搜索族
```

本批把这条也带出来，作为逐条块的**子块**（不是新章节）：`case_results[i].turns`
= `[{role:'user', content}, {role:'assistant', content}]`，PDF 里在 `攻击载荷` / `目标回答`
之后逐轮列出。缺失时退回单轮形态（与 promptfoo 的 fallback 一致）。

> 勘察未确认：`metadata.pluginId`（结果级）与 `testCase.metadata.pluginId` 是否对每个插件
> 都同时存在且一致。实现时在真 run 上核一次，别照抄一边。

### 6.2 BFF：镜像字段并改成逐条 finding

- `internal/maclaw/promptfoo_engine_client.go` 的 `EngineSafeResult` 增加对应字段。
- `EngineSafeResultFindings`（`promptfoo_engine_bridge.go:642`）从「逐插件一条汇总」改为
  **逐条一条 finding**：`Title` = 攻击原文、`Description` = 目标回答原文、
  `Suggestion` = 判定理由、`Severity` / `Category` / `metadata` 沿用插件信息。
- **保留**逐插件汇总：改放到报告 metadata 或「评估指标」段，不占 findings 的位置
  —— 否则 §2.1 那种「插件 X 执行 N 条」的信息会消失。

### 6.3 引擎：把 `isAllowedResultField` 真正接上

现在是装饰性的（§2.4）。本批要：

1. 在归约函数的出口**真的**调用它；
2. 把 `generation_path`、`num_tests_effective`、`judge_sources` 补进白名单
   —— **不补就会把 P2-6 与 U8 洗掉**；
3. 把新增的 `case_results` 补进去，并在白名单注释里写明「这一项是有意的例外：它承载原文」。

这样白名单才从「看着像闸门」变成「真的是唯一出口」，而且它现在同时是**契约的可执行表述**。

### 6.4 前端：改文案，并把攻击原文也显示出来

- `ChatCards.tsx`：「展开攻击成功摘要（**脱敏**）」与「以上为**脱敏摘要**，完整攻击样例与目标回答见 PDF 报告」
  这两处文案必须改 —— 它现在显示的就是原始目标回答（§2.1），文案在说假话。
- 展开区目前只渲染 `finding.description`，要把 `finding.title`（攻击原文）也渲染出来。

### 6.5 BFF：改 `sanitizeRedteamArtifactText` 与长度上限

- 取消按标记短语整体替换（那 8 个 marker）。
- **保留**密钥清洗（`sk-` / Bearer / 邮箱 / URL）—— 属于 §4 的凭据类，不放开。
- 长度上限从 2000 字提高（建议 20000 字），否则长载荷与长回答会被切掉 —— 而「挖系统提示词」正是长回答的典型场景。

### 6.6 BFF：修「判定理由」渲染成 handle 的毛病

`successfulAttackExampleLines`（`redteam_artifact_service.go:739`）渲染的是
`finding.Evidence`，而批量链路给 `Evidence` 赋的是 **evidence handle**
（`redteam_bridge.go:1016` `Evidence: evidence.Handle`）—— 所以 PDF 第 4 段现在会打印一行
「判定理由：redteam_evidence_xxxxx」。真正的判定理由在 `finding.Suggestion` 里。
本批一并修：第 4 段打印判定理由文本，handle 只留在 metadata 里。

### 6.7 PDF：只有第 3 段要改，第 4 段不动

- **第 3 段「评估发现」：逐条列全**（对齐 promptfoo `Risk Categories` 的逐条手风琴），
  每条带攻击载荷原文 + 目标回答原文。**这是本批唯一要动的段**。
- **第 4 段「攻击成功样例」：保持现状，一个字不改**（`maxSuccessfulAttackExamples()` 不动）。
  **结构与条数都不动。**

> **第 4 段唯一的例外**（§6.6）：它那行 `判定理由` 现在打印的是**证据编号**
> （`redteam_evidence_xxxxx`），不是判定理由 —— 真正的理由在 `finding.Suggestion` 里。
> 这是 bug，不是设计，所以还是修。如果你希望这一批完全不碰第 4 段，说一声就不修。

决定与理由见 §11.2。

### 6.9 BFF：报告里补上策略信息（原计划漏了这条，2026-10-15 补）

**漏的是什么**：报告（PDF 与报告卡）**现在完全没有策略信息**。
`redteam_artifact_service.go` 里连 `strategy_stats` 这个词都不存在；
策略统计只活在 `engine_runs.result` 里，而前端唯一读它的地方是
`EngineEvalPage.tsx:148`，用途还只是**估算探针数**，并不展示。

所以「卡片显示了策略、报告里却找不到它」——即使把 §12.2 的接线补好、策略真的生效了，
**报告里依然看不到**。这条必须一起补，否则用户没法验证策略到底起没起作用。

**补两处**（都对齐 promptfoo，且都不新增章节 —— 填进 `AGENTS.md:55` 已有的段）：

| 位置 | 内容 | 对齐 promptfoo 的哪一块 |
|---|---|---|
| **逐条卡片**（第 3 段） | 每条加上它用的策略 | 它的逐条 CSV 有 `Strategy` 列；`Attack Methods` 抽屉的标题就是策略名 |
| **第 5 段「评估指标」** | 逐策略一块：策略名、探针数、攻击成功数、成功率 | 它的 `Attack Methods` 段：每策略一张卡（名称、进度条、`N%`、`N / M attacks succeeded`） |

**数据来源**：`EngineSafeResult.strategy_stats`（`promptfoo_engine_client.go:325`）
已经有 `id` / `label` / `probes` / `attack_success` / `success_rate` / `max_severity`，
不需要引擎侧再改 —— 只需要平台把它渲染出来。

**与 §6.1 的关系**：§6.1 的逐条数组里有 `strategy_id`，逐条卡片上的策略名由它渲染；
第 5 段的汇总由 `strategy_stats` 渲染。两处都靠已有数据，不新增引擎字段。

> **实测到的数据形状（服务器记录 §1 判据 5a/5b）**：没有策略时，`strategy_stats` **不是空数组**，
> 而是**一条 `id="none"` 的记录**（把无策略探针记在 `none` 名下）。
> 例如判据 5a（不选策略）得到单条 `none`（3 探针）；判据 5b（选 `base64`）得到
> `none`（3 探针）+ `base64`（3 探针）两条。
>
> 所以第 5 段渲染时 **`none` 不能当成一个策略名显示**——它应渲染成
> 「无策略（直接提问）N 条探针」，否则读者会以为有个叫 `none` 的攻击手法。
> 这一条同时适用于逐条卡片：`strategy_id="none"` 的条目应显示「未使用策略」。

**验收**：卡片上显示的策略名，能在报告第 5 段找到对应的那一块，且探针数与卡片一致。

### 6.8 文档

`AGENTS.md`（§3.1）、`PROJECT_GUIDE.md`（报告口径那一节）、
`maclaw-current-state.md`（报告与安全边界一节）、本文件。

---

## 7. PDF 对齐 promptfoo 原生报告

> 依据：对已安装 `promptfoo@0.123.0` 的 `dist` 做的一次只读勘察（未执行任何 run）。
> 引用的 SPA 内部标识符（`L3` / `wpe` / `fpe` 等）是压缩名，**重新压缩就会变**；
> 稳定的锚点是**字符串**（`"Risk Categories"` / `"Attack Methods"` /
> `"Vulnerabilities and Mitigations"` / `"Failure Reason"` …）与字段路径。

### 7.1 一个必须先说清的发现：promptfoo 的 redteam 报告**不是一个文件**

0.123.0 里有**两个完全不同的东西**都叫「报告」：

| | (a) `output.html` | (b) redteam 报告 |
|---|---|---|
| 是什么 | 通用 eval 结果表 | **服务端渲染的 SPA** |
| 谁写 | `writeOutput()`（`dist/src/util-*.js`） | **没有任何东西写出来**，由 promptfoo 的本地服务实时渲染 |
| 模板 | `dist/src/tableOutput.html`（自包含，内联 CSS/JS/base64 logo） | `dist/src/app/index.html` + 3.4 MB assets |
| 触发 | 配了 `outputPath` 才生成 | `promptfoo redteam report` = **打开浏览器**，不产出文件 |

**并且 0.123.0 全库没有服务端 PDF 生成**：报告里的「PDF」按钮就是 `window.print()`。

**对「对齐」的直接影响**：我们**不可能**「复用 promptfoo 的渲染器」或「照抄它的 HTML」——
它是活的 SPA。所以对齐只能是**词汇与字段层面的对齐**：让我们的固定中文 PDF 用它的
章节名、字段名、逐条卡片的形状。本节就是这个意义上的对齐。

### 7.2 promptfoo redteam 报告的章节顺序（作为我们的映射源）

| # | promptfoo 段 | 它显示什么 | 我们 PDF 的对应 |
|---|---|---|---|
| 1 | Header card | 标题（`Risk Assessment`）、时间、徽章：`Target` / `Depth: N probes` / `Total Tokens` / `Prompt` / `Tools` | 封面页 + 「报告基本信息」 |
| 2 | `Filters` | 搜索框 / 状态 / 风险类别 / 策略筛选（`print:hidden`） | **不对齐**（静态 PDF 没有交互；我们也没有多份报告可在页内筛选） |
| 3 | 严重度卡片行 | Critical / High / Medium / Low / Informational，每张一个计数 + `Vulnerability`/`Vulnerabilities` | 第 2 段「风险等级与安全评分」的 metric cards |
| 4 | `Attack Methods` | 每个策略一张卡：名称、描述、进度条、`N%`、`N / M attacks succeeded` | 第 5 段「评估指标」里的策略部分（我们已有 `strategy_stats`，只是没渲染成卡片） |
| 5 | `Risk Categories` | 右上整体 `N%` 与 `(passes / total tests defended)`；类别手风琴 → 插件行 | 第 3 段「评估发现」按插件分组 |
| 6 | `Vulnerabilities and Mitigations` | 表格：`Type` / `Description` / `Risk Score` / `Complexity` / `Successful Attacks` / `Total Tests` / `Attack Success Rate` / `Severity` / `Actions`，另有 `Export vulnerabilities to CSV` | 第 3 段的分组标题 + 第 5 段指标 |
| 7 | `Framework Compliance (n/m)` | `N% Attack Success Rate (f/t tests failed across K plugins)` + 每个框架一张卡 | **不对齐**（我们不做合规框架映射） |

### 7.3 🔴 逐条原文的**卡片形状**：这就是用户要的那个东西

promptfoo 的 `Attack Methods` 抽屉里，每个逐条卡片**只有两个字段**，原文如下：

```
Prompt:   [<插件名徽章>]
          <攻击载荷原文，等宽字体，保留换行>

Response:
          <目标回答原文，等宽字体，保留换行>
```

**刻意没有的东西**：通过/失败徽章、分数、grader 理由都不在这个卡片里
（分类信号只有插件徽章，以及你在哪个 tab）。

**这正好就是用户的验收标准**（「我去挖系统提示词你至少要把系统提示词给我展示出来」）——
系统提示词会出现在 `Response:` 那段里。所以：

> **我们的 findingCard 逐条块，形状对齐到这一段**：标签 `Prompt:` / `Response:`
> → 中文 `攻击载荷：` / `目标回答：`，正文保留换行、不做截断、等宽或至少保留原始空白。

它取字段的优先级（promptfoo 自己的代码）：

```js
prompt = result.vars[injectVar] || result.vars.query || result.prompt.raw
output = result.response.output
```

我们的引擎侧必须按**同样的优先级**取值（§6.1 已按此更新），否则同一份用例
在 promptfoo 报告与我们的 PDF 里会显示成两条不同的载荷。

**另外两处逐条视图**（供参考，本批不做）：

- `Risk Categories` 抽屉：每个失败探针一条手风琴，展开后有红色大写 **`Failure Reason`**
  标签 + `gradingResult.reason`，然后是**逐轮对话**（`metadata.redteamHistory[]`
  或 `redteamTreeHistory[]` 的 `{prompt, output}`）。
- 链接过去的通用 eval 视图 `/eval/<id>`：抽屉有 `Details`，内含
  `Actual Prompt Sent` / `Modified User Input (Red Team)` / `Evaluation`（断言表：
  `Pass` / `Score` / `Type` / `Value` / `Reason`）/ `Messages` / `Metadata` / `Traces`。

### 7.4 CSV 导出：promptfoo 自己认为「每条探针该有什么」

`Download report → CSV` 的列，是它对自己逐条字段最干净的一次枚举：

`Test ID` / `Plugin` / `Plugin ID` / `Strategy` / `Target` / `Prompt` / `Response` /
`Pass`（渲染成 `Pass (0.8)` / `Fail (0.2)`）/ `Score` / `Reason` / `Timestamp`

我们的 finding 字段集合与它**可以一一对应**：`ID` ↔ `Test ID`、`Category`/metadata ↔
`Plugin`/`Plugin ID`、`metadata.strategy_id` ↔ `Strategy`、`Title` ↔ `Prompt`、
`Description` ↔ `Response`、`Suggestion` ↔ `Reason`、`Severity` ↔ `Severity`、
`CreatedAt` ↔ `Timestamp`。

> **`Score` 那一列跟**（2026-10-15 用户裁定：「列进报告，如果说是通过 promptfoo 来进行评分的，
> 那就都对齐 promptfoo」）。规则是**按判定口径分档**，不是一个全局开关：
>
> | `judge_track` | 逐条展示 | 为什么 |
> |---|---|---|
> | `engine`（promptfoo 判的） | `Pass (0.8)` / `Fail (0.2)` + `Score` + `Reason`，**与 promptfoo 报告逐字段一致** | 这份报告的分数本来就是这个数算出来的，展示它才是**忠实**；用户的裁定就是这个意思 |
> | `platform`（平台 Judge 判的） | 只有 通过/不通过 + 理由，**无分数** | 平台那套 `score_0_to_5` 受 `AGENTS.md:46` 约束，且它与 promptfoo 的 score 不是同一个东西 |
>
> 这条口径与 U4 已有的 `judge_track` 概念**天然对齐**（`redteam_judge_track.go`），
> 不需要新概念：报告卡与 PDF 的「报告基本信息」段本来就在渲染 `judge_track` 说明。
> **两个分数永远不会出现在同一份报告里**，所以「读者分不清哪个是被禁的那个」这个问题不存在了。

### 7.5 逐条条数上限：我们保持现状

promptfoo 的抽屉每个 tab 只渲染 5 条，而它同时提供完整 CSV / JSON 导出与链接的单条详情页。
我们的第 3 段「评估发现」本来就会逐条列全（与它的手风琴一致），所以**不需要向它学那个 5 条**；
第 4 段「攻击成功样例」是速览，保持现状不动。

**结论：这一节没有要对齐的东西，也没有要改的代码。** 见 §11.2。

### 7.6 明确不对齐的部分（写出来防止实现者越界）

| 不对齐 | 原因 |
|---|---|
| `Filters` 段 | 静态 PDF 没有交互；且我们的报告是单次 run 的，没有「页面内筛选」的概念 |
| `Framework Compliance` 段 | 我们不做合规框架映射；且新增章节撞 `AGENTS.md:55` |
| `Total Tokens` 徽章的细分 tooltip（Attacker / Grading / Cached Savings …） | `token_usage` 在引擎侧恒为 0（未接线，`16` 的 D3 已裁定暂缓） |
| 多轮对话转录（`redteamHistory`） | 见 §6.1b —— 本批**要做**，但作为逐条块的一个子块，不新增章节 |
| promptfoo 的 SPA / HTML 报告本体 | 它是活的 SPA，不是文件；且它的浅色英文字体栈与我们固定中文模板不兼容 |

硬约束（不因对齐而变）：PDF 仍是固定中文模板 `redteam_report_pdf_layout_v2`，
不新增章节（`AGENTS.md:55`），中文字体走 `STSong-Light` / `UniGB-UCS2-H`。

---

## 8. 「明文」这条决定的运行时后果（写成行为，不是警告）

采纳 B-3 之后，平台**不再**拥有任何一处能拦住原文外流的地方。所以：

1. **报告 PDF 是唯一出口**，它一旦生成就无法撤回 —— 这不是本批要解决的，是本批要**接受**的。
2. 因此 §4 的那四类（凭据 / 路径 / 判定数值 / 进度元数据）是**仅剩**的禁区，必须逐条有测试。
3. 报告表不加密（本来也没有加密列）—— 用户明确不要，本批不新增。

---

## 9. 验证判据与实测结果（2026-10-16 回填）

> 服务器验证记录：`23-阶段四服务器验证记录-20261016.md`。
> 结果口径：**✅ 实测通过** / **⚠️ 以单测或机制证据替代** / **⏸️ 本批不测（已裁定）**。
> 「未测」一律**不记为通过** —— 这是上一批留下的纪律。

| # | 判据 | 期望 | 实测 |
|---|---|---|---|
| 1 | 引擎链路有攻击原文 | PDF 出现攻击载荷正文，不再只有「插件 X 共执行 N 条探针」 | ✅ Run A 报告 findings = **12 条逐条记录**（case-1…12），每条带载荷 / 目标回答 / 策略 id / 判定理由 / severity |
| 2 | 攻击结果不是总结（用户原话） | 能看到目标实际回答的正文 | ✅ 逐条「目标回答」为**全文原文**（逐字可见）。⚠️ 本轮 12/12 攻击未成功，故没有「泄露系统提示词」的实例 —— 按 §0.2 口径**这不是缺口**：用户要的是「看得见载荷」，成功与否取决于方法与靶子 |
| 3 | 批量链路未被破坏 | 第 3 段仍有原文 | ⏸️ **测不了**：本部署没有批量链路的历史报告可对比。**并更正一处**：本批确实改了**两条链路共用的** `sanitizeRedteamArtifactText`，所以批量链路的报告文本行为**变了**（含 `raw prompt` 这类短语的文本以前会被整体替换，现在不会）—— 那是刻意的修复，但它是行为变更，不是「没碰」 |
| 4 | 凭据不出现在报告 | 报告里 `sk-…` / `bearer …` 被清洗 | ✅ 导出 markdown 中凭据形状命中 **0** |
| 5 | 进度元数据干净 | `stage_durations` 无正文 | ✅ Run A 的 `stage_durations = {}` |
| 6a | 平台判定数值不落库 | 报告无任何分数 | ⚠️ **无样本**：本部署没有 `judge_track=platform` 的引擎报告（向导不编译报告）。以新增单测覆盖 |
| 6b | 引擎判定分数按 promptfoo 口径展示 | 逐条出现判定理由与分数 | ✅ 逐条出现 `判定理由：All assertions passed`，报告口径行为「promptfoo 引擎（engine）」 |
| 6c | 两种分数不同时出现 | 一份报告只有一种口径 | ✅ Run A 只有 engine 一种 |
| 7 | 白名单真的接线了 | 删字段重跑 → 报告少一块 | ⚠️ 服务器侧反证需改代码（超出本轮纪律）。以 commit 内的反证单测为据 |
| 8 | 体积与渲染可接受 | 记录 JSON 大小与耗时 | ✅ 12 探针报告 markdown **12.7 KB**（≈1 KB/条）。`num_tests=50` 未跑 —— 该靶子单探针 10–60s，50 探针预计 40+ 分钟；按 1 KB/条外推 ≈ 60–80 KB，在 20000 字清洗上限内 |
| 9a | 逐条块形状对齐 | `攻击载荷：` / `目标回答：` 两段，保留换行 | ✅ 导出块为 `攻击载荷：` / `目标回答：` / `判定理由：` / `攻击策略：`，多行回答**保留换行**，与 promptfoo `Prompt:`/`Response:` 同形 |
| 9b | 字段取值链对齐 | 两边显示同一载荷 | ✅（机制）逐条载荷与 `case_results.prompt` 一致，取值链同 §7.3。promptfoo 侧未单独导出对照（其 workdir 终态已删），以引擎 `case_results` 为准绳 |
| 9c | 章节映射符合 §7.2 | 有汇总段，无新增 `Filters`/`Framework Compliance` | ✅ 导出含严重度 / 策略 / 按插件分组段落；无新增章节 |
| 10 | 多轮转录 | 逐条块出现逐轮对话 | ⏸️ **本批不测（用户 2026-10-16 裁定）**。引擎侧**已实测**：wizard 的 `iterative:tree` run 的 `case_results` 带 `turns` 数组（逐轮 user/assistant 全文）。对话链路侧未测：maclaw 计划卡两次都只带插件不带多轮策略，且靶子能否被多轮攻击属于**后续规划**（§0.2 第 3 条） |
| 11 | 引擎不泄露到日志通道 | `error_brief` 无正文 | ✅ 失败 run 的 `error_brief` 为纯诊断文本（插件 0/6 计数 + 通用排障提示） |
| 12 | **报告里有策略信息**（含 §12.2 的 P1） | 策略被真的执行，且报告能看出 | ✅ **P1 确认修复**：计划卡带 `base64` → run `strategies=[{"id":"base64"}]` → `strategy_stats` 双条（`none` 6 + `base64` 6）→ 12/12 探针；逐条块带「攻击策略：」字段，与 `engine_runs.result.strategy_stats` 一致 |

### 9.0 实测带出的一处**必须修**的行为：带策略的 run 多跑了一遍「无策略」（2026-10-16 已修）

Run A 的 `strategy_stats` 是 `none` 6 + `base64` 6 —— 用户要 6 条，实际 12 条，
**其中一半没有施加任何攻击手法**。用户裁定：「我不要再跑一遍没策略的」。

**根因（对 promptfoo 0.123.0 dist 的勘察结论）**：策略应用是**叠加**而非替换，
`dist/src/index.js:17097`：

```js
const finalTestCases = [...includeBasicTests ? pluginTestCases : [], ...strategyTestCases];
```

**唯一的开关**：`basic` 策略的 `enabled` 标志
（`dist/src/index.js:16653-16655`，`includeBasicTests = basicStrategy?.config?.enabled ?? true`）。
没有 `baseline` / `control` / `noStrategy` 这类选项；在别的策略上设 `enabled:false` 无效
（两处读 `config.enabled` 都按 `strategy.id === 'basic'` 门控）。

**处置**：引擎在选中策略时追加 `{ id: 'basic', config: { enabled: false } }`
（`withBasicCasesDisabled`，接在 builder 里 —— 与 U8 的教训一致：接在调用点可被删掉而测试仍绿）。
未选策略时不追加（否则用例集为空，直接 `no_test_cases` 失败）。

**仍然存在的乘数**：S 个策略仍是 `S × num_tests` 条探针（每个策略各跑一遍插件用例）。
**没有除回去**，因为下限会变成 `Σfanout × S` —— `harmful`（fanout 26）配两个策略会超过
`EngineMaxNumTests=50` 而**无法提交**。所以口径定为：**一旦选了策略，`num_tests` 是「每个策略的上限」**，
并让前端把乘积显示出来（`EngineEvalPage` 的 `strategyMultiplier`）。`num_tests_effective`
按**productive** 策略数（排除 `basic`）相乘，所以报告里报的探针数与实际跑的一致。

### 9.1 实测带出的一条速度事实（用户侧预期管理）

Run A（修复前）12 探针（含 `base64`）confirm 全程 ≈ **11 分钟**；`iterative:tree` 4 探针 ≈ **30 分钟**。
耗时与「轮次 × 策略乘数」成正比。

**§9.0 修好之后这个数字要重算**：那 12 条里有 6 条是「无策略」的对照组，现在已经不跑了。
同一场景（6 条用例 + 1 个策略）应当是 6 条探针、约一半的时间。
剩下的乘数只有「策略个数」，且前端会把乘积显示出来 —— 用户不会再把它读成系统变慢。

### 9.2 三处证据缺口怎么补（不需要本批做）

| 判据 | 缺口原因 | 补法 |
|---|---|---|
| 3 | 本部署无批量链路历史报告 | 跑一次专家样本 / Skill 评测并导出一份，与改动前的报告对比 |
| 6a | 无 `judge_track=platform` 的引擎报告 | 批量链路（平台判定）的报告产出后即可核 |
| 7 | 服务器侧反证要改代码 | 已在 commit 内以单测反证；若要在服务器复现，需临时注释掉白名单里的一行 |

## 10. 本批不做

- 自选评测导航（向导）的主动改造 —— 用户明确排除（顺带影响见 §5 与 §11.1）。
- 加密、保留期清理、权限分级、导出拦截 —— 用户明确不要。
- 报告新增章节 —— `AGENTS.md:55` 禁止。promptfoo 报告里的 `Filters` 与
  `Framework Compliance` 两段也不对齐（§7.6）。
- 批量链路的**findings 构造**不改（它本来就在带原文）。⚠️ 但共用的清洗器改了 ——
  见 §9 判据 3 的更正。
- ~~maclaw 侧计划卡写入策略字段~~ —— **这个依赖不存在**（§12.5，2026-10-16 修正）：
  maclaw 已经原样回传了平台自己发出的 `promptfoo_strategy:` ref。
- `isAllowedResultField` 之外其它「看着像护栏」的东西本批不逐一审计。

### 10.1 明确撤下与延后的三项（2026-10-16 用户裁定）

| 项 | 处置 | 理由 |
|---|---|---|
| **confirm 前的生成模型连通性预检** | **撤销** | 用户原话「网络的那个不需要管，如果失败了我自己会把网络的弄开」。原先记的这条建议作废。顺带记一笔以便日后回看：这暴露了一处不一致 —— `AGENTS.md` 要求 Skill 链路做连通性预检，promptfoo 引擎链路没有；**本批不补，但这是同一类依赖上只装了一半的闸** |
| **追一次「攻击成功」的实例** | **不需要** | 用户原话「我觉得能让我看见攻击载荷就行，成功毕竟是要看方法」。见 §0.2 第 1 条 |
| **对话链路的多轮策略可达性** | **延后到规划** | 用户原话「我目前被攻击的靶子没有弄得很好……这个是要后面进行计划才能决定，测试可以先不测试这个」。实现已上线且引擎侧实测有 `turns`；缺的是计划卡不带多轮策略 + 靶子能否被多轮攻击（两个都要规划，不是本批的 bug） |

### 10.2 下一批候选

| 项 | 说明 |
|---|---|
| **§12 聊天确认卡片**：显示已选策略 + × 删单个插件/策略 | 用户 2026-10-15 提的需求，本批明确留下。ref 形态已由 §12.5 澄清，可独立做、独立验 |
| 多轮策略在对话链路的可达性 | 需要先回答「靶子能不能被多轮攻击」（规划问题），再动 maclaw 计划卡或目标配置 |
| §9.2 的三处证据缺口 | 都依赖「跑一次批量链路评测」，可以顺手一起补 |

---

## 11. 决策记录（2026-10-15）

| # | 问题 | 裁定 | 落到哪 |
|---|---|---|---|
| 1 | 自选评测页面的报告要不要也带原文 | **跟着一起变** | §6.2 不分流；`EngineSafeResultFindings` 直接改，不看 `run.source` |
| 2 | 「攻击成功样例」那一段列几条 | **照旧，一个字不改**（这条本来就不该问，见 §11.2） | 只有 §6.7 的第 3 段要改 |
| 3 | 要不要印 promptfoo 的分数 | **列进报告；promptfoo 判分的就都对齐 promptfoo** | §7.4 —— 规则升级为「展示口径跟随 `judge_track`」 |
| 4 | 多轮攻击要不要印每一轮 | **暂时每一轮都印** | §6.1b，本批做 |

---

### 11.1 已定：不分流，向导也带原文

理由（我原来的建议，用户采纳）：两种形态并存会让「这份报告里有没有原文」变成需要猜的事。
实现上就是**不加任何 `run.source` 判断**。

### 11.2 已定（2026-10-15）：第 3 段列全，第 4 段**保持现状不动**

> **这一条一开始就不该问。** 我看到报告里有两段都在列攻击内容、第 4 段是第 3 段的成功子集，
> 于是把它当成一个「要改什么」的选择题交给用户。用户连问三遍，最后一句是
> 「你就照着之前的，不需要进行任何修改不就好了？」—— 对的，现状本来就没问题，
> **是我想多了**。

**决定**：

- **第 3 段「评估发现」：列全部测试项**（这是本批必须做的：原文要进来），
  每条带 `攻击载荷原文` + `目标回答原文`（+ 多轮转录，§6.1b）。
- **第 4 段「攻击成功样例」：保持现状，一个字不改** —— 仍只列成功项、仍按现有条数上限。
  `maxSuccessfulAttackExamples()` 不动。

**为什么现状就够**：第 4 段是「哪些真的打穿了」的短清单（速览），
第 3 段是完整台账。速览本来就该短 —— 把速览也拉长不会让人多看到什么，
只会让报告变厚。用户要的「看得到原文」由第 3 段满足。

### 11.3 已定：分数列进报告，按 `judge_track` 分档

用户原话：「列进报告，如果说是通过 promptfoo 来进行评分的，那就都对齐 promptfoo」。

这条把问题解决得比我原来的方案更干净：**不是「印不印」，而是「按谁判的分决定展示什么」**。

| `judge_track` | 逐条展示 | 说明 |
|---|---|---|
| `engine` | `Pass (0.8)` / `Fail (0.2)` + Score + Reason | promptfoo 判的分，报告里就展示 promptfoo 的数字，**逐字段对齐它的报告** |
| `platform` | 只有 通过/不通过 + 理由 | 平台 Judge 的 `score_0_to_5` 仍禁（`AGENTS.md:46`） |

详细规则见 §7.4。这条不需要新概念 —— U4 的 `judge_track` 就是干这个的。

### 11.4 已定：多轮每一轮都印（实现已上线；对话链路可达性延后）

用户原话：「暂时每一轮都印进去吧」。落点 §6.1b：`case_results[i].turns`
取 `metadata.redteamHistory` / `redteamTreeHistory`，PDF 逐条块里在
`攻击载荷` / `目标回答` 之后逐轮列出。

**2026-10-16 实测**：引擎侧成立 —— wizard 的 `iterative:tree` run 的 `case_results`
带 `turns` 数组（逐轮 user/assistant 全文）。但**对话链路拿不到多轮策略**：
两次请求的计划卡都只带插件不带多轮策略，且靶子能否被多轮攻击尚无结论（§10.1）。
用户裁定**本批不测**，属于后续规划。

---

## 12. 聊天确认卡片：显示已选策略 + 可删掉单个插件/策略

> 用户 2026-10-15 追加的需求，原话：
> 「我还希望聊天的时候，生成的卡片也会显示选择策略有哪些，如果可以的话，
> 策略和选项的小卡片中应该有小叉，可以决定不要某个插件或者策略」
>
> **2026-10-15 范围澄清（用户）**：「我提的所有要求都是关于 promptfoo 这边的，
> skill 那边你不需要进行任何修改」。
>
> 所以本节**只管 promptfoo 引擎这条链路**：
>
> | 管 | 不管 |
> |---|---|
> | promptfoo **插件**标签（`promptfoo_plugin:<id>`）的显示与删除 | **Skill 标签**（`selected_skills`）—— 一个字节都不动 |
> | promptfoo **攻击策略**标签的显示与删除 | **专家样本 / 模板 / 组合攻击**标签（`selected_capability_refs`、`resource_handles`）—— 不动 |
> | 引擎快速通道（`confirmPromptfooEnginePlan`）的确认请求 | maclaw agent 循环那条链路（`prepareSelectedCapabilities` 走的那条）—— 不动 |
>
> 这条澄清把本节的复杂度砍掉了一大半：**没有 Skill 就意味着一路上没有
> `manage_skill(run)` → `register_skill_payload_dataset` → payload handle 那条链**，
> 删除只影响引擎请求里的两个 id 数组。

### 12.1 现在这张卡片长什么样（实测）

`PlanConfirmCard`（`frontend/src/pages/enterprise/ChatCards.tsx:156-258`）现在显示：

| 行 | 内容 | 可编辑？ |
|---|---|---|
| 任务名称 / 评估目标 / 目标类型 / 目标地址 / 模型 | 文字 | 否 |
| 资源模式 | 文字 | 否 |
| **选择策略** | **注意：这一行是「资源选择策略」，不是攻击策略**（`selection_strategy`，值是 `auto` / `skill_generated` 之类） | 否 |
| 评估类型 | 小标签（chips） | 否 |
| **使用能力** | 小标签，来自计划负载的 `selected_skills` + `selected_capability_refs` + `resource_handles` | 否 |
| 选择理由 | 文字（最多 3 条） | 否 |
| 测试问题数 | 数字输入框 | **是**（`min=1 max=2000`） |
| 确认执行 | 按钮 | — |

### 12.1b 策略其实**已经在卡片上了**，只是一串没人翻译的原文（2026-10-16 修正）

上面那张表最后一行的「使用能力」标签，数据来自计划负载的 `selected_capability_refs`。
**策略就在这里，以 ref 字符串的形式**——服务器验证记录（`21-阶段三服务器验证记录-20261015.md` §3）
实测到的计划卡内容是：

```
selected_capability_refs = ["promptfoo_plugin:prompt-extraction", "promptfoo_strategy:base64"]
```

而卡片的渲染函数 `displayCapabilityName`（`ChatCards.tsx:144-155`）**没有这两个前缀的分支**：

```js
if (lower.includes('ccbos') || …) return 'CCBOS 文言文改写 Skill'
if (lower.startsWith('skillhub:'))  return 'Hub Skill: …'
if (lower.startsWith('sample:'))    return `专家样本 …`
if (lower.startsWith('template:'))  return `专家模板 …`
if (lower.startsWith('composed_attack:')) return `已组合攻击 …`
return text          // ← promptfoo_plugin: / promptfoo_strategy: 落在这里，原样显示
```

**所以用户现在看到的，是两串内部标记原文**：`promptfoo_plugin:prompt-extraction` 和
`promptfoo_strategy:base64`。技术上「显示了」，但对读的人来说等于没显示 ——
既看不出哪个是插件哪个是策略，也看不出 `base64` 到底是什么手法。

**这修正了我上一版文档的一处错误判断**：我原来说「计划卡的数据结构里根本没有攻击策略字段」。
准确的说法是：

> 策略**在**数据结构里（`PlanInfo.selected_capability_refs: string[]`，`services/chat.ts:48`），
> 只是**没有任何一层把它当作策略对待** —— 前端不翻译、不分组，后端不解析、不转发。

三件事直接对应用户的观察：

1. **卡片上「攻击策略」和「攻击插件」混在同一组标签里，且都是内部标记原文。**
   另外表里那行写「选择策略」的其实是**资源选择策略**（`selection_strategy`），
   与攻击策略毫无关系 —— 这个命名本身也该改（建议改成「资源选取方式」），否则
   卡片上会同时出现两个都叫「策略」的东西。
2. **所有标签都不能删。** 确认请求只带 `test_count` 和 `plan_message_id`
   （`maclawRuntimeConfirmInput`，`internal/api/handler/maclaw_runtime.go:42-46`）。
3. **确认时选择是「从计划卡里读」的，客户端说了不算**：
   `ConfirmPlan`（`:314`）用 `selectedCapabilityRefsFromContent(planMessage.Content)` 和
   `promptfooEngineRefsFromPlan(planMessage.Content)` 从**计划消息**里抽选择。

### 12.2 已由服务器实测确认的 P1：策略在 confirm 路径被静默丢弃

**这一条不再是代码推断，而是实测结论。**
`21-阶段三服务器验证记录-20261015.md` §3 记了一个 P1：

- 计划卡 `selected_capability_refs = ["promptfoo_plugin:prompt-extraction", "promptfoo_strategy:base64"]`；
- confirm 之后的引擎 run `er-ccc1f0e1` **`strategies = []`**，`strategy_stats` 只有 `none`（6 探针）
  —— **`base64` 没有被执行**；
- 对照：自选评测（wizard）链路同样选 `base64`（判据 5b）**正常施加**。

**根因**：`promptfooEngineRefsFromPlan`（`internal/api/handler/engine_confirm.go:43-44`）
只解析 `promptfoo_plugin:` 前缀：

```go
if strings.HasPrefix(lower, "promptfoo_plugin:") {
    plugins[...] = true
}
```

`promptfoo_strategy:` 没有分支，**静默跳过**。随后 `confirmPromptfooEnginePlan`（`:115-124`）
构造 `PromptfooEngineRunInput` 时也只传 `Plugins`、不传 `Strategies`。

**影响**：用户在对话评测里确认的计划若选了策略，实际执行的是「无策略直问」——
**攻击面被静默收窄，用户无从知晓**。这正是 P3-1 宣称消灭的「选了却被静默丢弃」形态，
只是发生在 confirm 路径而不是 wizard 路径。

> **还有第二层断裂（2026-10-15 代码补查，服务器未涉及）**：报告里**根本没有渲染策略的地方**。
> `redteam_artifact_service.go` 全文不含 `strategy_stats` 这个词，策略统计只活在
> `engine_runs.result` 里，前端唯一读它的是 `EngineEvalPage.tsx:148`，用途还只是**估算探针数**。
> 也就是说：**即使把上面两处接线补好、策略真的生效了，报告里依然一片空白。**
> 所以 §6.9 是本节的必要配套 —— 否则「卡片上显示了策略」反而变成一句无法验证的话。

**本批的处置**（与服务器记录 §3 的修复建议一致，并且更严）—— **已实现（2026-10-16）**：

| # | 项 | 落点 |
|---|---|---|
| 1 | 提取 `promptfoo_strategy:` refs | `promptfooEnginePlanSelectionFromContent`（原 `promptfooEngineRefsFromPlan` 只认插件前缀，已替换） |
| 2 | 传入 `EngineRunRequest.Strategies` | `confirmPromptfooEnginePlan` → `PromptfooEngineRunInput{Plugins, Strategies}` |
| 3 | **fail-closed 两道闸** | `promptfooEnginePlanRefusal`（纯函数，可单测）：①`promptfoo_plan_has_no_plugins`——没有插件就没有用例可生成；②`promptfoo_strategy_refs_unresolved`——计划声明了策略却一个都解析不出来。两者都返回 409 |

**测试**：`engine_confirm_test.go` 六个用例，其中
`TestPromptfooEnginePlanSelectionKeepsStrategyRefs` 直接钉住**服务器实测的那份 payload**
（`["promptfoo_plugin:prompt-extraction","promptfoo_strategy:base64"]` → strategies=[base64]）。
反证已跑：删掉 `promptfoo_strategy:` 分支 → 4 个用例变红（含那条回归测试）。

> **这条独立于用户的新需求也值得单独修。** 而且用户要的「卡片显示已选策略」正好会把它暴露出来：
> 卡片上显示出策略、跑完报告里却没有该策略的统计，矛盾会立刻可见。

### 12.3 要做什么

| # | 项 | 说明 |
|---|---|---|
| 1 | 卡片把 `promptfoo_plugin:` / `promptfoo_strategy:` 两类 ref **翻译成人话并分组** | 数据**已经在** `selected_capability_refs` 里（服务器实测），缺的只是解析与展示（§12.1b）。策略一组、插件一组，各自显示可读名字。**只做这两组**，Skill / 样本标签不动 |
| 2 | **promptfoo 插件与策略**的标签加 × | 只在**未确认**时可点；确认后（或运行中）不可改 —— 与「测试问题数」现在 `disabled={confirmed}` 的做法一致。其余标签不加 × |
| 3 | 「选择策略」那一行改名 | 改为「资源选取方式」，避免与攻击策略混淆 |
| 4 | 确认请求带上**最终选择** | `maclawRuntimeConfirmInput` 增加可选的插件 / 策略字段 |
| 5 | 服务端**只允许删、不允许加** | 见 §12.4 |
| 6 | 删完之后**重算并重校验** | 见 §12.4 的「删完之后」 |
| 7 | 「测试问题数」的提示随标签联动重算 | 它现在根据选中插件展开后的子插件数给提示；删掉插件后这个数要跟着变 |

### 12.4 安全边界：计划卡是授权来源，编辑只能做减法

这是这一节最要紧的一条。

现在的信任模型是：**计划卡（由 maclaw 生成）是「允许做什么」的唯一来源**，客户端碰不到它。
一旦允许客户端改选择，就必须回答：客户端能不能**加**一个计划里没有的能力？

**不能。** 所以请求里的选择**必须与计划卡的选择取交集**，而不是直接替换：

- 客户端送来 `plugins` / `strategies` → 服务端先与计划卡里的集合求交集，
  交集之外的一律丢弃（并记一条日志）；
- 等价且更简单的做法：请求只带**移除列表**（`removed_refs`），服务端从计划卡的集合里减。

我建议**后者**，理由是它让「只能做减法」成为请求本身的形状 ——
一个能表达「加」的字段，早晚会被某条代码路径用来加。

**为什么这是一条硬边界而不是洁癖**：计划卡决定了这次执行被授权了哪些能力，
而执行授权（grant）是按计划卡绑定的。允许客户端加能力，等于让一个持有 grant 的浏览器
去跑计划里没有的能力 —— 那正是这个项目在 MCP 授权上一直在防的事（见 `AGENTS.md` 的
grant / plan_confirm 相关条款）。

### 12.5 跨仓依赖：**不存在**（2026-10-16 修正）

上一版我写的是「需要 maclaw 把策略写进计划卡，这是另一仓的事」。
**这是错的。** 服务器实测显示 maclaw 已经把策略 ref 写进计划卡了
（`promptfoo_strategy:base64`，见 §12.2）。追一下 ref 是从哪来的：

| 环节 | 位置 | 现状 |
|---|---|---|
| 平台**生成**策略能力卡，ref 就叫 `promptfoo_strategy:<id>` | `internal/maclaw/promptfoo_plugin_catalog.go:662` `SourceRef: "promptfoo_strategy:" + entry.ID` | ✅ 已有 |
| maclaw 调用 `search_platform_redteam_capabilities` 拿到这些卡 | 平台 MCP 工具 | ✅ 已有（`promptfoo_engine_bridge_test.go:31-37` 断言了卡的 ref 形态） |
| maclaw 把选中的 ref 写进计划卡的 `selected_capability_refs` | maclaw 侧 | ✅ **已有**（实测） |
| BFF 解析 `promptfoo_strategy:` 并转发 | `engine_confirm.go:43-44` | ❌ **断在这里** |

**所以整条链是平台自己的契约，没有跨仓依赖** —— 平台发出 ref、maclaw 原样回传、
平台自己不认自己发出的 ref。这是本批最应该修的一环，而且完全在本仓内。

**这也说明本批没有需要等待 maclaw 的部分**：§12 的全部工作都在 `evaluating_platform` 里。

### 12.6 「删掉」在这一批里只剩两种语义

因为范围收窄到 promptfoo 这一条链路（§12 抬头），删除只影响引擎请求里的两个 id 数组：

- **删插件**：从 `PromptfooEngineRunInput.Plugins` 里去掉。
- **删策略**：从 `PromptfooEngineRunInput.Strategies` 里去掉（而策略本来就要先接通 —— §12.2）。
- **删到空**：插件被删光时必须**拒绝确认**并说明原因，不能提交一个没有攻击面的 run。
  （这正是「插件是攻击面的唯一来源」的直接推论：策略只是包装，没有插件就没有用例。）

**不涉及的**（因为范围明确了）：

- **Skill 标签** —— 本批不给 ×，行为完全不变。
- **专家样本 / 模板 / 组合攻击标签** —— 同上。
- 因此也**不需要**处理「Skill 被删掉但它的 payload handle 还在等注册」这类半成品状态 ——
  那是只有 Skill 可删时才会出现的问题。

**其它标签保持原样**：只有 promptfoo 插件与策略这两组带 ×，其余标签仍是只读展示。
这样才能让「带 × 的就是可以删的」成为一条可读的规则。

### 12.7 验收判据

| # | 判据 | 期望 |
|---|---|---|
| 1 | 卡片显示攻击策略 | 计划卡选了两个策略 → 卡片上出现两个策略标签（不是「选择策略」那一行） |
| 2 | 能删单个插件 | 点 × → 标签消失；提交后引擎收到的插件集合里没有它 |
| 3 | 能删单个策略 | 同上；报告的策略统计里没有它 |
| 4 | 删完重算提示 | 删掉 `harmful`（展开 26 个子插件）后，「测试问题数」的下限提示跟着下降 |
| 5 | 确认后不可改 | 确认后 × 消失或不可点 |
| 6 | **不能加** | 手工构造一个带计划外插件的请求 → 服务端求交集后**不执行**它，且不报 500 |
| 7 | **不能删空** | 删光插件后确认 → 明确拒绝 + 原因，不产生 run 行 |
| 8 | **策略真的被用了**（§12.2） | 卡片上显示的策略，在报告第 5 段能找到对应的那一块，且探针数与卡片一致（需要 §6.9 先补上） |
| 9 | **Skill / 样本那条链路零回归** | 计划卡里同时有 Skill 与 promptfoo 插件时：Skill 标签**没有 ×**、仍走原链路、`selected_skills` 一字不改；确认结果与改动前一致 |
