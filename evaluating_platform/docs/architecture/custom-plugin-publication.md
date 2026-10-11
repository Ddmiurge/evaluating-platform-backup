# 专家自定义插件发布链路（U10）

> 状态：已实现（首期仅 YAML 形态）
> 承接：`14-Phase2实施计划.md` §2 U10；`06` D-03（取消「管理员审核」作为发布前置门 → 管理员**事后**治理）；`03` R-3（沙箱边界）

---

## 1. 这条链路解决什么

专家在**专家门户直接发布**插件/策略，**管理员事后治理**（启停 / 下架 / 归档 / 查看使用情况）。首期**仅 YAML 形态**。

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

- 管理员在 `资源治理` 页启停 / 归档（`PATCH /admin/maclaw/resources/:id`）；
- `CustomPluginResolver` 在**每次 run 准备时**重查发布表（`GetPublishedByHandle` 过滤 `enabled = true AND status = 'published'`），**不缓存**；
- 因此停用 → 下一次 run 引用该插件即被拒，**无重启窗口、无同步延迟**；
- 被停用的包在引擎侧**从不解密**（fail-closed 顺序：先治理后解密，有测试钉住）。

---

## 6. 已知边界（如实登记）

1. **沙箱两项未实测**：本机无 Docker，`verify_u10_sandbox.sh` 返回 2（SKIP）。只读根与无外网需要在 CI 或具备 Docker 的环境跑一次。
2. **自定义插件仅 Plan C 可执行**：Plan A 让平台 LLM 合成攻击提示，从不调用 promptfoo 的插件机制，包的 `generator` 会被静默丢弃。因此 Plan A 对携带包的请求**直接拒绝**（`custom_plugin_requires_native_generation`），而不是跑出一份绿色报告。
3. **策略（strategy）形态未做**：首期只做插件。策略的自定义需要另一套 promptfoo 加载契约。
4. **`file://` 等可执行引用的文本检查是纵深防御**：在 promptfoo 0.123.0 上，插件定义的**字段值**本身不经文件加载器，所以这不是已证实的利用路径。它存在是因为上游一旦改变，这个 payload 就变成任意文件读取原语，而这是唯一在存储前看到原文的地方。
5. **归档的插件不自动从企业侧影子副本清除**：`maclaw_resource_shadows` 的清理沿用既有资源治理语义，未在 U10 扩展。

---

## 7. 相关文件

**BFF**
- `internal/maclaw/promptfoo_custom_plugin.go` — 形态 / 文本 / schema / id / 远程依赖五道校验 + 规范化序列化
- `internal/maclaw/promptfoo_custom_plugin_resolver.go` — `pf_plugin:` 引用解析 + 治理生效点
- `internal/maclaw/resource_store_service.go` — 存储出口接线（护栏落点）
- `internal/maclaw/promptfoo_engine_bridge.go` — `PreparePromptfooEngineRun` 接线 + 引擎/记录双 id 列表
- `internal/api/handler/maclaw_errors.go` — 拒绝码 → 400 / 409（不是 500）
- `cmd/server/main.go` — resolver 装配

**引擎**
- `promptfoo_engine/src/runner/customPlugin.ts` — 物化 / 文件名 / 标记替换 / 报告 id 反查
- `promptfoo_engine/src/sandbox.ts` — 三项性质的运行时校验与报告
- `promptfoo_engine/src/env.ts` — 启动期 fail-closed（**只加不减**，四个开关未动）
- `promptfoo_engine/Dockerfile` / `docker-compose.yml` — 沙箱声明

**前端**
- `frontend/src/pages/expert/PromptfooPluginPublisher.tsx` + `promptfooPluginRejectionHints.ts`
- `frontend/src/pages/admin/ResourceGovernance.tsx` — 插件治理视图

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