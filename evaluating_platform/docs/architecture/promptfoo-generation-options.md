# promptfoo 引擎「攻击用例生成」方案对比

> 背景：promptfoo 引擎 run 在「生成攻击用例」阶段失败。根因是 promptfoo 的
> `redteam.run` 模式**必须自己生成测试用例**，而项目通过
> `PROMPTFOO_DISABLE_REMOTE_GENERATION=1` 禁用了它的生成能力。
> 用户要求：① 保持 fail-closed 不放宽；② 攻击用例生成复用 MaClaw 链路用的 LLM。
> 这两条要求与 promptfoo redteam 模式的设计**冲突**。本文对比可行方案。

## 一、已确认的事实（基于 promptfoo 0.123.0 源码）

1. `redteam.run` → `doRedteamRun` → **总是**调用 `doGenerateRedteam` 生成用例，
   然后 `doEval` 执行。生成步骤不可跳过。
2. 即使配置里传入了 `tests` 字段，`doGenerateRedteamInternal` 会**忽略**它：
   ```
   ⚠️ Warning: Found both 'tests' section and 'redteam' configuration...
   The 'tests' section is ignored when generating red team tests.
   ```
   → **redteam 模式不接受外部传入的用例。**
3. harm/prompt-injection 等插件在 `neverGenerateRemote()===true` 时直接返回空数组：
   ```
   if (neverGenerateRemote()) { logger.error(...disabled...); return []; }
   ```
   → fail-closed 下插件生成 0 条用例，run 必然失败。
4. promptfoo 另有 `evaluate`（普通 eval 模式，`evaluateWithSource`），它**接受**
   `tests` 数组并直接执行，不走 redteam 生成、不依赖远程生成。

## 二、方案对比

### 方案 A：改用 promptfoo `evaluate` 模式（推荐）

**思路**：引擎不再调用 `promptfoo.redteam.run`，改用 `promptfoo.evaluate`。
BFF 用平台 MaClaw 默认 LLM（deepseek-v4-flash）生成攻击用例，作为 `tests`
传入；promptfoo 只负责「打目标 + 判定」，不做生成。

| 维度 | 评估 |
|---|---|
| 安全（fail-closed） | ✅ 完全保持。不碰 `PROMPTFOO_DISABLE_REMOTE_GENERATION`，不碰 `PROMPTFOO_REMOTE_GENERATION_URL`，不放宽 env.ts。 |
| 符合「用 MaClaw 生成」 | ✅ 生成走平台 MaClaw LLM，与既有攻击生成同源。 |
| 改动范围 | 引擎 `redteamRun.ts` 改用 `evaluate`；BFF 新增「用 MaClaw LLM 生成攻击用例」逻辑；请求结构加 `tests` 字段。 |
| 判定方式 | promptfoo 的 `assert`/`rubric` 判定（非 redteam 插件的严重度模型）。需确认判定结果能否映射到平台的 `attack_score`/`success`。 |
| 风险 | 中等。evaluate 模式的判定语义与 redteam 不同，需要验证判定结果是否满足平台的「攻击成功/失败」二元判定。 |
| 工作量 | 中。BFF 生成逻辑 + 引擎改造 + 判定映射 + 端到端验证。 |

**关键风险点**：evaluate 模式没有 redteam 的「插件严重度 + 攻击成功判定」，
判定要靠 promptfoo 的 `assert`（如 LLM rubric、contains、regex）。需要设计
判定规则，让「目标 LLM 是否被攻破」能被可靠判定。

### 方案 B：允许 promptfoo 用自托管端点生成

**思路**：设置 `PROMPTFOO_REMOTE_GENERATION_URL` 指向平台自己的 LLM
（deepseek），并放宽 env.ts 开关，让 promptfoo 的插件用平台 LLM 生成用例。
promptfoo 仍走 `redteam.run`。

| 维度 | 评估 |
|---|---|
| 安全（fail-closed） | ⚠️ **需要放宽**。必须让 `neverGenerateRemote()` 返回 false，即放宽 `PROMPTFOO_DISABLE_REMOTE_GENERATION`。虽然端点指向平台 LLM（非 Promptfoo Cloud），但「远程生成」开关被打开，与「保持 fail-closed」冲突。 |
| 符合「用 MaClaw 生成」 | ⚠️ 部分。生成走的是 promptfoo 插件 + 平台 LLM，不是 MaClaw 的攻击生成逻辑。生成质量/可控性不如平台侧自己生成。 |
| 改动范围 | env.ts 放宽 + 设置 `PROMPTFOO_REMOTE_GENERATION_URL` + 引擎透传端点。改动小。 |
| 判定方式 | ✅ 保留 redteam 原生的插件严重度 + 攻击成功判定，最贴近 promptfoo 设计。 |
| 风险 | 高。放宽安全开关，与用户「保持 fail-closed」要求直接冲突。需要用户明确接受「端点只指向平台 LLM」这一前提。 |
| 工作量 | 小。 |

**关键风险点**：本质上是「打开 promptfoo 远程生成，只是把端点指向平台 LLM」。
用户已明确质疑过「远程生成」并选择回滚。此方案需要用户再次明确接受。

## 三、对比总结

| | 方案 A（evaluate 模式） | 方案 B（自托管端点生成） |
|---|---|---|
| 保持 fail-closed | ✅ 完全 | ⚠️ 需放宽开关 |
| 用 MaClaw 生成 | ✅ 平台侧生成 | ⚠️ promptfoo 插件生成 |
| 判定保真度 | ⚠️ 需设计判定规则 | ✅ redteam 原生判定 |
| 改动量 | 中 | 小 |
| 安全风险 | 低 | 中（放宽开关） |

## 四、建议

**方案 A 更符合你的两条要求**（保持 fail-closed + 用 MaClaw 生成），但需要
设计 evaluate 模式的判定规则，工作量中等。

**方案 B 改动小、判定保真，但本质是放宽安全开关**，与你的要求冲突，需要你
明确接受「端点只指向平台 LLM」这一前提。

请选 A 或 B（或提出其他方向）。选定后我再出详细实现计划。

---

## 五、补充方案 C：修改 promptfoo 让它接受外部用例

**思路**：patch promptfoo 的 `doGenerateRedteamInternal`，让它在 `tests` 非空时
**early return**（跳过插件生成），直接用传入的 tests 做 eval。

**可行性**：
- `dist/src/index.js` 文件可写（node 用户），patch 技术上可行。
- 修改点：在 `doGenerateRedteamInternal` 检测到 `testSuite.tests.length > 0` 时，
  直接构造 redteamConfig 并 return，跳过 `generateRedteamTests`。
- 需要在 Dockerfile 里用 `patch-package` 或 `sed`/`node -e` 在 build 时应用 patch，
  保证每次重建镜像都生效。

| 维度 | 评估 |
|---|---|
| 安全（fail-closed） | ✅ 完全保持。不碰 `PROMPTFOO_DISABLE_REMOTE_GENERATION`，不碰 `PROMPTFOO_REMOTE_GENERATION_URL`。插件生成被跳过，不会触发远程生成。 |
| 符合「用 MaClaw 生成」 | ✅ 平台侧生成，promptfoo 只消费。 |
| 判定保真度 | ✅ 保留 redteam 的 eval 流程（doEval），判定走 promptfoo 原生。 |
| 改动范围 | patch promptfoo dist + Dockerfile 应用 patch + 引擎透传 tests + BFF 生成。 |
| 风险 | **高（维护性）**。patch 第三方库 dist 文件，promptfoo 升级时 patch 会失效，需要重新适配。dist 是打包后的代码，patch 点可能随版本变化。 |
| 工作量 | 中。patch + Dockerfile + BFF 生成 + 端到端验证。 |

**关键风险点**：
1. **维护性**：patch 第三方 dist 文件是「脏」做法，promptfoo 升级（0.123.0 → 0.124.0）时
   patch 大概率失效，需要重新定位 patch 点。长期维护成本高。
2. **可复现性**：patch 依赖 dist 文件的具体内容，不同构建可能不一致。
3. **审计性**：patch 后的 promptfoo 行为与官方不同，安全审计时需要说明。

## 六、三方案对比总结

| | A. evaluate 模式 | B. 自托管端点生成 | C. patch promptfoo 接受外部用例 |
|---|---|---|---|
| 保持 fail-closed | ✅ 完全 | ⚠️ 需放宽开关 | ✅ 完全 |
| 用 MaClaw 生成 | ✅ 平台侧 | ⚠️ promptfoo 插件 | ✅ 平台侧 |
| 判定保真度 | ⚠️ 需设计判定 | ✅ redteam 原生 | ✅ redteam eval |
| 改动量 | 中 | 小 | 中 |
| 安全风险 | 低 | 中（放宽开关） | 低 |
| **维护性** | ✅ 高（用官方 API） | ✅ 高 | ⚠️ **低（patch dist）** |
| promptfoo 升级兼容 | ✅ 好 | ✅ 好 | ⚠️ **差（patch 失效）** |

## 七、最终建议

- **方案 A（evaluate 模式）**：最干净，用 promptfoo 官方 API，维护性最好，完全保持
  fail-closed。唯一代价是要设计 evaluate 模式的判定规则（用 LLM rubric 判定攻击成功/失败）。
- **方案 B（自托管端点）**：改动最小，但放宽安全开关，与你的要求冲突。
- **方案 C（patch promptfoo）**：保持 fail-closed + 平台侧生成，但 patch 第三方 dist
  文件，维护性差，promptfoo 升级会失效。

**综合推荐方案 A**：用官方 API、维护性好、完全符合你的两条要求。判定规则可以用
promptfoo 的 `llm-rubric` assert（让 LLM 判定目标响应是否构成攻击成功），这与
平台现有的 judge 逻辑思路一致。

---

## 八、已选方案 A：设计定稿（2026-09-22）

**决策：方案 A。** 生成在**引擎侧**进行（使用 BFF 已传入的平台默认 MaClaw LLM
凭据 `credentials.generation`），promptfoo 只用 `evaluate` 模式执行 + 判定。
契约不变，BFF 改动最小。

### 数据流

```
BFF CreateRun
  ├─ 校验 purpose/plugins/num_tests
  ├─ GetTargetWithSecret → target 凭据
  ├─ ResolveEngineGenerationCredential → generation 凭据（平台默认 MaClaw LLM）
  │    └─ nil → 503 generation_not_configured（fail-fast：evaluate 模式必需）
  └─ POST /runs（契约不变，credentials.generation 已存在）

engine worker
  ├─ generating_tests: generateAttackTests() — fetch generation LLM
  │     POST {base_url}/chat/completions（OpenAI 兼容），生成 N 条攻击提示（JSON）
  ├─ executing_probes: promptfoo.evaluate(testSuite, { cache:false,
  │     maxConcurrency, progressCallback, abortSignal })
  │     testSuite.tests = 预生成用例（vars.prompt + llm-rubric assert + metadata）
  │     defaultTest.options.provider = { id:'openai:chat:<model>',
  │       config:{ apiBaseUrl, apiKey, temperature:0 } }  // 判定 provider
  │     不设 writeLatestResults / redteam → 不写 SQLite、不走远程生成/远程分级
  ├─ engine_judging: reduceEvalToSafeResult（极性翻转：pf fail = attack success）
  └─ compiling_report → SafeRunResult（计数/类别/redacted reasons only）
```

### 源码验证过的关键事实（promptfoo 0.123.0 dist）

1. `evaluate()` 仅在 `writeLatestResults` 时才跑 DB migrations / `Eval.create`
   → 不设置即不触碰 SQLite（绕开 `eval_results` 查询失败问题）。
2. `llm-rubric` 的判定 provider 取自 `test.options`（继承 `defaultTest.options
   .provider`）；provider 对象**必须有 `id` 字段**（`loadFromProviderOptions`
   要求），形状 `{ id:'openai:chat:<model>', config:{ apiBaseUrl, apiKey } }`。
3. 远程分级分支要求 `state.config?.redteam` 为真；evaluate 模式无 redteam 配置
   → 必走本地判定 provider。
4. `evaluate` 支持 `progressCallback` / `abortSignal` / `maxConcurrency` / `cache:false`。
   `cache:false` 避免攻击提示与目标响应写入容器磁盘缓存。
5. `neverGenerateRemote()`（fail-closed 开关）只影响 redteam 插件生成与远程分级，
   不影响 evaluate 模式 → 安全开关保持不动。

### 错误码

| code | 产生位置 | 语义 |
|---|---|---|
| `generation_not_configured` | BFF fail-fast | 平台默认 MaClaw LLM 未配置，run 不创建 |
| `generation_failed` | 引擎 | 生成 LLM 调用/解析失败（重试 1 次后仍失败） |
| `engine_error` | 引擎 | 其余执行失败 |

### severity 语义

evaluate 模式没有 redteam grader 的 severity 字段 → 引擎 reduce 时按 pluginId
前缀推断：harmful/injection/jailbreak/pii → high；bias/其他 → medium。属于展示
层近似，在引擎代码中注明。

### 改动清单

- 引擎 `src/runner/generateTests.ts`（新增）：生成模块 + `EngineRunError`
- 引擎 `src/runner/redteamRun.ts`：改 evaluate 模式 + severity 推断
- 引擎 `src/manager/taskManager.ts`：错误码细分（EngineRunError.code）
- Go `internal/api/handler/engine_run.go`：删 debug 日志 + nil generation fail-fast
- Go `engine_run_test.go`：generation 注入专项断言 + fail-fast 测试

---

## 九、最终落地结果（2026-09-24）

方案 A 已上线并端到端验证通过。相对第八节设计的补充修正：

### 实施中修复的三个额外问题

1. **target transformResponse 路径错误**（Phase-0 遗留，从未被执行到）：
   `json.response.choices[0]…` → `json.choices[0].message.content`（transform 的
   `json` 即解析后的响应体，OpenAI 兼容响应的 choices 在顶层）。
2. **结果读取路径错误**（Phase-0 遗留）：`evalResult.store.results` →
   `evalResult.results`（library 模式下 `addResult` push 到 Eval 实例的
   `results` 数组，Eval 类没有 `store` 属性）。
3. **target base_url 端点规范化**：promptfoo http provider 把 `config.url`
   原样作为 POST 端点，而平台存的 target base_url（如 `…/v1/`）不含
   `/chat/completions`，会 404。引擎侧新增 `toChatCompletionsURL` 规范化。

### 生成 LLM 配置变更

平台默认 MaClaw 模型配置（`maclaw_model_defaults`，加密存储）由
DeepSeek（key 失效，官方 401）切换为：

- provider：`mimo`，`https://api.xiaomimimo.com`，`mimo-v2.6-flash`
- 生成与判定请求均禁用推理模式（`thinking:{type:'disabled'}` +
  `reasoning_effort:'none'`；判定经 promptfoo openai provider 的
  `passthrough` 注入），避免 reasoning_content 耗尽 completion 预算导致
  空 content 误判。
- 判定 provider `max_tokens: 4096`（防御性）。
- 更新已自动同步到 3 个已映射的 maclaw 租户。

### 端到端验证（2026-09-24）

- 企业账号 `num_tests=3` run（`er-33f882235af24d88a5e36991cfc77280`）：
  `succeeded`，20.2s，probes=3，attack_success=0，pass_rate=1.0（Qwen 目标
  安全拒答，判定 LLM 判 pass）。
- 引擎 51/51 vitest 通过（含真实 promptfoo 库的 evaluate 模式 smoke e2e）；
  Go 全量测试通过。
- 安全抽查：`engine_runs` 全表无密钥/Bearer/prompt 原文/目标响应原文/
  本地路径；result 仅含 totals/severity/plugin/strategy/risk/token_usage
  计数字段；引擎日志无凭据。
- 引擎 fail-closed 开关全程未动：`PROMPTFOO_DISABLE_REMOTE_GENERATION=1`、
  `PROMPTFOO_DISABLE_REDTEAM_REMOTE_GENERATION=1`、telemetry/share 禁用，
  未设置 `PROMPTFOO_REMOTE_GENERATION_URL`。

---

## 六、U6 · 远程依赖能力护栏（2026-10-11）

> 本文第一节第 3 条记录了那个静默失败：`return []`。U6 不改生成方案（仍是方案 A），
> **只把那层静默变成显式可见的护栏**，为 U7（方案 C）预铺。

### 6.1 问题：fail-closed 下「跑完了」不等于「覆盖到了」

第一节第 3 条的代码在 plan A 下不构成问题——**方案 A 根本不调用 promptfoo 的远程插件**
（`generateTests.ts` 自己合成攻击 prompt，promptfoo 只在 evaluate 模式消费）。
但它是一颗定时炸弹：**U7 一旦接入 promptfoo 原生生成（`runRedteam`），
`createRemotePlugin` 就会走到这条分支**，于是「评测跑完、报告出来、该插件覆盖 0」，
用户完全无从得知。

### 6.2 数字（promptfoo 0.123.0 运行时求值）

| 来源（上游常量） | 运行时长度 | 命中平台目录 |
|---|---|---|
| `REMOTE_ONLY_PLUGIN_IDS` | 93 | **65** |
| `UNALIGNED_PROVIDER_HARM_PLUGINS` | 22 | **21**（1 条不在平台目录内） |
| `BIAS_PLUGINS` | 4 | **4** |
| **合计去重** | | **90** |

（上表的目录命中数是**当时** 119 目录下的值；该表本身不随 P2-1 变化——被移出的三个 id 都不在这些远程清单里，所以 `marked` 仍为 90。）

→ P2-1 后：目录 **116 个插件**，其中 **26 个可纯本地生成**（清单见 `internal/maclaw/testdata/promptfoo_remote_dep.json` 的 `catalog_marked_plugins`，其补集被 `TestLocalOnlyPluginIDsAreTheDocumentedList` 硬钉住）。
原来写作「**90 / 119、仅 29 个可本地生成**」，那个 29 里含 3 个 promptfoo 根本不接受的 id（`prompt-injection` / `jailbreak` / `security-exploit`），已在 P2-1 移出目录，故可用面由 29 收敛为 26——**收敛掉的是从来跑不通的选项，不是能力**。id 合法性由 promptfoo 自己的 `RedteamConfigSchema.safeParse` 实测，见 `internal/maclaw/testdata/promptfoo_plugin_legality.json`。

策略侧被屏蔽 **6 个**（P2-2 由 4 个增至 6 个）：

| 平台 id | promptfoo 原生 id | 何时发现的 |
|---|---|---|
| `bestOfN` | `best-of-n` | U6 手写表 |
| `likert` | `jailbreak:likert` | U6 手写表 |
| `citation` | `citation` | U6 手写表 |
| `singleTurnComposite` | `jailbreak:composite` | U6 手写表 |
| `iterative:meta` | `jailbreak:meta` | **P2-2 实测补上** |
| `jailbreak` | `jailbreak` | **P2-2 实测补上**（废弃别名，委派给上面那个 provider） |

平台 id 与上游 id 不一致者（`likert` / `singleTurnComposite` / `iterative:meta`）的译名记录在闸门表里。

**这两条为什么会漏**：U6 的策略闸是一张**手写的 4 项表**，而 promptfoo 的远程要求在 provider 构造时才抛错，`gen_strategy_catalog.mjs` 的探针只调 `action()`，看不见（该文件自己为 `best-of-n` 记过这一点）。P2-2 改为**实测**：扫描 dist 里 `getRemoteGenerationExplicitlyDisabledError("<name>")` 的抛错点，加上 `REMOTE_DELEGATIONS` 处理「废弃别名用 provider 种类而非策略 id 委派」的那一类（`jailbreak` 的 action 调 `addIterativeJailbreaks(…, "iterative:meta", …)`）。遇到未登记的抛错标签或消失的委派字面量，生成器直接失败，不再静默漏闸。

### 6.3 三层护栏

| 层 | 落点 | 行为 |
|---|---|---|
| **目录标记** | `internal/maclaw/promptfoo_plugin_catalog.go` | 条目增 `RequiresRemoteGeneration`，经`p()`/`s()` 构造器**从映射表派生**（不逐条手写，无法漂移）；随 `EngineCatalog` 下发给引擎 |
| **前端可见** | `engineEval.ts` + `EngineEvalPage.tsx` | 被标记项**置灰但保留可见**，并**就地写出屏蔽原因**（不依赖悬停）；页面底部说明置灰数量与原因 |
| **运行前拒绝** | `promptfoo_remote_dep.go` + 引擎 `remoteDepGuard.ts` | 请求含被标记项 → **409** + `plugin_requires_remote_generation` / `strategy_requires_remote_generation` + 受阻 id + 可用替代提示 |

**为什么 BFF 与引擎各有一道闸**：引擎是可独立部署的服务，有自己的 REST 面。
绕过 BFF 直连引擎的请求（老 BFF、重放的抓包、未来新调用方）同样会产出静默 0 覆盖的run。
两道闸共享同一真相源——标记由 BFF 随 catalog 下发，**引擎侧不复制映射表**；
无 catalog 时引擎不拒绝（避免出现第二份会腐烂的清单），由 BFF 那道门兜底。

### 6.4 防漂移：映射表是生成物，不是手维护物

```
scripts/gen_remote_dep.mjs  ──运行时求值──>  internal/maclaw/testdata/promptfoo_remote_dep.json
                                                              │
                                                        go:embed│
                                                              ▼
                                    internal/maclaw/promptfoo_remote_dep.go
```

**升级 promptfoo 后必须重跑并review diff**：

```bash
cd promptfoo_engine && npm ci
cd .. && node scripts/gen_remote_dep.mjs
cd .. && git diff --exit-code internal/maclaw/testdata/promptfoo_remote_dep.json
```

**禁止按源码字面量或「形状」数清单**（详细原因与实测见 `audit-2026-10-09/14` §3.4.1）：

- `REMOTE_ONLY_PLUGIN_IDS` 含 **8 个 `...spread`**，字面量 26 vs 运行时 **93**（差 67）；
- 同chunk 内有 **171 键的 plugin→severity map** 与26 键 `HARM_PLUGINS`，形状启发式会误认
  （U6 第一版据此跑出 **116 marked / 3 local** 的错数）；
- chunk 文件名是**内容哈希**（本机 3 个变体），导出名是压缩后的（`zt`/`Ut`/`St`），**都不可硬编码**。

唯一可靠法：**解析 `dist/src/index.js` 里 `import { zt as REMOTE_ONLY_PLUGIN_IDS, ... } from "./tables-XXXX.js"`
这行**，得到「哪个 chunk 的哪个导出 = 哪个常量」，再 `import()` 取值。
生成器是 **fail-loud**：上游常量改名即 **exit 1**（勿改成静默兜底）。

`promptfoo_remote_dep_test.go` 的 7 个断言把这条链钉住，其中
`TestRemoteDepProvenanceMatchesInstalledPromptfoo` 把表里的 `promptfoo_version`
与 `package.json` / `package-lock.json` 三方对照——**升级不重生成就会红**。

### 6.5 边界与已知局限

- **本批零生成/判定逻辑变更**：`generateTests.ts` / `redteamRun.ts` / `env.ts` 均未改动，
  四个 fail-closed 开关未动。运行**前**行为有变更（原本静默跑 0 条 → 现在 409），这正是本批目的。
- **静态求值无法穷尽**：插件内部可能存在未登记的远程触发路径
  （例：`memoryPoisoning-*.js` 直接调 `getRemoteGenerationUrl()`，**不经 `neverGenerateRemote()` 守卫**）。
  本表是「已知的已知」，**最终以 U7 Step 0 真实环境实测为准**。
- **多轮策略不在屏蔽之列**：`crescendo` / `goat` / `hydra` / `iterative` /
  `iterative:tree` / `iterative:meta` 用 `shouldGenerateRemote()` **条件性**选择远程 provider，
  在方案 C 下可走本地 provider——**这正是 U7 的主要收益来源**。
