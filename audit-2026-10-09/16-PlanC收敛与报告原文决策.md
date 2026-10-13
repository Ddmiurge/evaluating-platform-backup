# 16 · Plan C 收敛与报告原文决策（**实施计划 · D1~D4 已定，D5 待答**）

> 登记：2026-10-13｜架构师：高见远
> 性质：**计划文档，本批不改任何代码。** 用户看完§6 决策清单后再决定是否执行。
> 输入：`audit-2026-10-13-attack-generation-architecture.md`、`audit-2026-10-13-report-detail-boundary.md`
> 纪律：本文所有数字与代码位置均为**本轮实测复核**所得，与上游笔记有出入处**已在正文标注**。
>
> **2026-10-13 二次更新（用户决策回写）**：
> - ✅ **D1 = B**（原文进报告 PDF / 报告卡片）→ 见 **§5.0-A**
> - ✅ **D2 = 三段式** → 见 **§3.2 / §3.3 / §3.4**，实测清单见 **§3.2.1**
> - ✅ **D4 = 用户方案**（前端不显示 + 规划时选不到）→ 见 **§6-A**
> - 🔶 **D5 仍未决**：D1=B 排除 (a)/(d) 作主路线，实际是在 **B-1/B-2/B-3** 间选择
> - ⏸️ **D3 暂缓**（`token_usage` 本批不修）
> - **当前唯一待用户回答的问题**：§5.0-A.4 选 B-1 / B-2 / B-3
> - **本轮新发现的地雷**：`direct` 是表单默认策略且属 `plan_a_only`（§6-A.2），
>   D4 实施前**必须先修**，否则改了会坏


---

## 0. 冲突声明（**本计划最重要的一段**）

### 0.1 诉求 2 与 `AGENTS.md` 的硬约束直接冲突

用户诉求 2 是「让报告能在对话框中显现，评估能否让原文出现在报告之中」。
本文核实了这条诉求的**两个可能目标**，发现它们的冲突程度完全不同：

| # | `AGENTS.md` 条款（实测定位） | 禁止的东西 |
|---|---|---|
| C1 | L47：`save_redteam_evidence` / `compile_redteam_report` 在 `maclaw_redteam_evidence` / `maclaw_redteam_reports` 中**只持久化安全摘要与元数据**；**原始目标提示词、原始目标回答、payload 正文、凭据、token、本地路径一律不得**出现在这两张表 | 报告表 + 证据表 |
| C2 | L47末：上述内容**一律不得**出现在…**报告 DTO、浏览器响应或日志**里 | 报告 DTO + 浏览器响应 + 日志 |
| C3 | L15 / L49：`planned_count` / `executed_count` / `current_stage` / `stage_durations_json` 等进度元数据**不得**包含 prompts、payloads、raw target responses、credentials、tokens、evidence content、local paths | 进度元数据 |
| C4 | L46 末：`judge_attempt` 的 `attack_score` / `refusal_detected` 等原始值**必须永不**被记录、持久化、回浏览器、复制进报告 DTO 或证据/报告表 | 判定原始值 |
| C5 | L64：浏览器 DTO 只能拿到**安全元数据**（`payload_modality` / `image_count` / `image_mime_types`） | 浏览器 DTO |
| C6 | L23 / L24：payload 原文**只**作为服务端临时 handle 存在，**不得**返回浏览器或存进报告/证据 | payload 原文 |

代码侧同一条边界的落点（实测）：

- `migrations/026_promptfoo_engine.sql:1-7` 头注释：「No target prompts, responses, payloads, credentials, or engine-local paths are ever stored here.」
- `migrations/022_maclaw_redteam_artifacts.sql`：`maclaw_redteam_reports` 只有
  `summary / risk_level / safety_score / findings(JSONB) / evidence_handles / metadata`，
  **没有任何存放逐条原文的列**
- `internal/maclaw/redteam_artifact_service.go:264-290` `sanitizeRedteamArtifactText()`：
  命中 `raw payload` / `raw prompt` / `raw response` / `target response body` / `local path`
  等标记即整体替换为 `[redacted sensitive text]`
- `promptfoo_engine/src/types.ts:255-326` `SafeRunResult`：只有 counts / severity /
  plugin_stats / strategy_stats / risk_categories / judge_sources / tool_call_signals /
  token_usage，**结构上就没有逐条原文的位置**
- `promptfoo_engine/src/runner/redteamRun.ts:28` 模块头：「Raw prompts/responses never leave this module.」

### 0.2 我的判断（必须让用户先看到这一句）

**诉求 2 与 C1~C6 全部冲突。这不是"打开一个开关"，是修改项目级安全契约。**

但**冲突的严重程度取决于用户说的"对话框"是哪一个**——这是本文 §5 要拆开的东西，也是
我建议**不要一次做完**的原因：

- 如果指**报告 PDF / 报告卡片**（→ 进报告存储）→ 撞C1 + C2，**必须改契约**。
- 如果指**聊天对话框里临时看一眼本次运行的原文**（→ 进浏览器响应）→ 撞 C2 + C5，
  **同样必须改契约**，但它有一个关键优势：**不落库**。不落库就不构成
  `audit` 报告 §5 点名的「敏感数据仓库」风险。

所以我的倾向是**分段走**：先做不落库的形态（§5.1），把契约改动压到最小；
落库的形态（§5.2/§5.3）等用户明确要"留存"时再单独决策。

**我不会在契约未改的情况下实现任何一种「原文回浏览器 / 原文进报告」。**
任何声称"顺手就能加"的实现都会绕过 C1~C6，那是本项目明确拒绝的失败形状。

---

## 1. 现状事实（全部本轮实测复核）

### 1.1 分支点（与上游笔记一致）

`promptfoo_engine/src/runner/redteamRun.ts:307-317`：

```ts
export async function executeRedteamRun(
  record: RunRecord, emit: Emit,
  options: { nativeGeneration: boolean; workdir: string },
): Promise<RunnerResult> {
  if (options.nativeGeneration) {
    const { executeNativeRedteamRun } = await import('./nativeRedteam.js');
    return executeNativeRedteamRun(record, emit, options.workdir);
  }
  return executePlanARedteamRun(record, emit);   // ← Plan A
}
```

### 1.2 开关链路（实测：`parseBool` 默认 false）

| 位置 | 事实 |
|---|---|
| `src/env.ts:33-36` | `parseBool`：`!v` → false；只认 `1/true/yes/on` |
| `src/env.ts:89` | `NATIVE_GENERATION_SWITCH = 'PROMPTFOO_ENGINE_NATIVE_GENERATION'` |
| `src/env.ts:99-101` | `nativeGenerationEnabled()` = `parseBool(env[SWITCH])` |
| `src/env.ts:84` | `loadEnv()` 写入 `EngineEnv.nativeGeneration` |
| `src/server.ts:46` | 传给 `executeRedteamRun` |
| `src/server.ts:60-62` | **`onTerminal` 清理钩子只在 `env.nativeGeneration` 为真时挂载** |

> **部署事实复核**：compose / Dockerfile / `.env` 里**没有任何一处**设置该变量
> （全仓 grep 只命中源码、测试与文档）。故当前部署 = **Plan A**，与上游笔记一致。

### 1.3 Plan A 做什么（`src/runner/generateTests.ts`，189 行）

一次 `chat/completions` 调用（`generateTests.ts:93-114`），system 提示是
「你是大模型安全评测平台的红队专家…」，user 提示把 `plugin_id`当**类别标签**列出
（`generateTests.ts:85`：`攻击类别（plugin_id 只能从以下选择）：…`），
并要求「尽量均匀覆盖各攻击类别」（`generateTests.ts:87`）。

配比由 LLM 自行决定；`parseCases`（`:159-189`）只做 JSON 解析 + 白名单纠正，
**不做任何均分**。promptfoo 在这条路径上只被调用 `promptfoo.evaluate()`
（`redteamRun.ts:481`），`testSuite` 里**没有 redteam 段、没有 plugins**（`:456-471`）。

### 1.4 Plan C 做什么（`src/runner/nativeRedteam.ts`，924 行）

`promptfoo.redteam.run({ config: <per-run yaml>, force: true, strict: true, … })`
（`:839-860`），配置文件由 `buildNativeRedteamConfig`（`:508-635`）写进 per-run workdir。

四道 fail-closed 闸门（实测都在）：

| 闸门 | 位置 | 作用 |
|---|---|---|
| `assertNativeSlots` | `:144-179` | 缺 `provider` →拒（否则 promptfoo 静默用 OpenAI 默认） |
| `assertSafeProviderUrl` | `:187-205` | 禁6 个厂商域名 |
| `assertNativeStrategyMapIsCurrent` | `nativeStrategyMap.ts:216-226` | 策略表漂移 → 拒启动 |
| `strict: true` + `undefined` 归零 | `:846`、`:871-876`、`:901-906` | 0 用例 / 0 可判定 → 拒，**不报绿色成功** |

---

## 2. 诉求 1 · 移除 Plan A 的真实影响面（实测）

### 2.1 引用点逐符号统计

| 符号 | 定义处 | 生产引用 | 测试引用 | 合计 |
|---|---|---|---|---|
| `executePlanARedteamRun` | `redteamRun.ts:378` | 1（`:316` 分支） | 1（`u8BothPaths.e2e.test.ts:27,165`） | **2** |
| `generateAttackTests` | `generateTests.ts:54` | 1（`redteamRun.ts:429`） | 0（测试只测 `parseCases`） | **1** |
| `PLAN_A_ERR_CUSTOM_PLUGIN` | `redteamRun.ts:325` | 3（同文件 `:325/:340` + `types.ts:117` 注释） | 2（`u10CustomPlugin.e2e.test.ts:323,330`断言字符串字面量） | **5** |
| `PLAN_A_ERR_CUSTOM_STRATEGY` | `redteamRun.ts:351` | 3（同文件 `:351/:365` + `types.ts:133` 注释） | 0 | **3** |
| `assertNoCustomPluginsOnPlanA` | `redteamRun.ts:336` | 1（`:393`） | 0（测试**只断言错误码**，不 import 该函数） | **1** |
| `assertNoCustomStrategiesOnPlanA` | `redteamRun.ts:361` | 1（`:397`） | 0 | **1** |

**关键结论：Plan A 的引用面极小——13 个引用点，其中 8 个在 `redteamRun.ts` 内部。**
这是一次**局部删除**，不是架构手术。这是支持用户诉求 1 的最强论据。

### 2.2 瘦身测算（实测行号）

| 区块 | 行范围 | 行数 |
|---|---|---|
| Plan A 守卫块（2 个错误码 + 2 个 assert 函数 + 注释） | `319-370` | **52** |
| `executePlanARedteamRun` | `372-507` | **136** |
| 合计可删 | | **188** |
| 模块头Plan A 描述（`:4-16`，需改写而非全删） | | ~11 |

**`redteamRun.ts`：771 → 约 580 行**（再减去 `generateAttackTests` import 与
`EvalLike.response` 注释里提到 Plan A 的部分，约 **575~585**）。

连带删除：`generateTests.ts` **189 行整文件消失**——
但 `EngineRunError` 类被4 个文件依赖（`taskManager.ts`、`nativeRedteam.ts`、
`promptfooStateCleanup.ts` 及 3 个测试），**必须先把 `EngineRunError` 挪到
`errors.ts` 或 `runner/redteamRun.ts`**，否则会连带 7 个 import 断掉。
**这是本批唯一的真实技术风险点。**

### 2.3 开关删or 留 —— 我的判断

**结论：保留开关，但把语义反过来 —— 从"选路径"改成"允许回滚到 Plan A"，且默认 true。**

三个选项对比：

| 方案 | 优点 | 代价 | 我的判断 |
|---|---|---|---|
| **删掉开关** | 代码最干净；无"两条路径"这个心智负担；`env.ts` 少 40 行 | **没有回滚路径**。Plan C 一旦在真实模型上出问题（`15` §2 的能力矩阵**完全未测**），只能 revert 代码 + 重新部署。而 `14` §4.5 选双路径的理由正是「回滚成本最低」 | ❌ |
| **保留但默认 true** | 有回滚路径；`parseBool` 语义从"默认关"变"默认开" | **没有"显式关闭"这个动作的强表达**——需要把 `parseBool` 换成"默认 true、只认 `0/false/off/no` 为关"的反向解析。这是**改动 `parseBool` 语义**，比看起来风险大（同一个 `parseBool` 还服务四个 fail-closed 开关，**绝不能碰**） | ✅ **推荐** |
| 保留现状（默认 false）+ 部署文件里设 `=1` | 零代码改动 | **默认值与事实相反**——代码说"默认走Plan A"，部署说"走 Plan C"。下一个读代码的人（或 agent）会得出错误结论。`env.ts:95-97` 的注释正是拿"默认值即安全网"当论据 | ⚠️ 次优，但**零风险**，可作为阶段一的过渡 |

**推荐落地（三段式，见 §3.2）**：
1. 阶段一（本批）：**只改部署**，设 `PROMPTFOO_ENGINE_NATIVE_GENERATION=1`，
   代码零改动 → 立刻验证 Plan C 在真实环境下能不能跑通。
2. 阶段二：确认可用后，**新增一个独立的反向开关** `PROMPTFOO_ENGINE_ALLOW_PLAN_A`
   （默认 unset = 禁用 Plan A），而**不是**改 `parseBool`。这样四个 fail-closed
   开关的解析逻辑一行都不动，回滚路径保留，且"Plan A 已死"变成一个**显式事实**
   而不是"要记得不设某个变量"。
3. 阶段三（可选）：删`generateTests.ts` 与Plan A 代码。

**理由**：真实模型下的 Plan C 能力矩阵**从未实测**（`15` §2「❌ 完全未做」，
`15` §5 亦列「多轮策略在真实目标上的表现」未验证）。在没有实测证据前删掉回滚路径，
是把一个未验证的路径变成唯一路径。**先证明它能跑，再拆掉退路。**

---

## 3. 诉求 1 · 实施方案（分步骤，每步可独立验证）

### 3.1 前置：必须先做的一件事

> **Gate前置：能力矩阵实测（`15` §2 / `14` §4.4 Step 0）尚未完成。**
> 本计划**不替代**它，也**不能跳过**它。原因：`15` §2 明确登记「29 个插件逐个实测」
> ❌ 完全未做；而移除 Plan A 会让**唯一生成路径**变成这条从未在真实模型上跑过的路径。

若用户决定不等这一步就推进，则**接受风险**：Plan C 在真实租户 LLM 上的生成质量、
29 个插件的实际可跑通率、多轮策略的真实表现，三项均为未知。

### 3.2 阶段一 · 打开原生生成（零代码改动，1 个配置项）

| 项 | 内容 |
|---|---|
| **改动** | `docker-compose.yml`（或 `.env`）加 `PROMPTFOO_ENGINE_NATIVE_GENERATION=1` 给 promptfoo-engine 服务 |
| **涉及文件** | `evaluating_platform/docker-compose.yml`（或 `scripts/` 下的部署模板，以实际为准） |
| **独立验证** |① `GET /health` 正常；② 发起一次含 ≥2 个插件的 run，观察 `status_text` 是否出现「promptfoo 原生生成攻击用例」（`nativeRedteam.ts:835`）；③ 若仍是「生成攻击用例」（`redteamRun.ts:427`，Plan A 专有文案），说明开关没生效 → 停 |
| **风险** | 真实模型下 29 个插件有**哪些跑不通**未知；`strict: true` 会把 0 用例变成 `PartialGenerationError` → run **失败**（这是**正确行为**，不是 bug） |
| **回滚** | 删掉这一行环境变量，重启引擎。**无需改代码、无需数据迁移** |

> ⚠️ 阶段一还有一个**必须一并检查**的连带项：`server.ts:60-62` 的 `onTerminal`
> 清理钩子**只在 `nativeGeneration` 为真时挂载**。打开开关后它会自动挂上，
> 这是**期望行为**（Plan C 会让 promptfoo 落盘 SQLite，见 `nativeRedteam.ts:56-86`
> 的裁定）。但要**实测**清理真的生效了——否则原始提示词会留在 promptfoo 的库里。

#### 3.2.1 阶段一实测清单（**打开后必须逐条确认**）

阶段一是零代码改动，但**行为变化只有跑起来才看得见**。下表逐项给出
「看什么」「预期什么」「不符怎么办」。

| # | 实测项 | 怎么看 | 预期 | 不符时 |
|---|---|---|---|---|
| **1** | **开关是否真生效** | 发起 run，观察 `status_text` | 出现「promptfoo 原生生成攻击用例」（`nativeRedteam.ts:835`） | 若仍是「生成攻击用例」（`redteamRun.ts:427`，Plan A 专有文案）→ 开关未生效，**立即停止并查部署** |
| **2** | **SQLite 清理钩子是否生效** 🔴 | run 结束后检查 promptfoo 的 SQLite 与 `/data/runs` | 产物被清理（`cleanupPromptfooRunArtifacts` 走通）；`/data/runs` 为空 | **若残留原始提示词 = 安全事故**（U7 的原始承诺是「阅后即焚」）。须查 `server.ts:60-62` 的条件挂载与 `promptfooStateCleanup.ts` |
| **3** | **清理失败的可观测性** | 故意让清理失败（如把 SQLite 目录设为只读） | run 被降级为 `failed` + `terminal_cleanup_failed`，**不静默留残留** | 若静默成功 = `nativeRedteam.ts:64-67` 的裁定失效，**必须修** |
| **4** | `token_usage` 显示成什么样 | 看报告 / `engine_runs.token_usage` | **仍是三档全 0**（§4.2 已证：reducer 写死） | 0 是**预期**，不是故障。但要**确认 UI 没有把它显示成「用量正常」**——0 在报告里无法区分「没测」与「测了是 0」（见 D3） |
| **5** | 配比变化 | 对比同一配置在 Plan A / Plan C 下的每插件条数 | **不同**，且 Plan C 的条数由 promptfoo 决定 | 属**预期**（§4.1）。但**必须提前告知用户**，否则「数字变了」会被当故障报 |
| **6** | 攻击提示词文风 | 读Dify 侧的被测回答 | 从「红队专家自由创作」变成「插件模板生成」 | 属预期。这是**用户唯一能感知到的实质变化** |
| **7** | 12 个平台维度的丢弃表现 | 选一个 `plan_a_only` 维度（如 `role-play`）跑一次 | 攻击手法**未按该维度施加**；emit 报「原生生成（N 个平台维度不参与）」 | **这是 D4 要修的缺口**。阶段一先**实测确认现象存在**，D4 落地后此项应变为「前端选不到」 |
| **8** | **`direct` 默认值的连带影响** 🔴 | 不选任何策略，直接提交 | `EngineEvalPage.tsx:283` 的 `values.strategies ?? ['direct']` 会下发 `direct`，而 `direct` 是 `plan_a_only` → **每次默认提交都在白跑一个被丢弃的维度** | **已实测确认的隐患**，见 §5.2-D4-附。**建议阶段一就先观察影响面**，D4 落地时一并修 |
| **9** | `policy` 插件的判定基础 | 选 `policy` 跑一次，看 `judge_sources` | 记为 `promptfoo:policy`（§4.5），非 `category:*` | 属预期（`nativeRedteam.ts:305`）。**但 `policy` 还需 `config.policy` 策略文档**，平台不提供 → 选它可能 0 用例（`15` §4 已登记） |
| **10** | 0 用例插件的表现 | 挑一个疑似跑不通的插件 | run **失败** + `PartialGenerationError`（`strict: true`） | 这是**正确行为**（消灭「绿色 0 覆盖」），**不是 bug**。但要记录**哪些插件跑不通**，供 `15` §2 能力矩阵回填 |
| **11** | 多轮策略真跑通 | 选 `crescendo` / `goat` / `hydra` 各跑一次 | promptfoo 策略引擎**真的介入**（对比 Plan A 下它只是 prompt 里一个字符串） | 若攻击成功率与 Plan A 无差异，需查 `applyStatefulToMultiTurn`（`agentSession.ts`）是否被正确触发 |
| **12** | 无出网 | 抓包 / 网络断言 | 生成过程**未访问** `api.promptfoo.app` 或任何非平台端点 | 这是 `14` §4.6 第 3 条验收标准。**Plan C 才是真正会触发 promptfoo 远程通道的路径**，此项在 Plan A 下是自动成立的，**在 Plan C 下必须重新验证** |
| **13** | 凭据零泄漏 | 查日志 + `engine_runs` 表 + workdir | 生成凭据不落日志/不落 DB/不回浏览器；workdir 终态被 `rm -rf` | 凭据泄漏 = P0。`nativeRedteam.ts:832-833` 写的 config 含明文 key，**必须确认 workdir 真被清** |
| **14** | 报告数字的**不可比性** | 拿Plan A 的历史报告与本次 Plan C 报告对比 | **不可直接比较**（生成范式变了，§4.1+§4.5） | 建议在报告里加一个「生成路径」标记（`metadata.generation_path`），否则用户会把两次结果当同一口径 |

### 3.3 阶段二 · 把「Plan A 已死」变成显式事实

| 步骤 | 内容 | 涉及文件 | 验证 |
|---|---|---|---|
| 2.1 | 新增 `PROMPTFOO_ENGINE_ALLOW_PLAN_A`（**默认 unset = 禁用 Plan A**）；`env.ts` 加`planAEnabled()`，与 `nativeGenerationEnabled` 做 XOR 互斥断言（两个都设 → 拒启动，报清晰错误） | `src/env.ts` | 反证测试：只设 `ALLOW_PLAN_A=1` → 走 Plan A；只设 `NATIVE_GENERATION=1` → 走 Plan C；两个都设 → 抛错；都不设 → 走 Plan C |
| 2.2 | `server.ts` 把`nativeGeneration: env.nativeGeneration \|\| !env.planAEnabled` 改成显式计算，新增 `enginePath: 'native' \| 'plan_a'` 字段以便 `/health` 与日志报出当前路径 | `src/env.ts`, `src/server.ts` | `/health` 增开 `engine_path` 字段（**安全**：只是个字符串枚举） |
| 2.3 | 更新 `env.test.ts`：现有 `describe('nativeGenerationEnabled (U7 path switch, default OFF)')` **整块语义失效**，必须重写（不能只加不改——否则测试在断言一个不再成立的事实） | `tests/env.test.ts` | CI 全绿 |

> **注意**：`env.test.ts:112-130` 现有 13 个断言里，有 11 个在断言
> 「未设/拼错 → false」。若最终决定「Plan C 为默认」，这些断言必须**逐条重写**
> 而不是删除——`ALLOW_PLAN_A` 的拼写错误行为同样要测（拼错 → 禁用 Plan A，
> 方向与现在相反：拼错会**更安全**）。

### 3.4 阶段三 · 删除 Plan A 代码（建议**暂缓**）

| 步骤 | 内容 | 涉及文件 |
|---|---|---|
| 3.1 | 提取 `EngineRunError` 到独立模块 | 新建 `src/runner/errors.ts`；改 7 处 import |
| 3.2 | 删 `generateTests.ts`（189 行）；`generateTests.test.ts` 随之删除 | 2 个文件 |
| 3.3 | 删 `redteamRun.ts` 的守卫块（52 行）+ `executePlanARedteamRun`（136 行）+ 分支；`executeRedteamRun` 塌成 Plan C 直线调用 | `src/runner/redteamRun.ts` |
| 3.4 | 删 2 个 Plan A 错误码；`types.ts:117/133` 注释同步| `src/types.ts` |
| 3.5 | 删/改 3 个测试：`u8BothPaths.e2e.test.ts`（Plan A 半边）、`u10CustomPlugin.e2e.test.ts`（2 个 Plan A 拒绝用例）、`smoke.e2e.test.ts`（走 Plan A） | `tests/` |
| 3.6 | 文档同步：`custom-plugin-publication.md:118`、`promptfoo-generation-options.md:245`、`14-Phase2实施计划.md` §4.5/§4.6 | 3 个 md |

**阶段三的净收益**：净删约 **190（redteamRun.ts）+ 189（generateTests.ts）= 379 行**，
删2 个错误码、2 个 assert 函数、3 个测试。**这是全项目最干净的一批删除。**

**阶段三的代价**：`14` §4.6 验收标准第 6 条「**Plan A 可回退**：关开关 → 行为与 U7 前完全一致（**反证测试**）」
将**永久无法满足**。必须在 `14` §4.7 U7 DoD 里显式记为「已裁定删除，不再适用」，
否则 Gate G2 会永远挂着一个无法勾选的条目。

---

## 4. 打开 Plan C 后会变的行为（含两处**对上游笔记的修正**）

### 4.1 配比会变 —— ✅ 属实

Plan A 是 LLM 自己决定（`generateTests.ts:87`「尽量均匀覆盖」是**建议**不是约束），
Plan C 由 promptfoo 按插件生成。**同一份配置在两条路径下每插件条数必然不同。**

**这反而是修好的一件事**：Plan A 的配比不可预测、不可复现、不可解释；
Plan C 的配比由promptfoo 决定且记录在 `eval` 里。**但必须提前告知用户，
否则"数字变了"会被当成故障。**

### 4.2 `token_usage` 会不再是 0 —— ❌ **不属实（实测反证）**

> **本条修正上游笔记。** `token_usage` 全 0 的原因**不是**「Plan A 不经promptfoo 的用量统计」，
> 而是**两条路径共用的 reducer 里写死了 0**：
>
> `redteamRun.ts:754-758`：
> ```ts
> token_usage: { generation: 0, judging: 0, target: 0 },
> ```
>
> 这在 `reduceEvalToSafeResult`（`:521`）的 return 里，而该函数
> **Plan A（`:502`）与 Plan C（`nativeRedteam.ts:894`）都调用**。
> 打开 Plan C **不会**让它变成非 0——它仍然是写死的 0。
>
> 若确实要真实用量，需要**另开一批**改动：让 reducer 从
> promptfoo 的 Eval 结果里读`tokenUsage`，并把 `EvalLike`（`redteamRun.ts:39-57`）
> 扩出usage 字段。**这不在本计划范围内**，此处只作事实澄清。

### 4.3 「119 → 29」—— ❌ **框架不成立（实测反证，本节最重要）**

> **本条修正上游笔记的定性。** 事实基础是对的（数字已核实，见下），
> 但**"打开 Plan C 才会从 119 变成只能选 29"这个因果是错的**。

实测证据：

| 证据 | 位置 | 说明 |
|---|---|---|
| 权威数字 | `internal/maclaw/testdata/promptfoo_remote_dep.json` | `platform_catalog_totals: {plugins: 119, marked: 90, local_only: 29}`，promptfoo 0.123.0 |
| **门控是路径无关的** | `promptfoo_engine/src/routes/runs.ts:50-66` | U6 闸门在 `POST /runs` 里，**在 `executeRedteamRun` 分支之前**。选Plan A 还是 Plan C 都会过这道闸 |
| BFF 侧同样路径无关 | `internal/api/handler/engine_run.go:313` | `ValidateEngineRunRemoteGeneration` 在**凭据物化、落库、派发之前** |
| 审计实测用的 5 个插件 | `audit-2026-10-13-attack-generation-architecture.md` | `prompt-extraction` / `pii` / `pii:social` / `prompt-injection` / `cross-session-leak` —— **实测 5 个全部 `marked=False`，即全在 29 个本地集内** |

**推论**：用户**今天就已经**只能选那 29 个。90 个远程依赖插件在 Plan A 下
**同样被409 拒绝**（`plugin_requires_remote_generation`），并**同样在 UI 上置灰**
（`EngineEvalPage.tsx:219`、`frontend/src/services/engineEval.ts` 的 `requires_remote_generation: true`）。

**所以：这不是"功能退化"，也不是"暴露真相"，而是「什么都没有变」。**

上游笔记说「从『能跑 119 个（其中 90 个实际静默 0 覆盖）』变成『只能选 29 个』」——
**这个描述在两个方向上都不准确**：

1. **Plan A 下也不是「能跑 119 个」**——U6 的 409 闸门早就挡住了 90 个。
   若真的能跑 119 个且静默 0 覆盖，那恰恰是 U6 要消除的失败形状，
   而 U6 已在 2026-10-11 落地。
2. **也不是 Plan C 导致的收缩**——两条路径共用同一个闸门。

**真正会变的只有一件事**：那 29 个本地插件，**每一条的攻击提示词由谁写**。
从「平台 LLM 自由创作」→「promptfoo 的插件模板生成」。这是 §4.1 说的配比变化，
它才是用户唯一能感知到的差异。

> 顺带修正：`remoteDepGuard.ts:1-32` 的文件头注释描述的是
> 「若没有 U6，Plan C 下的 `createRemotePlugin` 会`return []`」。
> 那个描述对Plan C 成立，但对**当前 Plan A 部署**是无害的——
> Plan A 从不调用 `createRemotePlugin`。这个差异此前没人写下来。

### 4.4 多轮策略在 Plan C 下才真正可用 —— ⚠️ 部分属实，且有**新增损失**

**属实的部分**：`crescendo` / `goat` / `hydra` 在 `nativeStrategyMap.ts:114-116`
映射为 promptfoo 的 `crescendo` / `goat` / `jailbreak:hydra`。Plan A 下这三个 id
只是被塞进 prompt 里当字符串标签，**promptfoo 的策略引擎从未介入**。
Plan C 下它们才真正执行。

**但实测发现一个上游笔记没提的重要事实**：被 U6 门控的 4 个策略是
`bestOfN` / `likert` / `citation` / `singleTurnComposite`
（`remote_dep.json` 的 `remote_gated_strategies`）——**crescendo/goat/hydra 不在其中**，
所以它们打开后确实可跑。

**新增损失（上游笔记完全没提，必须让用户知道）**：
`nativeStrategyMap.ts:59-95` 有 **12 个 `PLAN_A_ONLY` 平台自造维度**
（`direct` / `role-play` / `encoding` / `multi-turn` / `assumed-knowledge` /
`few-shot` / `many-shot` / `translate` / `otherEncodings` / `simba` / `retry` /
`mathPrompt`；**其中 9 个前端可见，另 3 个已被 Phase 3 移出目录**）。Plan C 下它们被
`resolveNativeStrategies`（`nativeRedteam.ts:216-267`）**直接丢弃**，
只在 `emit` 里报一句「原生生成（N 个平台维度不参与）」（`:787-791`）。

**这意味着：打开 Plan C 后，选`direct` / `role-play` / `few-shot` / `translate`
这些维度的用户，攻击手法会从「LLM 按这个维度创作」变成「这个维度被静默忽略」。**

**这是本次收敛里唯一真正的功能退化**，且它比 §4.3 严重得多。处置见 §6-D4。

### 4.5 判定路径 `judge_source` 的注入方式 —— ⚠️ **两条路径不一致（实测确认）**

| 维度 | Plan A | Plan C |
|---|---|---|
| 注入载体 | 每条 test 的 `metadata[PLUGIN_JUDGE_SOURCE_METADATA_KEY]`（`redteamRun.ts:451`） | 插件 config 的 `graderGuidance`（`nativeRedteam.ts:389-406`） |
| 生效位置 | 平台自建的 `llm-rubric` assertion（`redteamRun.ts:447`） | **promptfoo 自己的 grader**，平台标准以 guidance 形式**追加在其后**，冲突时guidance 优先 |
| `policy` 插件 | 正常注入平台 rubric | **跳过注入**（`PROMPTFOO_CONFIG_UNSAFE_PLUGIN_IDS`，`nativeRedteam.ts:305`），报告记为 `promptfoo:policy` |
| 报告里的 `judge_source` | per-test stamp，最准确 | 走 `judgeSourceOverrides` / `judgeSourceByPlugin`（`nativeRedteam.ts:894-897`） |

**结论**：`judge_source` 的**报告字段仍然有值**（U8 的四档设计在Plan C 上已就位），
但**语义不完全相同**：Plan C 的 `plugin:<id>` 意为「promptfoo 的 grader + 平台标准作为
guidance 覆盖」，Plan A 则是「纯平台标准」。`14` §2.10.3 要求「报告里的判定基础必须与
实际一致」——**打开 Plan C 后，`plugin:<id>` 这一档的含义变窄了，文档必须同步**。

### 4.6 汇总：用户可感知的行为变化清单

| # | 变化 | 性质 | 用户能看到吗 |
|---|---|---|---|
| 1 | 每插件攻击提示词条数变化 | **预期**（配比决定权易主） | 能，且很可能被误报为故障 → **必须提前公告** |
| 2 | 攻击提示词的文风变化 | 预期（从「红队专家自由创作」→「插件模板生成」） | 能 |
| 3 | `token_usage` | **不变**（仍写死 0，§4.2） | 看不到 |
| 4 | 可选插件集| **不变**（29→29，§4.3） | 看不到 |
| 5 | 多轮策略真正执行 | **改善**（crescendo/goat/hydra 真的跑） | 需对比攻击成功率 |
| 6 | 12 个平台维度被静默忽略（前端可见 9 个） | **退化**（§4.4，唯一真退化） | **看不到**——只有一句 emit 文案；**D4 落地后变为「选不到」** |
| 7 | `policy` 插件判定基础 | 降级为 `promptfoo:policy` | 能（`judge_sources`） |
| 8 | promptfoo SQLite 会落盘 | 预期（`nativeRedteam.ts:56-86` 裁定：接受 + 终态清理） | 不能，但**必须实测清理生效** |
| 9 | 0 用例插件 → run **失败** | **改善**（`strict: true` 消灭「绿色 0 覆盖」） | 能（`PartialGenerationError`） |

---

## 5. 诉求 2 · 报告原文的四条路

### 5.0 三个选项的大白话版（2026-10-13 用户反馈「不懂选项含义」后重写）

> **你想要的「在报告里看到攻击原文」，原文要存在哪儿？**
>
> | 选项 | 打个比方 | 报告文件本身里有没有原文 |
> |---|---|---|
> | **(d) 临时看一眼** | 像**监控实时画面**：点开时实时从引擎拿，看完不存 | **没有** |
> | **(b) 只存指针** | 像**快递取件码**：报告里存一个编号，你点了去 Dify 那边看原文。平台不存原文 | **没有** |
> | **(c) 平台存原文** | 像**把快递寄回家**：原文加密存在平台，报告里直接显示 | **有** |
>
> **三者的唯一实质差别就是：原文会不会跟着报告 PDF 一起流出去。**

### 5.0-A 🔴 D1 = B 已定：原文进报告 PDF / 报告卡片 —— 风险与实现边界

> **用户已裁定D1 = B**（2026-10-13）。本节评估该裁定的影响面，**不重开决策**，
> 但必须让用户在**实施前**看到三个具体问题。

#### 5.0-A.1 冲突范围收窄

D1 = B 只撞以下条款（**不撞 C3/C6**，因为进度元数据与 payload handle 两条不受影响）：

| # | 条款 | 位置 | 被撞部分 |
|---|---|---|---|
| **C1** | `maclaw_redteam_reports` **只持久化安全摘要与元数据** | `AGENTS.md:47` | 报告表要存逐条原文 |
| **C2** | 原文不得进**报告 DTO**、浏览器响应或日志 | `AGENTS.md:47` | 报告 DTO 要带原文 |
| **C4** | 判定原始值不得复制进报告 DTO / 报告表 | `AGENTS.md:46` 末 | 判定依据可能引原文片段 |
| **AGENTS:48** | 报告 PDF 的**固定中文模板**含「findings、successful attack examples」等章节；明令「**Do not reintroduce** … evidence indexes, attack-path summary … appendices」 | `AGENTS.md:48` | 「攻击样例原文」若作为**新章节**加入，撞「Do not reintroduce」；若填进**已有的 successful attack examples 章节**，则不撞——**这个区分决定了工作量** |

**存储层现状**（实测）：`migrations/022_maclaw_redteam_artifacts.sql` 的
`maclaw_redteam_reports` 有 `findings JSONB`——**技术上装得下**，无需新迁移；
但 `sanitizeRedteamArtifactText`（`redteam_artifact_service.go:264-290`）
会在写入前把含 `raw prompt` / `raw response` / `payload body` 等标记的文本
整体替换为 `[redacted sensitive text]`——**该函数必须开口子，否则写了也被洗掉**。

#### 5.0-A.2 核心风险：加密存储挡不住 PDF 流出

这是本轮**最重要的一句话**，请务必让用户看到：

> **(c) 路线的加密，只解决「数据库被拖库」，不解决「报告 PDF 流出」。**
> 内容一旦渲染进 PDF，**它就是明文**，加密库对它毫无办法。

完整风险链：

```
攻击原文 → 平台库（可加密）→ 报告 findings → 报告 DTO（明文）→ PDF（明文）
                ↑                                    ↑
         加密在这里有效              加密在这里已经失效
                                        ↓
                        PDF 发出客户邮箱 / 存进工单系统 / 进合规归档
                                        ↓
                    平台完全失去控制：无法撤回、无法删除、无法追踪流向
```

**所以在 D1 = B 下，(c) 的加密产生的保护接近于零**，而它带来的成本
（密钥管理、保留期清理任务、权限矩阵、审计）却是 100% 的。

**这改变了 (c) 的性价比判断**：原本「加密 + 不导出」的组合能形成一个
「可控的封闭 enclave」，但 D1 = B 主动放弃了「不导出」这一半，
**只剩下成本，没有对应的保护。**

#### 5.0-A.3 三个必答问题（对 (c) 路线）

| 问题 | 选项 | 代价 |
|---|---|---|
| **Q1 谁能看** | (i) 所有能读报告的人 | 最省事，但报告面本来就更宽（客户/管理层）→ **攻击手册发给最不该拿的人** |
| | (ii) 仅管理员门户 | 需权限分级；但**报告本身仍是共享的**，读者看到「有原文但看不到」会困惑 |
| | (iii) 需显式授权的角色 | 最安全；**成本最高**（角色模型 + 审计日志 + 授权撤销） |
| **Q2 保留多久** | (i) 永久 | 平台变成**长期攻击语料库**，与 §5.2 判据直接冲突 |
| | (ii) 按 run 设（如 30 天） | 需清理任务；需处理「清理失败」→ 参照 `terminal_cleanup_failed` 的处置先例 |
| | (iii) 立即删（只留加密备份） | 「阅后即焚」；但 PDF 已经出去了，**这只约束未来的 PDF** |
| **Q3 能否导出** | (i) PDF 含原文 | D1=B 的直接后果，**无法两全** |
| | (ii) PDF 不含，网页单独看 | **保住 PDF 这一半，放弃「报告里直接显示」** ← 实质上退回 (d) |
| **Q4 会让平台变成什么** | — | 若 Q1=(i) 且 Q2=(i)：**平台事实上成了攻击语料仓库**，且已通过 PDF 分发到平台外——**这是最坏组合** |

#### 5.0-A.4 D1 = B 的实现边界选项（**请用户从这三个里选一个**）

我不替用户决定，但**必须把「D1=B 不等于必须接受最坏形态」这件事说清楚**——
B 选定的是「**在报告这个载体里看**」，它有三种不同的实现强度：

| 选项 | 形态 | 撞约束 | PDF 是否含原文 | 代价 | 我的倾向 |
|---|---|---|---|---|---|
| **B-1轻量** | 报告**只显示前N 条成功探针的原文摘要**（如每条截断 200 字），**完整原文需单独申请** | 撞 C1/C2（摘要也是原文） | **否**（只显示摘要，完整版走单独入口） | 用户可能嫌「看不全」；需新增一个申请入口 | ✅ **倾向** |
| **B-2 受控** | 原文**仅在管理员门户网页可见**，**不进 PDF、不进报告 DTO**；报告只给一个跳转链接 | 撞 C2/C5，**不撞 C1** | **否** | 需权限分级 + 单独查询接口 | ✅ 倾向（与 B-1 可叠加） |
| **B-3 全量** | 接受原文明文进 PDF、进报告表 | 撞 C1/C2/C4（视PDF 章节写法） | **是** | **平台成为攻击语料分发渠道**；须接受 Q1=(i)、Q2=(i) 的后果 | ⚠️ 若坚持，这是唯一诚实的实现，但**须先回答 Q1~Q4** |

**关键提示**：**B-1 + B-2 叠加** 能满足「在报告里看到攻击原文」这个诉求的**大部分**
（用户能在报告里看到前若干条、知道后面还有、能跳到完整视图），
同时**不产生「PDF 明文外流」这个不可逆的后果**。
而 B-3 一旦上线，**任何一份已发出的 PDF 都撤不回**。

#### 5.0-A.5 我对 D1=B 的判断

- **D1 = B 是合理的产品诉求**——用户在报告里看不到任何实际攻击内容，
  报告的说服力确实不足。这个需求本身是对的。
- **但 D1 = B 推出「必须接受 Q1=(i) + Q2=(i)」是不成立的**。
  §5.0-A.4 的 B-1 / B-2 说明：**「在报告里看」与「原文明文进 PDF」是两件事**，
  中间有实现空间。
- **我倾向 B-1 + B-2**：报告里有原文摘要（够用），完整原文在受控入口（可控），
  **PDF 保持干净**（不可逆的那一步不做）。
- **若用户坚持 B-3**，我建议至少先回答 Q1/Q2，并**在代码里留下显式的契约变更标记**
  （`AGENTS.md` L47 的例外条款 + `022`迁移注释），让后来者知道这不是疏忽。

### 5.0 「对话框」是哪一个

用户原话「按照计划，让报告能在**对话框**中显现，评估能否让原文出现在**报告**之中」
——**这句话里有两个不同的目标物，安全含义完全不同**：

| | **理解 A：聊天对话框** | **理解 B：报告 PDF / 报告卡片** |
|---|---|---|
| 落点 | `frontend/src/pages/enterprise/ChatPage.tsx` + `chatDisplay.ts` | `redteam_artifact_service.go` 编译的报告 + `/export` 产出的 PDF |
| 数据流 | 进**浏览器响应**（SSE / HTTP body） | 进**报告存储**（`maclaw_redteam_reports.findings` JSONB）+ PDF 文件 |
| 撞哪条约束 | **C2 + C5 + C6**（不得进浏览器响应/ DTO；payload 只作服务端 handle） | **C1 + C2 + C4**（不得进报告表/DTO） |
| 是否落库 | **否**（若只做一次性展示） | **是**（必然） |
| 是否成为「敏感数据仓库」 | **不会**（不落库 = 无留存 = 无仓库） | **会**（§5.3 风险） |
| PDF 导出能否带出 | 不适用 | **AGENTS.md:48 明确规定报告 PDF 的固定章节**，加「攻击样例」= 改模板契约 |
| 实施难度 | 中（引擎出→ BFF 转发 → 前端渲染） | 中高（多一层存储 + 保留期 + 权限 + 导出拦截） |
| **我的倾向** | ✅ **可做，风险可控** | ⚠️ **需先决策，不建议本批做** |

**判断**：用户说的「对话框」从字面看**更可能指理解 A（`ChatPage`）**——
「在对话框中显现」是屏幕语义，「报告之中」才是存储语义。
**这一点必须请用户确认，因为它决定了要不要动C1。**

### 5.1 路线对比

> **D1 = B 已定**（2026-10-13）：用户要的是**报告载体**里能看到原文，
> 不是聊天对话框。因此 **(a) 与 (d) 作为主路线出局**——(d) 是「报告里没有原文」，
> 与 D1=B 直接矛盾。下表保留它们仅作对照，实际可选的是 **(b) 与 (c) 两条**，
> 且 (c) 的实现强度由 §5.0-A.4 的 B-1 / B-2 / B-3 决定。

| | (a) 放开报告存原文 | (b) 原文留被测系统侧，平台存指针 | (c) 加密的运营者证据模式 | (d) 只读瞬时回显 |
|---|---|---|---|---|
| **谁能看到** | 所有报告读者（含导出 PDF 的接收方） | 任何能访问 Dify 的人 | 仅管理员门户 | 提出请求的**同一用户**，仅本次 |
| **保留多久** | 永久（随报告） | 在被测系统自己的保留期内 | 按 run 设保留期，需清理任务 | **不落库，进程结束即消失** |
| **能否导出** | **能**（PDF/JSON 导出都会带出去） | 不适用 | **取决于 §5.0-A.4 选哪个**：B-3 能、B-1/B-2 不能 | 否 |
| **是否敏感数据仓库** | **是** | 否 | **是**（受控的） | **否** |
| **撞的约束** | C1 + C2 + C4 + AGENTS:48 | 无 | C1 + C2 + C4（需改契约） | C2 + C5（需改契约，**不改 C1**） |
| **契约改动量** | 最大 | **零** | 大 | 小 |
| **与 D1=B 的相容性** | ✅ 相容（就是 B-3） | ⚠️ **不相容**（报告里没有原文，是反向） | ✅ 相容（强度可选） | ❌ **不相容**（报告里没有原文） |
| **关键风险** | 报告是**交付物**，会外发 → **攻击手册流出平台** | 用户要自己写 SQL/翻控制台；但这是**当前现状** | **加密只护库、不护 PDF**（§5.0-A.2）——若选 B-3，加密近乎无效 | 与 D1=B 矛盾 |
| **本轮状态** | 实际等同 **B-3** | 落选（但**仍应写进产品文档**作为「去哪看原文」） | **待用户在 B-1/B-2/B-3 间选择** | 落选 |
| **我的结论** | ❌ **反对** | ✅ **默认推荐** | ⚠️ 若用户确需留存 | ⚠️ **若用户要的是"看一眼"** |

### 5.2 关键判断：**「敏感数据仓库」风险的准确边界**

`audit-2026-10-13-report-detail-boundary.md` §5 把这称为「治理决策」。
我同意，并补充一条**更具体的判据**，让用户能自己判断：

> **攻击原文入报告库之后，它会随报告一起离开平台。**
> 现在报告的读者是「自己公司的安全工程师」；一旦报告 PDF 被发给客户、
> 存进工单系统、或被合规流程归档，**攻击原文就成了攻击手册**——
> 而平台无法控制报告流向。这是 (a) 与 (c) 的共同风险，不是 (a) 独有。

所以我的推荐顺序是：

1. **先做 (d) 的最小形态**（只读、瞬时、不落库、不导出）——
   它满足「让原文能被看到」这个诉求，同时**不产生仓库、不改C1**。
2. **同时把 (b) 写进产品文档**（当前零成本：告诉用户「去 Dify 看原文」）。
3. **(c) 只有在用户明确说「我需要留存原文」时才做**，且必须先回答：
   谁解密、保留多久、谁能导出、密钥怎么管。**这四点答不出来就不要开工。**

### 5.3 (d) 方案的最小设计（供用户评估，非承诺）

| 决策点 | 建议取值 | 理由 |
|---|---|---|
| 触发方式 | 报告卡片上按需请求，**不给就永远不生成** | 避免每次评测都产生原文 |
| 取数范围 | **仅本次 run 的成功探针**（不是全部原文） | 报告关心的是「哪条攻击成功了」，不是攻击语料库 |
| 形态| 只读、可关闭、默认折叠 | 降低误截图/误分享概率 |
| 落库 | **绝不** | 保住C1 |
| 导出 | **不进 PDF/JSON 导出** | 保住 AGENTS:48 的固定章节 |
| 日志/错误追踪 | 必须在实现时**逐条检查** SSE 缓冲、错误信息、`console.log` | 这是最容易漏的一处——原文容易从日志侧漏出去 |
| 护栏 | 反证测试：断开本功能后，安全断言**仍全绿** | 沿用 `15` §8 的「每个新增护栏都做过反证」纪律 |
| **仍需用户确认的** | 它仍撞 C2/C5（浏览器响应）。**契约必须改，且改动落在 AGENTS.md** | 这一条不能绕过 |

---

## 6. 决策点（状态已更新：D1/D2/D4/D6 已定，D3 暂缓，D5 待答）

### D1 ·「对话框」到底指哪一个？→ ✅ **已定 = B**

| 选项 | 代价 |
|---|---|
| ~~A. `ChatPage` 聊天对话框（瞬时展示）~~ | 撞 C2+C5，要改契约；不落库；风险可控 —— **用户未选** |
| ✅ **B. 报告 PDF / 报告卡片** | 撞 C1+C2+C4+AGENTS:48，要改契约；**落库 = 仓库风险** —— **用户已选** |
| ~~C. 两个都要~~ | 契约改动最大 —— **用户未选** |

**原倾向（已作废）**：先 A 后 B。**用户选了 B**。
→ **落地前的最后一道闸**：B 这个选择内部还有三种实现强度，
见 **§5.0-A.4 的 B-1 / B-2 / B-3**。**倾向 B-1 + B-2**（报告内可见、
PDF 保持干净）。**这是当前唯一待用户回答的问题。**

### D2 · 开关删还是留？→ ✅ **已定 = 三段式**

| 选项 | 代价 |
|---|---|
| 删 | **无回滚路径**，而 Plan C 真实能力矩阵从未实测（`15` §2） |
| 留但默认 true | 需新增反向开关（不能改 `parseBool`——它服务四个 fail-closed 开关） |
| 保留现状 + 部署设 `=1` | 零风险，但「默认值」与「事实」相反 |

**✅ 用户已定：三段式** ——阶段一（§3.2，零代码改动，仅设环境变量）→
阶段二（§3.3，新增反向开关）→ 阶段三（§3.4，能力矩阵通过后再删代码）。
**原倾向与用户决策一致。** 阶段一实测清单见 §3.2.1（14 项）。

### D3 · `token_usage` 要不要真的修？→ ⏸️ **暂缓（未决）**

| 选项 | 代价 |
|---|---|
| 不修（保持写死 0） | 报告里永远看不到用量；但**零风险** |
| 修（让 reducer 读promptfoo 的真实用量） | 需扩 `EvalLike`（`types.ts:39-57`）、新增字段、**跨过`redact.ts:62` 的 `token_usage` 白名单边界**需确认；属**独立一批** |

**倾向：本批不修，另开一批。** 现状 0 是诚实的（未被测量的值显示为 0
不等于「测得为 0」——但也**容易被误读**，见下）。
**⚠️ 无论是否修，阶段一实测清单第 4 项都要确认 UI 没有把三档全 0 显示成「用量正常」。**

> ⚠️ 顺带指出一个既存的表达缺陷：`token_usage` 三档全 0 在报告里
> **无法区分「没测」与「测了是 0」**。这与 U8 对 `judge_source` 的处理
> （缺失 vs `fallback:unmapped` 明确区分）是同一类问题，但**当前未被识别为缺陷**。

### D4 · 12 个 `PLAN_A_ONLY` 维度怎么办？→ ✅ **已定案，见 §6-A**

打开 Plan C 后 `direct` / `role-play` / `encoding` / `few-shot` / `translate` 等
**12 个**平台自造维度**被静默丢弃**（§4.4）。

| 选项 | 代价 |
|---|---|
| **A. 照现状静默丢弃** | 用户选了「角色扮演」却什么都没发生，**且他看不到**——只有一句 emit 文案 |
| **B. 显式拒绝**（类比 U10 的 `assertNoCustomPluginsOnPlanA`） | 一致性最好；但会让一批历史配置**直接跑不起来** |
| **C. 前端隐藏这些维度** | 用户看不到就不会选；但**存量配置/专家包引用了它们**时仍会撞 |
| **D. 映射到 promptfoo 等价策略** | 最正确但工作量最大，且部分无等价（`few-shot`/`many-shot` promptfoo 无对应） |

**倾向：B + C 组合**——前端隐藏（`engineEval.ts` 选项表）+ 后端保留
`assertNoCustomStrategiesOnPlanA` 式的显式拒绝（**把「静默丢弃」换成
「明确报错 + 说明用哪个 promptfoo 策略替代」**）。
理由：U6 存在的全部意义就是消灭「选中能力、零覆盖、无法察觉」，
而 §4.4 这个退化**正是 U6 要消灭的那种形状**，只是方向相反。
**不能一边引入U6，一边引入一个新的静默丢弃。**

---

## 6-A.✅ D4 已定案（用户方案）：前端不显示 + 规划时选不到

> **用户裁定（2026-10-13）**：「前端不显示，maclaw 生成计划时不能选就好」。
> **该方案优于本plan 的原建议（B+C 组合）**，理由见 §6-A.3。

### 6-A.1 可行性核实：代码已就位（本轮实测）

主理人指出「代码已就位」，**本轮逐条核实，确认成立**：

| 声称 | 实测 | 结论 |
|---|---|---|
| `nativeStrategyMap.ts:198-200` 已有 `planAOnlyStrategyIds()` | 存在，导出 `PLAN_A_ONLY` 的 12 个 key | ✅ 成立 |
| 映射表第 180-185 行已有 `native` / `plan_a_only` 两个 disposition | `MAPPING` 把 `NATIVE` 映为 `native`、`PLAN_A_ONLY` 映为 `plan_a_only` | ✅ 成立 |
| 前端 `engineEval.ts:378` 有 `isStrategyRemoteGenerationGated` | 存在，但**只看 `requires_remote_generation`**，不看 disposition | ✅ 存在，但**需扩展判断条件** |
| `EngineEvalPage.tsx:264` 已在用它算 `gatedStrategies` | `:223` `gatedStrategyCount` 已在用 | ✅ 成立 |

**实测统计（本轮新算，用于验证改造后的可见性）**：

| 集合 | 数量 | 说明 |
|---|---|---|
| 前端 `ENGINE_STRATEGY_OPTIONS` 总项| **37** | `engineEval.ts:247` 起 |
| 已被远程依赖置灰 | **4** | `bestOfN` / `citation` / `likert` / `singleTurnComposite` |
| 去掉远程置灰后仍可选 | **33** | — |
| ├ 其中属 `NATIVE`（Plan C **真正可用**） | **24** | `base64`…`custom` / `jailbreak` / `prompt-injection` |
| └ 其中属 `PLAN_A_ONLY`（Plan C **会丢弃**） | **9** | `direct` / `role-play` / `encoding` / `multi-turn` / `assumed-knowledge` / `few-shot` / `many-shot` / `translate` / `otherEncodings` |
| 既不在两表的未知项 | **0** | ✅ 两表完全覆盖前端选项，**没有漏网** |

> **注**：`PLAN_A_ONLY` 共 12 个，但前端只暴露 9 个——
> 另3 个（`simba` / `retry` / `mathPrompt`）在 Phase 3 §6.1.2 已被**移出目录**，
> 用户本来就选不到。**这与 `15` §6.1.2 的记录一致。**
>
> ⚠️ **本轮修正**：本文档§4.4 原写「13 个 `PLAN_A_ONLY`」，**准确值是 12 个**
> （此前用正则统计漏了带引号的 key）。**前端可见的是 9 个。**

### 6-A.2 🔴 必须先解决的地雷：`direct` 是表单默认值

**这是 D4 方案实施前必须处理的第一件事，否则改了会坏。**

| 事实 | 位置 |
|---|---|
| 提交时 `strategies: (values.strategies ?? ['direct'])` | `EngineEvalPage.tsx:283` |
| `direct` 在前端选项表里（`{ id: 'direct', name: '直接攻击' }`） | `engineEval.ts:248` |
| `direct` 属 `PLAN_A_ONLY` | `nativeStrategyMap.ts:60` |

**后果**：用户**不选任何策略**直接提交 → 下发 `['direct']` → Plan C 下
`direct` 被 `resolveNativeStrategies` 归入 `planAOnly` → **被静默丢弃**。
而若按D4 方案把 `direct` 置灰/隐藏，**这个默认值会变成「指向一个不可选项的默认提交」**，
用户不选策略时表单会**静默提交失败**或**提交一个被拒的请求**。

**处置（必须做，二选一）**：

| 方案 | 做法 | 代价 |
|---|---|---|
| **推荐：改默认值** | 把 `values.strategies ?? ['direct']` 改为 `?? []`（**不选 = 不施加平台维度**，只跑插件默认攻击），并同步改表单初始值 | 需改 2 处；语义更正确（「没选策略」本就不该等于「直接攻击」） |
| 改默认值到一个 `NATIVE` 项 | 改成 `['jailbreak']` 之类 | ❌ **不好**：会把「用户没选」悄悄变成「施加了越狱模板」，同样是静默施加用户没要的手法 |

**倾向：改成 `[]`。** 理由：`[]` 的语义是「不加额外维度」，
而 `['direct']`的语义是「施加直接攻击」——**这是两个不同的意思，
现在的默认值在语义上是错的**，不只是无用。

### 6-A.3 为什么用户方案优于本plan 的原建议

| 维度 | 本 plan 原建议（B 后端拒绝 + C 前端隐藏） | 用户方案（前端不显示 + 规划时选不到） |
|---|---|---|
| 前端 | 置灰但**可见** | **不显示** |
| 存量配置 / 专家包引用这些 id | 后端报错 → **跑不起来** | 未覆盖（见下方风险） |
| 用户心智 | 「有这个能力但此处不可用」 | 「平台没这个能力」 |
| 引入的新概念 | 沿用 U6 的置灰机制，**零新概念** | 零新概念 |
| 与项目惯例一致性 | U6 用的是「置灰但保留可见」（`EngineEvalPage.tsx:22` 注释明确说明「不隐藏任何条目」） | ⚠️ **与U6 的惯例不同**——但方向更保守（隐藏 > 置灰） |

**用户方案更好的三个理由**：

1. **不引入新功能，只把「不支持」变成「不可选」**。
   置灰需要「禁用原因」这套 UI 已经在用（`EngineSelectOption.reason`），
   但**隐藏更简单**：不显示就不需要解释为什么不能选，也不需要在灰态上悬停提示。
2. **MaClaw 规划时也选不到**——用户明确要求这一点。能力目录卡片
   （`promptfoo_plugin_catalog.go:501`的 `card.SafeMetadata["unavailable_reason"]`）
   走的是**同一份 `Unavailable`** 判定，所以前端不显示 = 卡片不显示 = 规划时选不到，
   **三处天然一致**（都读同一个source of truth）。
3. **用户不会产生「我选了角色扮演但没生效」的错觉**——
   这正是 §4.4 那个静默丢弃的原始问题。

**用户方案的一个风险（须在实施时补上）**：
「前端不显示」**只覆盖新提交**。**存量 run 记录、专家包、自定义策略包**
若引用了这 9 个 id，行为不变（仍被静默丢弃）。
**处置**：后端仍**必须**保留一条显式拒绝（把静默丢弃换成明确报错），
这与前端隐藏**不冲突**——前端不显示是「不给选」，后端拒绝是「选了也会说清」。

> **最终形态 = 用户方案（前端不显示 + 规划时选不到）**
> **+ 后端显式拒绝作为存量兜底（防静默丢弃）**
> **+ §6-A.2 的 `direct` 默认值必须先修**

### 6-A.4 改动清单

| # | 改动 | 文件 | 具体位置 | 依赖 |
|---|---|---|---|---|
| **1** | **导出 `PLAN_A_ONLY` 的 id→reason 映射** | `promptfoo_engine/src/runner/nativeStrategyMap.ts` | 已有 `planAOnlyStrategyIds()`（`:198`）；**新增** `planAOnlyStrategyReasons(): Readonly<Record<string,string>>`（`:59-95` 的表已含 reason） | — |
| **2** | **BFF 侧生成 disposition 清单** | 新建或复用 `internal/maclaw/promptfoo_strategy_catalog.go` | 复用 `PlatformToNativeStrategyID()`（`:250`）：**返回 `""` 的即为平台自造维度**。新增 `PlatformInventedStrategyReasons() map[string]string` | 需把 `nativeStrategyMap.ts:59-95` 的 reason 同步进 `testdata/promptfoo_strategy_catalog.json`（生成脚本 `scripts/gen_strategy_catalog.mjs`） |
| **3** | **EngineCatalog 携带 disposition** | `internal/maclaw/promptfoo_engine_client.go:209-217` + `promptfoo_plugin_catalog.go:736-745` | `EngineCatalogStrategy` 增 `Disposition string \`json:"disposition,omitempty"\`` + `UnavailableReason`；构造处填`native` / `plan_a_only` | 2 |
| **4** | **前端不显示** | `frontend/src/services/engineEval.ts:247-300` | 从 `ENGINE_STRATEGY_OPTIONS` **移除**那 9 项（`direct`/`role-play`/`encoding`/`multi-turn`/`assumed-knowledge`/`few-shot`/`many-shot`/`translate`/`otherEncodings`）。**不做置灰**（用户方案） | 3 |
| **5** | 🔴 **修 `direct` 默认值** | `frontend/src/pages/enterprise/EngineEvalPage.tsx:283` | `values.strategies ?? ['direct']` → `?? []`；同步改表单 `initialValues` | 4 |
| **6** | **后端显式拒绝（存量兜底）** | `promptfoo_engine/src/runner/nativeRedteam.ts:216-267` + 新错误码 | `resolveNativeStrategies` 把 `planAOnly` 从「收集后忽略」改为**抛 `EngineRunError`**（新码如 `strategy_not_available_on_native_generation`），错误信息带上 `nativeStrategyMapping(id).reason` | 1 |
| **7** | **BFF侧同步拒绝** | `internal/api/handler/engine_run.go`（`ValidateEngineRunRemoteGeneration` 附近） | 新增 `ValidateEngineRunStrategyDisposition(plugins, strategies)`，在凭据物化前拒绝 | 3 |
| **8** | **前后端一致性护栏** | `internal/maclaw/gen_frontend_options_test.go` | 断言：前端 `ENGINE_STRATEGY_OPTIONS` **不含**任何 `platform_invented_strategy_ids`；且与后端 disposition 逐条一致 | 4,3 |
| **9** | **反证测试** | `promptfoo_engine/tests/nativeRedteam.test.ts` | **摘掉第 6 项的抛错** → 测试必须变红（沿用 `15` §8「每个新增护栏都做过反证」） | 6 |
| **10** | **MaClaw 能力卡片同步** | `internal/maclaw/promptfoo_plugin_catalog.go:501` | 复用现有 `unavailable_reason` 通道；`plan_a_only` 的卡片**不生成**（而非生成后标灰） | 2 |

**依赖顺序**：1 → 2 → 3 → {4,6,7,8,10} → 5 → 9
（**第 5 项必须在第 4 项之后**，否则会短暂指向已删除的选项）

**关键风险**：

| 风险 | 处置 |
|---|---|
| **存量 run / 专家包引用这 9 个 id** | 第 6、7 项兜底：明确报错 + 给出替代建议，**而不是静默丢弃** |
| **前端移除 9 项后，选项表与后端目录漂移** | 第 8 项护栏（`gen_frontend_options_test.go` 已有同类断言可扩展） |
| **`planAOnlyStrategyIds()` 变了但后端清单没变** | 两表一致性已有测试：`TestPromptfooStrategyNativeMappingMatchesEngineTable`（`promptfoo_strategy_catalog.go:246-248` 注释所述）——**扩充它**，让它同时断言 disposition 一致 |
| **改了 `direct` 默认值 = 改变了现有语义** | 需在 `14` 记一笔：「未选策略 ≠ 直接攻击」；旧报告不可比（与 §3.2.1 第 14 项同源问题） |

### D5 · 报告原文的路线选择

见 §5。**倾向：先 (d) 最小形态 + 同时落 (b) 的文档；(c) 暂不做，
除非能答出解密/保留期/导出/密钥四问。**
>⚠️ **2026-10-13 更新**：D1 = B 已定，(d) 作为主路线**出局**（报告里必须有原文）。
> 见 §5.0-A.4 的 B-1 / B-2 / B-3 三选一。

### D6 · 阶段三（删 Plan A 代码）现在做还是等能力矩阵？

| 选项 | 代价 |
|---|---|
| 现在就删 | 净删 379 行；但**无回滚路径**，且唯一生成路径未实测 |
| 等 `15` §2 能力矩阵做完再删 | 保持回滚能力；但 379 行的删除被推迟 |

**倾向：等。** 因为 `15` §2 明确登记「❌ 完全未做」，而 Gate G2 本身要求
「能力矩阵实测完成且数字回写」——**能力矩阵没做完，Phase 2 本来就没过关。**

---

## 6-B. ✅ 决策状态总表（2026-10-13 更新）

| # | 决策 | 状态 | 结论 | 落地位置 |
|---|---|---|---|---|
| **D1** | 「对话框」指哪个 | ✅ **已定 = B**（报告 PDF / 报告卡片） | **必须放开 C1**，撞 C2/C4 + AGENTS:48。**但「在报告里看」≠「原文明文进 PDF」**，实现强度三选一（B-1/B-2/B-3），倾向 B-1+B-2 | §5.0-A |
| **D2** | 开关删还是留 | ✅ **已定 = 三段式** | 阶段一零代码验证 → 阶段二反向开关 → 阶段三能力矩阵后再删 | §3.2 / §3.3 / §3.4 |
| **D3** | `token_usage` 修不修 | ⏸️ **暂未定**（倾向：本批不修，另开一批） | 保持写死 0；但须确认 UI 不把 0 显示成「用量正常」 | §4.2 / §3.2.1 第 4 项 |
| **D4** | 平台维度怎么办 | ✅ **已定 = 用户方案**（前端不显示 + 规划时选不到） | **优于本 plan 原建议**。🔴 但必须先修 `direct` 默认值（§6-A.2），并保留后端显式拒绝作为存量兜底 | §6-A |
| **D5** | 原文走哪条路 | 🔶 **部分已定**（D1=B 排除 (a)/(d) 作主路线） | 实际在 **B-1 / B-2 / B-3** 间选择；(b) 落选但**仍应写进产品文档** | §5.0-A.4 |
| **D6** | 删代码现在做还是等 | ✅ **已定 = 等**（D2 三段式的第三阶段） | Gate G2 本来就卡在能力矩阵未做 | §3.4 |

**仍需用户回答的唯一问题**（其余均已定）：
→ **§5.0-A.4 的 B-1 / B-2 / B-3 选哪个。** 这是 D1=B 落地前的最后一道闸。

---

## 6-C. Gate G2 状态更新（阶段一完成后能关哪几条）

`14-Phase2实施计划.md` §7 Gate G2 原文：

> **Gate G2（Phase 2 完成）**：U4~U10 全部交付；能力矩阵实测完成且数字回写；
> `judge_track` 分轨落地；**原生生成在红线未动的前提下跑通**；
> 远程依赖项对用户**可见且可解释**；专家可发布 yaml 插件。

**前提**：阶段一（仅设`PROMPTFOO_ENGINE_NATIVE_GENERATION=1`）完成，
且 §3.2.1 实测清单**全部走完**。

| Gate G2 分句 | 阶段一能否关闭 | 依据 / 仍缺什么 |
|---|---|---|
| **U4~U10 全部交付** | ❌ 不能 | U7 的能力矩阵未做（`15` §2）。**交付 ≠ 验证完成** |
| **能力矩阵实测完成且数字回写** | ❌ 不能 | 阶段一只满足 `15` §2 的**步骤 2**（打开开关）；**步骤 3「29 个插件逐个实测」完全未做**，数字无处可回填 |
| **`judge_track` 分轨落地** | ✅ **已可关闭** | U4 已 completed（task #11）。**注意**：阶段一后 `judge_source` 语义会变窄（§4.5），但 `judge_track` 本身不受影响 |
| **原生生成在红线未动的前提下跑通** | ✅ **可关闭**（**前提是实测清单全过**） | 四个 fail-closed 开关值不变（`env.test.ts` 反证测试全绿）；阶段一实测清单第 1/12/13 项分别证明「开关生效」「无出网」「凭据零泄漏」。**这是阶段一唯一能真正关掉的 G2 分句** |
| **远程依赖项对用户可见且可解释** | ✅ **可关闭**（U6 已交付） | `remoteDepGuard.ts` + `engine_run.go:313` 两道闸 + 前端置灰。**本项与 Plan A/C 无关**，Phase 1 已完成 |
| **专家可发布 yaml 插件** | ✅ **可关闭**（U10 已交付） | `customPlugin.ts` / `customStrategy.ts` 在 Plan C 上完整可用。**但注意**：Plan A 下发布即被拒（`PLAN_A_ERR_CUSTOM_PLUGIN`），阶段三删 Plan A 后该错误码消失，须同步改文档（§7.3） |

**结论**：阶段一完成并跑完实测清单后，Gate G2 的 6 个分句中
**可关闭 4 个**（`judge_track` / 红线未动跑通 / 远程依赖可见 / 专家发布 yaml），
**仍卡 2 个**（U4~U10 全部交付 / 能力矩阵实测回写）——**都因为能力矩阵**。

> **给用户的一句话**：打开开关能让 Gate G2 从「1/6 完成」变成「4/6 完成」，
> 是很有价值的一步；但**它不能替代能力矩阵**，而能力矩阵才是Phase 2 真正的关卡。

---

## 7. 对既有文档的影响

### 7.1 `14-Phase2实施计划.md`需更新的条目

| 章节 | 现状 | 本计划执行后 |
|---|---|---|
| §4.5 双路径并存与开关 | 「Plan A 保留 / Plan C **默认关闭**」 | 若执行阶段二：**默认走 Plan C**，Plan A 需显式开启才能用 |
| §4.6 验收标准第 6 条 | 「Plan A 可回退：关开关 → 行为与 U7 前一致（反证测试）」 | 若执行阶段三：**永远无法满足**，须显式记为「已裁定删除，不再适用」 |
| §2 U8 落地结果 / §2.10.3 | 「报告里的判定基础必须与实际一致」 | 须补：`judge_source` 的 `plugin:<id>` 档在 Plan C 下**含义变窄**（§4.5） |
| §3 远程依赖能力矩阵 | 119/90/29（已核实正确） | **无需改数字**；但须补「该门控是路径无关的」这一事实（§4.3） |
| §4.5 / 新增 | 「未选策略 ⇒ `direct`」（`EngineEvalPage.tsx:283`） | D4 落地后语义变为「**未选 = 不施加任何维度**」，须记一笔（旧报告不可比） |
| §7 Gate G2 | 「能力矩阵实测完成且数字回写」 | **阶段一后 4/6 可关**（见 §6-C）；仍卡「能力矩阵」与由它派生的「U4~U10 全部交付」 |

### 7.2 `15-待验证台账.md` 需新增/修正的条目

| 条目 | 现状 | 应补 |
|---|---|---|
| §2 能力矩阵 | ❌ 完全未做 | 步骤 2「打开 `PROMPTFOO_ENGINE_NATIVE_GENERATION=1`」**在阶段一之后即已满足**，但**步骤 3 的29 插件逐个实测仍未做** |
| §6.1.2 三个既有缺陷 | `simba`/`retry`/`mathPrompt` 归为 `plan_a_only` | 须补：阶段三删 Plan A 后这三个归类**失去意义**（`PLAN_A_ONLY` 表只服务 Plan C 的策略分类） |
| **新增** | — | **「Plan C 下的 `plan_a_only` 维度处置」**——即 §6-A，**已定案**（前端不显示 + 后端显式拒绝 + 修 `direct` 默认值） |
| **新增** | — | **「`token_usage` 全 0 是写死而非未测」**——即 §4.2，本轮实测反证 |
| **新增** | — | **「`direct` 是表单默认策略且属 `plan_a_only`」**——即 §6-A.2，本轮发现的地雷 |
| **新增** | — | **「D1=B 下报告原文的存储形态」**——即 §5.0-A.3 的 Q1~Q4，**待用户回答** |

### 7.3 其他文档

- `evaluating_platform/docs/architecture/custom-plugin-publication.md:118`
  ——「自定义插件仅 Plan C 可执行」在 Plan A 消失后**恒真**，须改写；
  `PLAN_A_ERR_CUSTOM_PLUGIN` 错误码在阶段三后消失，**API 契约变更须记入**。
- `evaluating_platform/docs/architecture/promptfoo-generation-options.md:245`
  —— 该文是 Plan A / Plan C 的**原始决策文档**，移除 Plan A 后须标注「Plan A 已于<日期>移除」。
- `AGENTS.md` —— **D1 = B 已定，故必然需要改**：
  - L47 需增补例外条款（「报告表只存安全摘要」→ 允许在受控条件下存原文）
  - L48 需明确「攻击样例」是**填进已有的 successful attack examples 章节**，
    还是**新增章节**（后者撞「Do not reintroduce … appendices」）——**这个区分决定 B-1/B-3 的实现路径**
  - **建议**：无论选 B-1/B-2/B-3，都加一条显式变更标记，让后来者知道这是**裁定结果，不是疏忽**

---

## 8. 本计划的自我限制

- 本批**只产出本文件**，不改任何 Go/TS 代码、不动任何既有文档。
- §2 的行数、引用点、§4.3 的门控位置、§4.2 的 `token_usage` 写死位置、
  §6-A.1 的策略分档统计（37/4/33/24/9）、§6-A.2 的 `direct` 默认值——
  均为**实测**所得，标注了文件与行号。
- 与上游笔记不一致处**已显式标注为反证**，并给出依据：
  - §4.2 `token_usage`（依据 `redteamRun.ts:754-758`）
  - §4.3 `119→29` 的因果（依据 `routes/runs.ts:50-66` / `remote_dep.json` / `engine_run.go:313`）
  - §4.4 `PLAN_A_ONLY` 数量（**本文档初稿写 13，实测为 12**，已全文修正；
    初稿用正则统计漏了带引号的 key。前端可见的是 **9** 个）
- 用户已定的D1/D2/D4 已回写并标注状态；**唯一仍未替用户决定的是
  §5.0-A.4 的 B-1/B-2/B-3**——已列代价与倾向，等用户裁定。

**—— 计划完。D1/D2/D4/D6 已定，D5（B-1/B-2/B-3）待答 ——**

---

## 9. ✅ D5 已定：**B-3 强度**（原文明文进 PDF 与报告表）+ 契约变更留痕

> **用户裁定（2026-10-13）**：报告采用 **B-3 强度** —— **被测目标的原文
> 明文进入 PDF 与报告表**。
> **用户的理由**：另一条链路（Skill / `execute_redteam_evaluation_batch`）
> 的报告里**本来就有全文**。既然同平台的另一条链路已经这么做，
> promptfoo 链路再按更严的标准执行就只是**两条链路口径不一致**，
> 而不是「更安全」。
>
> 本节是**契约变更的实施说明与留痕**，不是新方案。

### 9.1 现状事实：两条链路的报告口径**不一致**（本轮复核成立）

主理人给出的依据**已复核成立**：

`internal/maclaw/redteam_bridge.go:1015`（`execute_redteam_evaluation_batch` 链路）：

```go
findings = append(findings, EvaluationReportFinding{
    ID:          findingID,
    Title:       b.reportTestPrompt(result.payload.PayloadHandle, runID, userID, sessionID, index),
    Severity:    severity,
    Category:    firstNonEmptyString(result.payload.Metadata["payload_kind"], "batch_payload"),
    Description: firstNonEmptyString(strings.TrimSpace(result.call.TargetResponse), strings.TrimSpace(result.call.Summary), "未获得模型回答。"),
    // …
})
```

**`Description` 直接就是 `result.call.TargetResponse` —— 被测目标返回的原文，
未经任何脱敏。** 且它是**首选值**（第一个参数），只有它为空才回退到
`Summary`，再回退到固定文案。

| 链路 | 报告里有没有被测目标原文 | 依据 |
|---|---|---|
| **Skill / `execute_redteam_evaluation_batch`** | ✅ **有**（全文，明文） | `redteam_bridge.go:1015`（本轮复核） |
| **promptfoo 引擎链路** | ❌ **没有**（只有脱敏汇总：计数/严重度/脱敏理由） | `redteamRun.ts` 的 `reduceEvalToSafeResult`；`SafeRunResult` 只含 counts/severity/reasons |

**所以 B-3 不是「放宽安全标准」，而是「把 promptfoo 链路对齐到既有链路」。**
这个区别很重要：它不是新开一个口子，而是**消除一处不一致**。

### 9.2 需要改的契约（**代码尚未改 · 阶段二/三的工作**）

| # | 位置 | 现在写的是什么 | B-3 要求 |
|---|---|---|---|
| **C1** | `AGENTS.md` **L47** | 「`save_redteam_evidence` and `compile_redteam_report` persist only safe summaries/metadata …; **never store raw target prompts, raw target responses**, payload bodies, evidence content, credentials, tokens, or local paths in those tables.」 | 需增补**例外条款**：报告表（`maclaw_redteam_reports`）的 `findings[].Description` 在受控条件下**允许**存被测目标原文。⚠️ **注意 `maclaw_redteam_evidence` 表本身仍应禁止存原文** —— 证据表与报告表是两回事，不要一起放开 |
| **C2** | `AGENTS.md` **L48** | 「The fixed Chinese report template includes report info, …, **successful attack examples**, …; Do not reintroduce … **appendices**」 | 需明确：原文是**填进已有的 `successful attack examples` 章节**（B-3，**不是新增章节**）。若新增章节会撞上 `Do not reintroduce … appendices` —— **这是 B-1 与 B-3 的实现分岔点**，B-3 恰好避开了它 |
| **C3** | `AGENTS.md` **L49** | 「`planned_count`, `executed_count`, `current_stage`, `duration_ms`, `stage_durations_json` are safe progress metadata …」 | **不需要改**。进度元数据与报告正文是两回事；B-3 不让原文进进度字段 |
| **C4** | **引擎 redact 边界** | `promptfoo_engine/src/sanitize/redact.ts` + `redactReason` / `redactTopReasons`，以及 `reduceEvalToSafeResult` 只输出脱敏汇总 | ⚠️ **这是本批唯一需要改代码的契约点**：当前引擎在**出口处就把原文丢掉了**（`SafeRunResult` 里根本没有原文字段）。B-3 要让原文流到报告，**必须给引擎加一个受控的原文通道** —— 而这会**扩大引擎的输出面**，需要独立设计与评审 |
| **C5** | 两处引擎-清单 | `nativeRedteam.ts` / `promptfooStateCleanup.ts` 的「never logs payload content」承诺 | **不动**。引擎**本地**不落盘/不打日志，与「报告里给不给原文」是**两个正交**的问题。B-3 不改变清理钩子的义务（`17` 实测清单第 2/3 项仍然必做） |

> ⚠️ **C4 是真正的工作量所在，不是 C1/C2/C3**。
> C1/C2/C3 只是把「已发生的口径不一致」写进契约（**追认现状**）；
> C4 才是给引擎加原文通道（**新行为，需设计**）。
> **建议 C4 单独排期，不要和 C1/C2/C3 混在一个批次里** ——
> 后者是文档，后者碰的是安全出口。

### 9.3 🔴 不可逆后果（**知情留痕**）

**报告 PDF 会发给客户 / 存工单 / 进合规归档。一旦原文印上去，
平台就失去对流向的控制。**

这句话必须逐字留在契约里，因为它描述的是一个**无法用代码撤销**的后果：

| 后果 | 说明 | 为什么不可逆 |
|---|---|---|
| **原文一旦印上，就已离开平台** | PDF 进客户邮箱、工单系统、合规归档后，平台**不再持有**这些原文，也无法收回或更正 | 即使后来把服务端的原文删干净，**已经发出去的 PDF 仍在** |
| **被测目标的原文可能含敏感数据** | 目标若在回答里回显了密钥、PII、内部数据，这些会随报告一起外流 | 这正是 `AGENTS.md` L47 原本禁止的—— B-3 是在**知情前提下**接受这个风险 |
| **凭据泄漏是 P0，与本决定正交** | 报告里出现**平台自己的**凭据（不是被测目标的业务内容）**仍然是 P0**，不受 B-3 覆盖 | `17` 实测清单第 7 项（凭据零泄漏）**不受本决定影响，照做** |
| **合规归档不可撤回** | 报告进合规归档后，等于把原文固化成了「组织已留档的记录」 | 合规系统通常有保留期，且不提供删除接口 |

> ✅ **用户已在知情后做出该决定**（理由见 §9 开头的裁定引文）。
> **本节的作用是把这个决定的边界写清楚，不是重新提出质疑。**
> 实现时必须做到：
> 1. 报告上**标明哪些是原文**（让读报告的人知道哪段是被测目标说的、哪段是平台判定的）；
> 2. **凭据类内容仍然强制脱敏** —— 「被测目标的业务原文」与「平台/客户的密钥」必须分开处理，
>    前者放开，后者**永远不放开**；
> 3. 在 `AGENTS.md` 加一条**显式变更标记**，让后来者知道这是**裁定结果，不是疏忽**。

### 9.4 本节的自我限制

- 本批**只改部署配置 + 写实测清单 + 写本节的契约说明**。
- **C1/C2/C3/C4 的代码改动一律未做** —— `AGENTS.md` 未改，`redact.ts` 未改，
  引擎输出面未扩大。本节是**实施说明**，不是完成汇报。
- §9.1 的 `redteam_bridge.go:1015` 已在本轮**重新复核**（非引用上游笔记）。
- §9.2 的 C1/C2/C3 行号与条文引自 `AGENTS.md` 现状（L47/L48/L49），
  **改动它们前需再次确认行号未漂移**。

---

**—— 阶段一（D1~D6 全部已定）落地完。D5 = B-3，契约变更清单见 §9，
代码改动待排期 ——**