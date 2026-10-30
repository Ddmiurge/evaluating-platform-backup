# 专家自定义插件 / 策略发布链路（U10 → P10/P11）

> 状态：**插件与策略均已实现并上机验证**（YAML 形态；策略自 P11 起两条入口都能执行）
> 最近验证：`records/34-…-20261023.md`（发布端）、`records/35-…-20261023.md`（执行链路）
> 承接：`14-Phase2实施计划.md` §2 U10；`06` D-03（取消「管理员审核」作为发布前置门 → 管理员**事后**治理）；`03` R-3（沙箱边界）

---

## 1. 这条链路解决什么

专家在**专家门户直接发布**插件/策略，**管理员事后治理**（启停 / 归档 / 查看使用情况）。**仅 YAML 形态**。

两种包共用一条链路（同一个加密存储出口、同一套校验与治理），区别只在 `resource_kind`：

| kind | 专家页 | 包内容 | 执行侧 |
|---|---|---|---|
| `pf_plugin` | 评测插件 | `generator` + `grader` | `resolveCustomPlugins` → 写入 per-run workdir |
| `pf_strategy` | 评测策略 | `strategyText` + `maxTurns` 等 | `resolveCustomStrategies` → 内联为 `custom:<variant>` |

与既有资源（样本 / 模板 / 组合攻击）的关系：**不另建一套存储与分发**。插件包复用 `maclaw_resources`（加密 payload）+ `maclaw_resource_publications` 分发，只新增一个 `resource_kind='pf_plugin'`。

---

## 2. 发布 → 加载 → 治理 三段

```
专家门户                    BFF                                    引擎容器
┌──────────────┐   POST /maclaw/evaluation/resources   ┌──────────────────────┐
│ 粘贴 YAML    │ ─────────────────────────────────────► │ SaveResource         │
│ 选依赖插件   │   kind=pf_plugin                       │  ├ 形态校验（仅 YAML）│
└──────────────┘                                        │  ├ 文本校验（无 file://│
                                                        │  │   / 无外部地址）     │
                                                        │  ├ schema（strictObject）│
                                                        │  ├ id 不与内置目录冲突│
                                                        │  └ U6 远程依赖护栏    │
                                                        │  ↓                   │
                                                        │ 规范化重序列化后加密存储│
                                                        └──────────┬───────────┘
                                                                     │
                                          maclaw_resource_publications│（enabled+published）
                                                                     ▼
                                              CustomPluginResolver（每次 run 重查，不缓存）
                                                                     │
                                          EngineRunRequest.custom_plugins│（正文内联下发）
                                                                     ▼
                                                        ┌────────────────────────┐
                                                        │ 写入 per-run workdir    │
                                                        │ <workdir>/custom-plugins│
                                                        │   /<handle>-<hash>.yaml│
                                                        │ 配置里换成 file:// 引用 │
                                                        │ promptfoo CustomPlugin │
                                                        └────────────────────────┘
```

### 2.1 关键设计选择

| 选择 | 理由 |
|---|---|
| 复用 `maclaw_resources` 加密存储 | 设计文档 §2 U10 ① 明确要求；另建存储会与既有 publication/shadow 投影分叉 |
| 护栏落在 **存储出口**（`SaveResource`）而非 HTTP handler | handler 层的护栏会被 shadow-copy 路径绕过（该路径无 handler 直接重存）。U8 的教训是「接线位置决定测试能不能抓住退化」 |
| 正文**内联下发**而非引擎回查数据库 | 引擎无数据库访问面；治理裁决必须在能看见发布表的 BFF 侧完成 |
| 写入 **per-run workdir** | workdir 在 run 终态被 `rm -rf`；插件包不跨 run 存活，停用的包不留残留 |
| 记录用 id 与引擎用 id **分开** | 引擎侧的 `promptfoo-custom:` 标记与 `file://` 路径都不得进入 run 记录 / 报告（AGENTS.md 禁止本地路径进报告） |

---

## 3. 沙箱边界（设计文档称「最重安全项」）

三项性质，**声明与执行分属不同层**，谁也替代不了谁：

| 性质 | 声明位置 | 执行/校验位置 | 本机实测 |
|---|---|---|---|
| **非 root** | `promptfoo_engine/Dockerfile` 的 `USER node` | `src/env.ts: assertSandboxInvariants` **启动即拒绝** uid=0 | ✅ 已实测（非 root 分支 + 单测） |
| **只读根文件系统** | `docker-compose.yml` 的 `read_only: true` + `PROMPTFOO_ENGINE_READONLY_ROOT=1` | `src/sandbox.ts` 的 `checkReadonlyRoot`；`PROMPTFOO_ENGINE_SANDBOX_PROBE=1` 时**真实尝试写 `/etc`** | ⚠️ **未实测**（本机无 Docker） |
| **无外网** | 运行时网络策略 | `nativeRedteam.ts: assertSafeProviderUrl`（只允许平台下发的端点）+ `sandbox.ts: checkNetworkEgress` 探测可达 `api.promptfoo.app` | ⚠️ **未实测**（本机无 Docker） |

### 3.1 为什么「无外网」不是字面意义的断网

引擎**必须**访问平台下发的模型端点（target / generation）。所以性质不是「没有网络」，而是**没有不受控的出口**：

- 唯一可达的外部目的地是平台选定的模型端点；厂商默认端点（`api.openai.com` 等）在 `assertSafeProviderUrl` 被显式拒绝；
- promptfoo 自己的远程生成服务是本部署 fail-closed 的通道（`PROMPTFOO_DISABLE_REMOTE_GENERATION=1`）。能连上 `api.promptfoo.app` 就说明这个开关只在库层面生效、网络层面没阻断 —— 这正是 U6 失败在**传输层**的形态，`checkNetworkEgress` 探测的就是它。

### 3.2 反证测试怎么跑

```bash
cd evaluating_platform
./scripts/verify_u10_sandbox.sh          # 退出码 0=PASS / 1=FAIL / 2=SKIP(未实测)
```

**退出码 2（SKIP）不是 PASS**，这是刻意的：本批开发机无 Docker，若把「没跑」记成「通过」，CI 快照就会把一个未验证的安全声明固化成绿灯。脚本在无 Docker 时打印 `[SKIP]` 并返回 2。

---

## 4. 首期仅 YAML：JS/脚本为什么必须拒绝

`file://x.js` 在 promptfoo 里**不是插件定义，是一次 import**：`maybeLoadFromExternalFile` 会原样保留 `file://….js[:fn]` 引用，provider/assertion 的加载器（`loadCallbackFromFileUrl`）随后 `import()` 它 —— 引擎进程内**任意代码执行**，而该进程持有已物化的生成模型 API key。

拒绝发生在**四个位置**，不是一个下拉框：

| 位置 | 拦什么 |
|---|---|
| BFF 存储出口 `SaveResource` | 声明形态非 yaml / 内容嗅探出 shebang、ESM、CommonJS / `file://`·`javascript:`·`python:` 引用 / 外部地址 |
| BFF 解析层 | schema 不符（缺 `generator`/`grader`、未声明字段、顶层非映射） |
| BFF 依赖层 | 声明依赖远程生成插件 → 复用 U6 护栏拒绝 |
| 引擎 `customPlugin.ts` | 直接打到引擎的请求（引擎是独立可部署服务，有自己的 REST 面） |

前端**不提供形态选择器** —— 提供一个点了会被拒的选项，会让专家以为自己弄错了什么。但前端不能是唯一防线，所以后端独立校验（`internal/maclaw/promptfoo_custom_plugin.go`）。

---

## 5. 管理员治理：即时生效

设计文档取消「管理员审核」作为发布前置门，治理全部发生在**发布之后**。

- 管理员在 `资源治理` 页启停 / 归档（`PATCH /admin/maclaw/resources/:id`），范围含 `pf_plugin` 与 `pf_strategy`；
- 治理同时更新**发布记录**与**专家自己的资源行**（2026-10-26 修复：只改发布记录时，
  专家列表仍显示 `published`，同一个事实两个视图互相矛盾）；
- 专家可在门户「下架」自己的包（`POST /maclaw/evaluation/resources/:id/archive`，仅本人）；
  下架 = 归档，**不是删除**；
- `CustomPluginResolver` 在**每次 run 准备时**重查发布表（`GetPublishedByHandle` 过滤 `enabled = true AND status = 'published'`），**不缓存**；
- 因此停用 → 下一次 run 引用该插件即被拒，**无重启窗口、无同步延迟**；
- 被停用的包在引擎侧**从不解密**（fail-closed 顺序：先治理后解密，有测试钉住）。

---

## 6. 已知边界（如实登记）

1. **沙箱两项未实测**：本机无 Docker，`verify_u10_sandbox.sh` 返回 2（SKIP）。只读根与无外网需要在 CI 或具备 Docker 的环境跑一次。
2. **自定义插件仅 Plan C 可执行**：Plan A 让平台 LLM 合成攻击提示，从不调用 promptfoo 的插件机制，包的 `generator` 会被静默丢弃。因此 Plan A 对携带包的请求**直接拒绝**（`custom_plugin_requires_native_generation`），而不是跑出一份绿色报告。
3. **没有物理删除**（2026-10-26 决定）：专家可「下架」、管理员可停用/归档，正文与 handle 保留。
   历史 run 与计划卡按 handle 引用它，删除会让引用指向空、旧报告无法解释。归档的效果与删除在
   **使用面**上等价（企业不可见、解析器拒绝），差别只在可追溯性。
3b. **代码策略（`form=js`）暂未开放**：promptfoo 支持 `file://` 策略模块（`import()` 执行），
   但策略 `action` 在引擎主进程内被调用，而引擎当前是**全局唯一实例**且持有平台级生成模型 key。
   开放前置条件是每租户引擎 + 租户级 key + 实例 token（与 Skill 同档），
   设计见 `strategy-code-packages.md`。在此之前 **`file://` 等拦截继续保留**。
4. **重新发布 = 更新同一个包**（P10）：请求带已有 `id` + `handle`；未带 handle 时沿用原 handle。
   每次发布都新生成 id 的旧行为会产生同名第二个包，已废弃。
5. **两条入口同一解析器**（P11）：向导与计划卡都经 `RedteamToolBridge.ResolveEngineCapabilities`；
   引擎侧先内联包再校验策略词汇表，且自定义策略保留 `config`（内置策略仍丢弃，U9 钉着不透传）。
6. **`file://` 等可执行引用的文本检查是纵深防御**：在 promptfoo 0.123.0 上，插件定义的**字段值**本身不经文件加载器，所以这不是已证实的利用路径。它存在是因为上游一旦改变，这个 payload 就变成任意文件读取原语，而这是唯一在存储前看到原文的地方。
7. **归档的包不自动从企业侧影子副本清除**：`maclaw_resource_shadows` 的清理沿用既有资源治理语义，未在 U10 扩展。

---

## 7. 相关文件

**BFF**
- `internal/maclaw/promptfoo_custom_plugin.go` — 插件形态 / 文本 / schema / id / 远程依赖五道校验 + 规范化序列化
- `internal/maclaw/promptfoo_custom_strategy.go` — 策略包的同一套校验（`custom:<variant>`、`strategyText` 必填）
- `internal/maclaw/promptfoo_custom_plugin_resolver.go` / `promptfoo_custom_strategy_resolver.go` — 引用解析 + 治理生效点
- `internal/maclaw/resource_store_service.go` — 存储出口接线（护栏落点；重发布归属校验在此）
- `internal/maclaw/promptfoo_engine_bridge.go` — `ResolveEngineCapabilities`（两条入口共用）+ `PreparePromptfooEngineRun` 接线
- `internal/api/handler/engine_run.go` — 向导路径调用同一个解析器
- `internal/api/handler/maclaw_errors.go` — 拒绝码 → 400 / 403 / 409（不是 500/503）
- `cmd/server/main.go` — resolver 装配

**引擎**
- `promptfoo_engine/src/runner/customPlugin.ts` — 物化 / 文件名 / 标记替换 / 报告 id 反查
- `promptfoo_engine/src/sandbox.ts` — 三项性质的运行时校验与报告
- `promptfoo_engine/src/env.ts` — 启动期 fail-closed（**只加不减**，四个开关未动）
- `promptfoo_engine/Dockerfile` / `docker-compose.yml` — 沙箱声明

**前端**
- `frontend/src/pages/expert/PromptfooPluginPublisher.tsx` + `promptfooPluginRejectionHints.ts`
- `frontend/src/pages/expert/PromptfooStrategyPublisher.tsx` + `promptfooStrategyRejectionHints.ts`
- `frontend/src/pages/admin/ResourceGovernance.tsx` — 插件 / 策略治理视图
- `frontend/src/pages/enterprise/EngineEvalPage.tsx` — 向导下拉的「专家发布」分组

**脚本**
- `scripts/verify_u10_sandbox.sh` — 沙箱反证（0/1/2 三态退出码）

---

## 8. 验收数据（口径 B：`-coverpkg` 全量）

| 项 | 基线（U8 收尾） | U10 收尾 |
|---|---|---|
| 语句块 / 覆盖块 | 4407 / 3140 | 4563 / 3267 |
| 覆盖率 | 71.25% | **71.60%（+0.35pp，只升不降）** |
| `verify_u3_coverage.sh` 退出码 | 0 | **0** |

测试规模：引擎 17 files / 226 tests（基线 14 / 188）；前端 8 files / 39 tests（基线 7 / 33）；Go 全包全绿。
### 6.8 策略包的两种形态（2026-10-28）

| 形态 | `pf_strategy_form` | 内容 | promptfoo 怎么加载 | 报告里的策略 id |
|---|---|---|---|---|
| 文本 | `yaml` | `strategyText` + 可选 maxTurns 等 | 内联成 `custom:<variant>` + config | `custom:<variant>` |
| 代码 | `js` | ESM 模块 `{ id, action }` | 写入 `<workdir>/custom-strategies/<variant>.js`，以 `file://` 引用被 `import()` | `custom:code:<variant>`（引擎归约前由路径换回） |

代码形态必须有显式 `pf_strategy_variant`：promptfoo 记录的是**文件路径**，平台只能靠这个标识区分两个代码策略。
执行前置条件（每租户引擎 / 租户级 key / 实例 token）与完整边界见 `strategy-code-packages.md`。

### 6.9 专家能力目录浏览（P23，2026-10-30）

专家在两个发布页里能看到「当前所有的插件和策略」：promptfoo 内置目录（116 插件按原生
category 分组 / 27 策略平组）+ 全体专家**已发布**的包。策略口径：后端可选集为 30 条，
其中「不可用且非远程闸」3 条不镜像进 `ENGINE_STRATEGY_OPTIONS`（这正是
`TestFrontendOptionsConsistency` 的跳过规则），目录呈现 27 条（44 号记录 §6 的出入即此，
不是缺陷）。形态是页面主体即分组目录，
本账号发布的行特殊标注「本账号发布」并保留重新发布/下架操作，其余行只读。

三条必须记住的边界：

| 边界 | 落点 |
|---|---|
| 合并只对 `pf_plugin`/`pf_strategy` 两个 kind 生效 | `maclaw_evaluation.go` `ListResources` 服务端硬编码白名单——专家传 `kind=sample&include_published_catalog=1` 也拿不到别人的样本（有测试钉死） |
| 发布者身份不可见 | 专家目录消费方走 `CatalogCaller{HideOwnerMetadata: true}`，`source_expert_user_id` 被删除；企业侧行为逐字节不变 |
| 归属判定在服务端 | 去重按 `SourceResourceID == own.ID`（自己的已发布包只出现一次，且是自己的全状态行）；`metadata.is_own` 由 `ListEnterpriseCatalog` 打标，前端不猜 id |

内置目录没有新后端：前端 import 的是 `engineEval.ts` 的同源镜像（一致性由
`TestFrontendOptionsConsistency` 钉死）。顺带修复一个活缺陷：`listPublishedPackages`
此前把响应直接 cast，而后端从不下发顶层 `form`（值在 `metadata.pf_strategy_form`），
企业向导的「· 代码策略」标签从未真实出现过；现在由 service 显式映射，
镜像守卫 `TestFrontendExpertCatalogKeepsTheCatalogContractLiterals` 防止键名单侧改名。

守卫：`TestResourceProjectionExpertCatalogMarksOwnAndHidesOwner`（service 去重/标注/隐去
owner）、`TestMaclawEvaluationHandlerExpertPublishedCatalog*` 四用例（合并、样本不泄漏、
不传参回归、企业不变）。
