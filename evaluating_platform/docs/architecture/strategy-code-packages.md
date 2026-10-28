# 策略代码包（可上传文件、可执行）设计稿

> 状态：**设计稿，未实施**（2026-10-28）
> 前置：本文档 §2 的三项租户级隔离条件**必须先落地**；在此之前不打开入口，也不移除现有拦截。
> 相关：`custom-plugin-publication.md`（发布链路）、`maclaw-current-state.md`（运行形态）、
> `../../audit-2026-10-09/23-聊天链路报告原文与promptfoo对齐.md` §31 之后（决策背景）

---

## 0. 一页结论

| 问题 | 答案 |
|---|---|
| promptfoo 支持代码策略吗 | **支持**。`loadStrategy()` 对 `file://` 路径做 `importModule()`，要求模块导出 `{id, action}`（`dist/src/strategies-*.js:2436`） |
| 现在为什么拦 | 不是"不支持"，是**执行位置**：策略 `action` 在引擎主进程里被直接调用，而引擎是全局唯一的，进程内可达平台级生成模型 key、所有 run 的靶机 key、共享数据库与自身 bearer token |
| 打开的前提 | **执行体与 Skill 同档**：每租户一引擎、生成模型 key 取租户配置、实例级 token。三者缺一不可 |
| 打开后上限 | 与 Skill 等价：能读到**本租户**的 key、能改坏**本租户**的用例；碰不到别的租户 |
| 本批交付 | 单文件 JS 策略包（`form=js`）+ 发布期引擎实测校验 + 运行期输出校验 + 治理与可见性 |

**一条不能含糊的话**：这不要被写成"黑名单换成了更好的扫描器"。黑名单（`file://` / `javascript:` /
`child_process` 等）**从来不是安全边界**，它只是提高门槛。真正的边界是 §2 的隔离，以及 §6 的输出校验。

---

## 1. 目标与非目标

### 1.1 目标

1. 专家可以发布**含代码**的策略包（JS 模块），企业评测时被加载执行；
2. 代码能做 `strategyText` 做不到的事：自定义编码算法、按用例内容做条件变换、确定性重排/裁剪；
3. 执行结果**可校验、可追溯、可停用**，失败不静默；
4. 与现有 `custom:<variant>` 策略**并存**，不替换。

### 1.2 非目标（本批不做）

| 不做 | 理由 |
|---|---|
| 多文件 / ZIP / npm 依赖 | 引入解压与依赖解析两条新攻击面；单文件足够覆盖算法类需求 |
| 调用 LLM 的能力 | 需要凭据；v1 要求策略是**纯函数**（见 §4.3）。要 LLM 的场景用 `custom:<variant>` 的 `strategyText` |
| 跨租户共享一份执行体 | 破坏 §2 的隔离前提 |
| 前端代码编辑器 / 语法高亮 | 先用文件上传 + 纯文本回显；编辑器另议 |
| 运行时热更新 | 每次 run 从发布记录重读（与现有 resolver 一致），不做常驻缓存 |

---

## 2. 信任模型与前置条件（Gate）

### 2.1 与 Skill 对齐的信任档

平台**已经接受**"专家代码 + 租户级 LLM key"这个档位（`ccbos-classical-chinese-skill` 等，
运行时由 MaClawSrv 注入 `MACLAW_LLM_*`）。策略代码包的目标就是**同一档**，不多也不少。

差距不在"有没有凭据"，而在三处（2026-10-28 核实）：

| | Skill | 策略代码（现状引擎） |
|---|---|---|
| key 作用域 | 当前**租户** LLM 配置 | **平台默认**生成模型（`GetDefaultRuntimeConfig` 无账号参数） |
| 同进程可达 | 本租户工作区 | 所有 run 的 `/data/runs/<run>/redteam.yaml`（各租户靶机 key）+ 共享 `promptfoo.db` + 引擎 bearer token |
| 隔离单位 | maclaw 每账号 instance/tenant | 全局唯一 `ep_promptfoo_engine` |

### 2.2 三项前置条件（**缺一不可**）

| # | 条件 | 现状 | 验收 |
|---|---|---|---|
| G-1 | **每租户引擎实例**：独立容器、独立 `/data/runs` 卷、独立 `~/.promptfoo` | ❌ 单实例 | 起两个租户的评测，互相查不到对方的 run 目录与数据库 |
| G-2 | **生成模型 key 取租户配置**（不再是平台默认） | ❌ 平台级 | 租户 A 的引擎进程里，`redteam.yaml` 的 key 与租户 B 的不同；A 的 key 在 B 的引擎里不存在 |
| G-3 | **实例级 bearer token**：一台引擎的 token 不能调另一台 | ❌ 全局同一 token | 拿 A 的 token 请求 B 的 `/runs` → 401/403 |

> **G-1 的附带收益**：单 worker 目前导致全局串行排队；每租户独立 worker 后，租户之间并行，
> 这是本项目已知的"长跑阻塞"问题（40 号记录 §3.1）的正解。
>
> **G-1 的成本**：常驻内存 × 租户数，或改成按需拉起/缩容到 0（后者需要新的生命周期管理与冷启动预算）。
> 这一项要单独立项评估，不在本设计里定案。

### 2.3 在此之前的拦截保持不变

- `customStrategyExecutableTokens` 与外部地址拦截**继续保留**；
- 文档措辞改为"执行位置在租户级隔离之前不适合"，而不是"平台不信任专家代码"（后者与 Skill 的事实矛盾）。

---

## 3. 包格式与上传

### 3.1 形态

| 项 | 取值 |
|---|---|
| `kind` | `pf_strategy`（复用现有） |
| `pf_strategy_form` | **`js`**（新值；现有 `yaml` 继续支持） |
| 正文 | **单个 ESM 文件**（`.mjs` / `.js`），UTF-8 |
| 大小上限 | **256 KB**（算法类代码；与插件包同档。超过多半是在塞数据） |
| 其他元数据 | 复用现有：`name` / `summary` / `version` / `pf_strategy_variant` / `pf_strategy_depends_on` |

### 3.2 模块契约（发布期即校验）

```js
// 必须导出对象，且 action 是函数。
// 注意：promptfoo 的报错文案写的是 "must export an object with 'key' and 'action'"，
// 但源码实际检查的是 `id`（dist/src/strategies-*.js:2442）——以源码为准，§12 Step 0 会实测确认。
export default {
  id: 'custom:my-code-strategy',      // 见 §3.3 的 id 规则
  action(testCases, injectVar, config, strategyId, context) {
    // 纯函数：返回新的 testCases 数组（可 async）
    return testCases.map((tc) => ({ ...tc, vars: { ...tc.vars, [injectVar]: transform(tc.vars[injectVar]) } }));
  },
};
```

### 3.3 id 规则（**待实测后定稿**）

- 报告里的策略标识来自 `testCase.metadata.strategyId`；它必须是一个**能解释**的 id；
- 风险：若模块 id 以 `custom:` 开头，promptfoo 的 `validateStrategies()` 会按
  `custom` 策略要求 `config.strategyText`，而代码策略没有这个字段 → 可能中途抛错；
- 因此 v1 建议：模块 id 必须形如 **`code:<variant>`**（平台命名空间），由平台在
  记录/报告里映射为 `custom:code:<variant>` 展示；§12 第 3 条负责实测这条路径。
- **禁止**：id 为空、含 `/` `..`、超过 64 字符、与内置 35 条策略同名。

---

## 4. 执行契约

### 4.1 调用点（实测自 promptfoo 调用处，`dist/src/index.js:16506-16517`）

```js
strategyAction = (await loadStrategy(strategy.id)).action;   // file:// → import()
...
// 调用形态（与内置策略一致）：
strategyAction(testCases, injectVar, { ...strategy.config }, strategy.id, { generationProviderSelection })
```

### 4.2 输入与输出

| 方向 | 内容 | 约束 |
|---|---|---|
| 输入 | `testCases[]`（含 `vars[injectVar]`、`metadata`） | 只读快照：平台在传入前**深拷贝**，策略改不到原对象 |
| 输出 | `testCases[]` | 必须满足：数组、长度 ≥1 且 ≤ 输入长度 × 2、每项含非空 `vars[injectVar]`、`metadata.pluginId` 保留 |
| 输出上限 | 单条 payload ≤ 64 KB，总量 ≤ 2 MB | 超限按失败处理（§9） |

### 4.3 纯函数要求（v1 硬约束）

- **禁止网络**：不得 `fetch` / `http.request` / 动态 `import()` 远程 URL。运行期无法完全阻断（同进程），
  因此 v1 靠**发布期静态扫描 + 本租户隔离**兜底，并在报告里注明"本次运行加载了代码策略"（§8.3）。
- **禁止读取环境变量与文件**：静态扫描 `process.env`、`fs`、`child_process`、`require`/`import`；
  命中即拒绝发布。这条同样是**提高门槛**，不是边界（边界见 §2）。
- **禁止写文件**：同上。
- **必须可中断（尽力）**：平台用 `Promise.race` 施加 wall-clock 上限（默认 **20s**，可配置）。
  同步死循环**无法被中断** —— 这是 A″ 档位的已知代价（§13 风险 R-2），靠租户级引擎 + 运维重启兜底。

### 4.4 确定性

同样的输入必须产出同样的输出（便于复现与报告对比）。**禁止** `Math.random()` / `Date.now()` /
`crypto.randomUUID()` 出现在输出路径上（静态扫描告警，不阻断；命中的策略在报告里标记"非确定性"）。

---

## 5. 加载路径（引擎侧）

```
/data/runs/<run-id>/
  custom-plugins/        ← 现有（插件包）
  custom-strategies/     ← 新增
    <variant>.mjs        ← 由引擎写入，文件名由平台生成，不由包内容决定
  redteam.yaml
```

要点：

1. **文件名与路径由引擎生成**，只从 `variant` 派生并做白名单校验（`[A-Za-z0-9._-]{1,64}`），
   绝不做路径拼接拼接来自包内容的字符串；
2. 配置里写的是 **`file://` + 绝对路径**，但绝对路径由引擎拼：
   `safeJoin(state.basePath || cwd, filePath)` 对**绝对路径原样返回**（实测 `esm-*.js:55-59`），
   所以引擎必须自己断言该路径落在本 run 的 workdir 内（`path.resolve` + `startsWith(workdir)`）；
3. **workdir 终态即删**（现有 `promptfooStateCleanup`），代码包不跨 run 存活；
4. 停用的包在 run 准备阶段就被 resolver 拒绝，**不写盘、不 import**（fail-closed 顺序与插件包一致）。

---

## 6. 准入与校验

### 6.1 发布期（BFF + 引擎两步）

| 步骤 | 位置 | 检查 |
|---|---|---|
| 1 | BFF 存储出口 `SaveResource`（唯一写入口） | `pf_strategy_form=js`；大小；UTF-8；禁止 token（`file://` / `javascript:` / `child_process` / `process.env` / `fs` / `require(` / 外部 URL / `eval(` / `new Function`）；variant 合法 |
| 2 | **引擎实测校验**（新增 `POST /strategy-packages/validate`） | 在**一次性子进程**里 `import()` 该模块，5s 超时，检查导出形状 `{id, action}`；只返回安全判定（`{ok, errors:[{code, message}]}`），**不回显代码、不回显路径** |
| 3 | 记录 | 校验通过后才写发布记录；失败给可读原因（沿用 `promptfooStrategyRejectionHints` 的形状） |

> 为什么要有第 2 步：Go 侧无法可靠判断"这是不是一个合法的 JS 模块"。只做文本扫描会放过
> "语法正确但形状不对"的包 —— 那种包会在**评测中途**才炸，而用户已经付了探针费用。
> 第 2 步把失败前移到发布时。

### 6.2 运行期

| 检查 | 动作 |
|---|---|
| 导出形状 | 加载后再次校验 `{id, action}`；不合法 → run 失败（不是降级） |
| 输出 schema | §4.2；不合法 → run 失败 |
| 超时 | §4.3；超时 → run 失败，`error_brief` 记 `strategy_code_timeout` |
| 输出指纹 | 对输出 testCases 计算 sha256，写入 run 元数据（不含内容），供复现比对 |

---

## 7. 隔离边界（现状 + 需新增）

| 层 | 现状 | 本设计是否改动 |
|---|---|---|
| 容器只读根 / `cap_drop: ALL` / 非 root / tmpfs noexec | ✅ 已有 | 否 |
| `REQUEST_TIMEOUT_MS`（外发 HTTP 上限） | ✅ 已有（180s） | 否 |
| 每租户实例 + 租户级 key + 实例 token | ❌ 缺 | **§2 Gate，必须前置** |
| 策略执行 wall-clock 上限 | ❌ 缺 | 新增（`Promise.race`，§4.3） |
| 输出 schema + 指纹 | ❌ 缺 | 新增（§6.2） |

> 明确说明：容器加固**管不到"同进程内读取"**。`read_only` 挡写不挡读；`cap_drop` 管能力不管
> `process.env`。所以 §2 的三项是本设计的**承重墙**，不是加分项。

---

## 8. 治理与可见性

### 8.1 治理（复用现有）

- 管理员：停用 / 归档，**对下一次 run 即时生效**（resolver 每次重查，不缓存）；
- 专家：下架自己的包（`POST /maclaw/evaluation/resources/:id/archive`）；
- 重新发布：按 id 更新同一包（现有语义），版本递增便于追溯。

### 8.2 目录与选择

- 企业侧插件/策略下拉出现「专家发布」分组，**代码策略标注「代码」标签**（与文本策略区分）；
  用户有权知道这次运行会执行专家代码。
- 计划卡同理：`strategy_kind=code` 时在卡片上显示标记。

### 8.3 报告

| 项 | 要求 |
|---|---|
| 标记 | 报告基本信息增加一行：「本次运行加载了 1 个专家代码策略（<name> v<version>）」 |
| 不出现 | 代码内容、文件路径、`file://` 引用、包 sha |
| `strategy_stats` | 使用包自身的策略 id（§3.3 映射后），保证与报告可对账 |

---

## 9. 失败与降级（不静默）

| 场景 | 行为 |
|---|---|
| 加载失败（语法错、形状不对） | run **失败**，`error_code=strategy_code_invalid`，`error_brief` 说明是哪个包 |
| 超时 | run **失败**，`strategy_code_timeout` |
| 输出不合法 | run **失败**，`strategy_code_output_invalid`，指明违反的规则（长度/schema） |
| 策略抛异常 | run **失败**，`strategy_code_failed`（只记错误类型与位置，不记堆栈中的路径） |
| 包被停用/下架 | run **拒绝**（409 `pf_strategy_ref_unavailable`），与现有文本策略一致 |

**不允许**"跳过该策略继续跑"：那会让报告声称覆盖了一个从未施加的手法。

---

## 10. 迁移与回滚

| 项 | 方案 |
|---|---|
| 数据迁移 | 无。`pf_strategy_form` 只是元数据新取值；历史 `yaml` 包不受影响 |
| 开关 | `MACLAW_STRATEGY_CODE_PACKAGES=off`（默认 off）。off 时：入口隐藏、`form=js` 拒绝、resolver 拒绝 |
| 回滚 | 关开关即可；已发布的 JS 包保留（不执行），不删数据 |
| 兼容 | `custom:<variant>` 文本策略路径完全不改 |

---

## 11. 测试与验收判据

### 11.1 必须的守卫（每条都要有反证）

| # | 判据 | 反证 |
|---|---|---|
| 1 | 合法 JS 包可发布、可被选中、可执行，`strategy_stats` 出现其 id | 摘掉加载接线 → 红 |
| 2 | 形状不对的包在**发布时**被拒（不是运行时） | 跳过引擎校验步骤 → 红 |
| 3 | 输出不合法（空数组 / 缺 `vars[injectVar]` / 超限）→ run 失败 | 去掉 schema 校验 → 红 |
| 4 | 超时 → run 失败且 worker 释放 | 去掉 `Promise.race` → 红 |
| 5 | 停用的包在 run 准备阶段被拒，且**不写盘** | 去掉治理检查 → 红 |
| 6 | 报告标注"加载了代码策略"，且不含路径/代码 | 去掉标记 → 红 |
| 7 | `file://` 路径必须落在本 run workdir 内 | 改成直接使用包内路径 → 红 |
| 8 | 开关 off 时发布与执行都被拒 | 忽略开关 → 红 |

### 11.2 上机判据（需 G-1~G-3 就绪）

1. 租户 A 的代码策略执行时，读不到租户 B 的 run 目录与数据库（**实测**：策略内尝试枚举 `/data/runs`）；
2. A 的引擎 token 调 B 的 `/runs` → 403；
3. 同一策略两次运行输出指纹一致（确定性）；
4. 停用后下一次评测被拒，无需重启。

---

## 12. Step 0 实测结果（2026-10-28，已完成）

源码阅读给出的三条事实，并由 `promptfoo_engine/tests/strategyCodePackages.step0.test.ts` 实测确认：

| # | 待测 | 结果 |
|---|---|---|
| 1 | `file://` 策略能否过 `validateStrategies()` / schema | ✅ 能。`validateStrategies()` 第 2419 行显式 `continue` 跳过 file://；`strategyIdSchema` 第二个分支接受 `file://…js\|ts` |
| 2 | `action` 的真实签名 | ✅ `action(testCases, injectVar, config, strategyId, context)`，同步与异步都接受；返回值里 `null`/`undefined` 项会被过滤（`applyStrategies` 里 `filter(t => t != null)`） |
| 3 | 模块 `id` 是否进 `strategy_stats` | ❌ **不进**。`applyStrategies` 写的是 `strategyId: t?.metadata?.strategyId \|\| strategy.id`，而 `strategy.id` 是 **file:// 路径**；模块导出的 `id` 从不被读取 |
| 4 | `custom:` 前缀是否触发 `strategyText` 校验 | ✅ 不触发（file:// 在第 2419 行就跳过了），但模块 id 本来也不进这条链路 —— 见 #3 |
| 5 | 报告里是否出现本地路径 | ❌ **会**。`case_results[].strategy_id` 与 `strategy_stats[].id` 都是 `file:///…/custom-strategies/probe.js` |

#3/#5 是这一批最重要的发现，**推翻了原设计 §3.3 的假设**（"模块 id 会进 metadata"）。由此定下两条实现约束：

1. **引擎必须在归约前把 file:// 路径换回公开 id**（`normalizeCodeStrategyIds`），否则报告会带本地路径 —— AGENTS.md 明令禁止；
2. **代码包必须显式声明 variant**（`pf_strategy_variant`）：既然 promptfoo 记录的 id 是路径，"两个代码策略如何区分"只由平台这一侧决定；不填就会和历史句柄派生出同一个 `custom:strategy`。

实施状态见 §15。

---

## 13. 风险登记

| # | 风险 | 等级 | 处置 |
|---|---|---|---|
| R-1 | G-1~G-3 未就绪就打开入口 → 暴露面=整台部署 | **高** | 开关默认 off；发布页与文档都写明前置条件；CI 断言开关与前置能力同时具备 |
| R-2 | 同步死循环无法中断，占住租户唯一 worker | 中 | 租户级隔离把影响限制在该租户；运维重启；文档与发布页明示 |
| R-3 | 静态扫描被绕过（字符串拼接、编码） | 中 | 明确它不是边界；真边界是 §2 + §6.2 |
| R-4 | 代码策略污染报告结果（篡改 testCases 以规避判分） | 中 | 输出 schema + 指纹；报告标注；可疑运行可由管理员下架 |
| R-5 | 租户级引擎带来的资源成本（N 份常驻） | 中 | 独立立项评估（按需拉起/缩容到 0） |
| R-6 | promptfoo 升级后 `file://` 契约变化 | 低 | Step 0 的实测清单纳入升级 checklist |

---

## 14. 交付拆分（建议顺序）

| 步骤 | 内容 | 依赖 |
|---|---|---|
| S-1 | §12 的五项实测（只写测试，不上线） | — |
| S-2 | G-1~G-3（每租户引擎 / 租户级 key / 实例 token） | 独立立项 |
| S-3 | 包格式 + 发布期引擎校验 + 开关（默认 off） | S-1、S-2 |
| S-4 | 运行期执行 + 输出校验 + 超时 + 失败码 | S-3 |
| S-5 | 治理与可见性（标签、报告标记、下架） | S-4 |
| S-6 | 上机判据（§11.2） | S-5 |

---

## 15. 实施状态（2026-10-28）

### 15.1 已完成（本地，未上机）

| 层 | 内容 | 文件 |
|---|---|---|
| 形态 | `pf_strategy_form=js` 识别；发布元数据**携带**而非强制 `yaml` | `promptfoo_custom_strategy.go` |
| 存储 | 代码包走独立的解析校验（大小 / 纯函数令牌 / shape / 显式 variant） | `promptfoo_custom_strategy_code.go` |
| 解析 | 文本包与代码包**分流**成两个通道（`CustomStrategies` / `CustomStrategyCode`） | `promptfoo_custom_strategy_resolver.go`、`promptfoo_engine_bridge.go` |
| 下发 | 引擎请求新增 `custom_strategy_code` | `promptfoo_engine_client.go` |
| 引擎 | 物化到 `custom-strategies/<variant>.js`；只放行本次运行物化的 file:// ref；归约前把路径换回公开 id | `customStrategyCode.ts`、`nativeRedteam.ts` |
| 引擎 | 调用方自带的 `file://` 一律拒绝（`native_generation_strategy_unsupported`） | `nativeRedteam.ts` |
| 前端 | 策略发布页增加「文本策略 / 代码策略」切换，模板随形态切换；代码形态要求显式策略标识 | `PromptfooStrategyPublisher.tsx` |

### 15.2 守卫与反证（全部已跑红）

| 守卫 | 反证 |
|---|---|
| 引擎 `customStrategyCode.test.ts`：物化命名、路径穿越拒绝、纯函数令牌、同文件名冲突、id 归一化、端到端（公开 id + 无路径）、调用方 file:// 拒绝 | 去掉 id 归一化 → 报告里出现 `file://`；去掉令牌检查 → 接受 `process.env`；放行任意 file:// → 用例挂起（会真的去 import 任意路径） |
| BFF `promptfoo_custom_strategy_code_test.go`：接受纯模块、拒绝能力令牌（逐个点名）、拒绝坏形状、两种形态互不串道、存储记录 form、解析分流、代码包必须显式 variant | 去掉附带 form 的改动 → 存成 `yaml`（文本包与代码包会串道）；去掉分流 → 代码包进了文本通道 |
| `strategyCodePackages.step0.test.ts` | 记录 Step 0 的五条实测事实（含"模块 id 不被使用""报告带本地路径"两条反面事实） |

### 15.3 尚未完成（上机前置与后续）

| # | 项 | 状态 |
|---|---|---|
| S-2 | **G-1~G-3**：每租户引擎 / 租户级生成模型 key / 实例 token | **未做**。这是打开入口的硬前置（见 §2.2） |
| S-3 | 发布期**引擎实测校验**（`POST /strategy-packages/validate`，子进程 import + 一次 action 调用，只回 `{ok, code, message}`） | ✅ 已做；BFF 在写发布记录**之前**调用，引擎不可达则拒绝发布 |
| S-3b | 总开关 `MACLAW_STRATEGY_CODE_PACKAGES`（默认 off，拒绝发生在任何写盘之前） | ✅ 已做 |
| S-4 | 运行期 wall-clock 超时（默认 20s，`MACLAW_STRATEGY_CODE_TIMEOUT_MS`）与输出 schema 校验 | ✅ 已做（见 §15.3b） |
| S-5 | 报告标注"本次运行加载了 N 个专家代码策略"（计数，不含源码/路径） | ✅ 已做 |
| S-5b | 目录「代码」标签、失败码细分 | 未做 |
| S-6 | 上机判据（跨租户不可读、token 不通用、确定性、停用即拒绝） | 未做 |

### 15.3b 超时与输出校验的实现方式（与 §4 的差异）

§4 设想在 `applyStrategies` 外部包一层。Step 0 之后改为**生成 wrapper 文件**：promptfoo 只接受
`{id, action}`，引擎无法 hook 调用点，所以物化时写两个文件 ——

```
custom-strategies/<variant>.module.js   ← 专家提交的模块原文
custom-strategies/<variant>.js          ← 生成的 wrapper：import './<variant>.module.js'
                                          再导出 { id: publicId, action: guarded(...) }
```

wrapper 做三件事：wall-clock 超时、输出 schema 校验（空数组/条数上限/缺 injectVar/单条与总量上限）、
把 id 固定成公开 id。package 用**裸相对路径** import，无法走出目录。

**超时的边界**：`Promise.race` 只能约束"永不 settle 的 promise"；同步死循环仍会占住 worker ——
这是进程内执行的固有代价（§13 R-2）。

### 15.3c 发布期校验的取舍

`ValidateCodeStrategy` 不可达时**拒绝发布**（而不是"先存着、跑的时候再说"）：一个从未加载过的模块，
其失败会在用户已经付费的 run 里出现，且看起来像引擎故障。拒绝发布的代价是发布方需要重试。

子进程只回 `{ok, code, message}`；模块 `console.log` 的内容、堆栈、路径都不回传（有守卫钉住）。

### 15.4 现在打开会发生什么（必须明确）

- 开关尚未实现，因此**代码包可以发布、可以下发、可以执行**；
- 但 **G-1~G-3 未做**：执行的引擎仍是全局唯一实例，进程内可达平台级生成模型 key 与所有 run 的靶机 key；
- 结论：**在 S-2 完成前，不应在共享/生产部署上发布代码策略**。开发/单租户验证环境可以用，且应视为"执行专家代码"的完整信任敞口。
