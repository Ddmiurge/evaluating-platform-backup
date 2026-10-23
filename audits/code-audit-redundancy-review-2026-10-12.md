# evaluating_platform 代码审计：冗余代码 + 修改意见

**日期**：2026-10-12
**场景**：代码审计（冗余 / 死代码 + 代码质量 / 修改意见）
**参与成员**：排障手（gstack-investigator，冗余与死代码）、产品官（gstack-product-reviewer，代码质量与修改意见）；主理人（gstack-lead）负责范围界定与汇编

> 审计基线：仓库为干净 git 提交快照（`git status` 无未提交改动）。真实源码 = `git ls-files | grep -v node_modules` 共 **535 个被跟踪文件**（go 176 / ts·tsx 101 / py 27 / sql 30 / 配置·文档等）。`frontend/node_modules` 与 `promptfoo_engine/node_modules`（约 2.4GB，占整个 evaluating_platform 2.5GB 的绝大部分）已被 gitignore、未入库，属本地依赖安装产物，**已排除在源码审计之外**，仅作为「备份冗余」在结论中点名。
> **范围排除（用户明确指示）**：正在开发的会话在另一份实时副本上修改的代码属于 WIP，不在本次审计范围；本备份为已提交基线，以下结论仅针对该基线。

---

## 📌 TL;DR（执行摘要）

- 整体结论：**🟡 有条件通过** —— 代码分层清晰、SQL 全程参数化、加密实现质量高；但存在「静默数据损坏」类隐患与若干关键路径缺测试，需排期修复。
- 冗余量级：**很小**。535 个源文件中仅 1 组字节级重复文件 + 1 处明显复制粘贴样板（约 100 行）+ 22 个畸形引号目录待改名。
- 阻塞项数量：**0**（无发布级阻塞，但有 5 项 P1 需优先排期）。
- 最大风险：**repository 层 `json.Marshal/Unmarshal` 吞掉错误**（P1，数据完整性）+ **生产环境密钥默认值 fail-open**（P1，安全）。
- 下一步：优先修 P1（JSON 吞错 / 密钥断言 / 关键单测），再清理 skill 样板与畸形目录名，并规范备份流程排除 `node_modules`。

---

## 🎯 核心结论卡片

| 项目 | 内容 |
|------|------|
| Go / No-Go | 🟡 条件 Go（基线可维护，P1 项修复前不建议直接上线生产） |
| 严重度分布 | 🔴 5 / 🟠 8 / 🟡 15（另含 2 条 🟢 信息项） |
| 关键行动项 | 7 条（3 条 P1 + 4 条 P2） |
| 冗余可清理量 | 约 6 个文件可删/合并 + 约 100 行样板 + 22 个文件需改名（不含 node_modules） |
| 建议负责人 | 后端（Go） / 前端 / DevOps |

---

## 1. 各成员核心结论

### 🔧 排障手（冗余与死代码）
- 核心判断：**整体冗余量级很小**。经内容哈希比对（487 个文本文件）、Go 重复/未用扫描、Skill 样板比对、依赖 import 核对，确认无死代码、无大段注释代码、无未使用依赖（go.mod / package.json 均干净）。
- 关键发现：① 根 4 个 `*‑skill` 的 `runtime/selfcheck.py` 字节级相同（仅末行打印字符串不同）约 92 行纯样板；② `maclaw_skills` 下 3 份 `input.json` 内容哈希完全一致；③ 两个带前导引号的畸形目录 `"specs` / `"audit-2026-10-09`（共 22 个 md 文件）存在工具链兼容风险；④ `client_legacy.go` 虽名带 "legacy" 实为活跃拆分文件（已核实非死代码）。
- 估计：可安全删除/合并约 6 个文件、约 100 行样板；22 个文件为改名不删。

### 🔍 产品官（代码质量与修改意见）
- 核心判断：**整体健康度中等偏上，但依赖「作者自律」**。分层（handler→maclaw service→repository→pgx）清晰，SQL 参数化无注入，加密（AES-256-GCM 随机 nonce、短密文边界、RWMutex+key_id 轮换）质量高，gin binding 校验规范。短板是缺 Go linter 与关键路径单测，导致忽略错误、裸 `log` 等坏味道能入库。
- 关键建议：① 修 repository 层 JSON 吞错（数据完整性，P1）；② 生产模式密钥默认值启动断言（安全 fail-open，P1）；③ 补 crypto/repository/billing/auth 关键路径单测（P1）；④ 引入 `golangci-lint`、统一 `os.Getenv` 配置读取、拆分 4 个「上帝文件」（>1000 行）。

### 👤 主理人补充（备份冗余）
- 核心判断：本目录 `evaluating_platform/` 体积 2.5GB，其中 ~2.4GB 为 `node_modules`（已 gitignore 未入库）。作为「备份」携带可重装依赖是**明显的体积冗余**，既不必要也拖慢备份/恢复。
- 建议：备份脚本/流程应排除 `node_modules`（及 `.map`/构建产物）；或在恢复时执行 `npm ci` / `go mod download` 重建依赖，而非随仓携带。

---

## 2. 综合审查发现（去重合并，按严重度排序）

| # | 严重度 | 类别 | 位置 | 问题描述 | 建议 | 来源 |
|---|--------|------|------|----------|------|------|
| 1 | 🔴 | 错误处理 | `internal/repository/engine_run.go:40-46` | `marshalEngineRunBlobs` 用 `plugins, _ = json.Marshal(...)` 等 5 处吞掉序列化错误，失败时静默写入 `nil`/损坏 JSON。 | 让函数返回 `error`，调用处校验，失败返回 500。 | 产品官 |
| 2 | 🔴 | 错误处理 | `internal/repository/engine_run.go:293-296` | `scanEngineRun` 用 `_ = json.Unmarshal(...)` 忽略反序列化错误，DB 字段损坏时被静默零值化。 | `scanEngineRun` 返回 `(record, error)`；关键字段解析失败告警。 | 产品官 |
| 3 | 🔴 | 错误处理 | `internal/repository/maclaw_resource_record.go:194-196` | 同样 `_ = json.Unmarshal(...)` 忽略标签/元数据解析错误，损坏时静默零值。 | 同上，返回 error 并告警。 | 产品官 |
| 4 | 🔴 | 可维护性 | `internal/crypto/aes.go`、`keystore.go` | 加密核心（AES-256-GCM、密钥轮换）**无任何单元测试**，安全关键路径无回归保障。 | 增 `aes_test.go`/`keystore_test.go`：往返一致、错误长度密钥、短密文边界、轮换后旧 key 可解密。 | 产品官 |
| 5 | 🔴 | 可维护性 | `internal/repository/engine_run.go`、`maclaw_resource_record.go` | 含数据损坏缺陷的 DAO **无专属单测**（仅被 handler 间接覆盖）。 | 增 repository 层单测，覆盖 Create/Update/Get/List 与 scan 路径（含损坏 JSON 用例）。 | 产品官 |
| 6 | 🟠 | 错误处理 | `internal/repository/template.go:47` | `_ = json.Unmarshal(varsJSON, &t.Variables)`，模板变量损坏被静默丢弃。 | 改为返回 error 或记录日志并返回错误。 | 产品官 |
| 7 | 🟠 | 配置/安全 | `cmd/server/main.go:96-104` + `pkg/config/config.go` | 启动仅对**空** `crypto.master_key` 告警，未强制 `JWT_SECRET`/`CRYPTO_MASTER_KEY` 非默认值；compose 带默认明文，生产漏覆盖即 fail-open。 | 生产模式（`Mode=="release"`）断言二者非空/非默认值，否则 `log.Fatalf` 拒绝启动（呼应既有审计 S-02）。 | 产品官 |
| 8 | 🟠 | 可维护性 | `internal/billing/service.go`、`internal/api/handler/auth.go`、`billing.go`、`admin.go` | 计费与鉴权/计费/管理员入口 handler **无专属测试**，资金与身份关键路径。 | 补 `billing/service_test.go` 与 handler 级测试（登录、充值、余额、角色鉴权）。 | 产品官 |
| 9 | 🟠 | 前端健壮性 | `frontend/src/services/expert.ts`（及 `admin.ts`/`engineEval.ts`/`billing.ts` 等同模式） | service 方法 `api.x(...).then(r => r.data)` 返回未包裹 Promise 且无 `.catch`，任一漏写 `try/catch` 即 unhandled rejection。 | 提供带错误兜底的封装或 `Result<T>` 类型；组件侧补 `try/catch`。 | 产品官 |
| 10 | 🟠 | 复制粘贴样板 | 根 `autodan-stealth-skill/`、`cipherchat-encoding-skill/`、`gptfuzzer-mutator-skill/`、`promptinject-goal-hijack-skill/` 的 `runtime/selfcheck.py`（各 25 行） | 4 份 `selfcheck.py` **字节级相同，仅末行 print 字符串不同**（约 23 行×4 纯样板）；`maclaw_skills` 自校验入口不统一。 | 抽 1 个参数化公共 `selfcheck.py`（接收 skill 名），删 4 份根副本；统一 maclaw_skills 自校验入口。 | 排障手 |
| 11 | 🟠 | 清洁度/命名 | `"specs/`（6 md）、`"audit-2026-10-09/`（16 md） | 两个目录名**带前导引号**（备份/导出引号未闭合），22 个文件可能因字面路径导致工具链/脚本解析异常。 | 重命名为 `specs/`、`audit-2026-10-09/`（去引号），确认无脚本依赖该字面名。 | 排障手 |
| 12 | 🟠 | 备份冗余 | `evaluating_platform/frontend/node_modules`、`promptfoo_engine/node_modules`（~2.4GB） | 备份携带可重装依赖，体积冗余且拖慢备份/恢复；已 gitignore 未入库。 | 备份流程排除 `node_modules`/构建产物，恢复时 `npm ci`/`go mod download` 重建。 | 主理人 |
| 13 | 🟡 | 完全相同文件 | `maclaw_skills/{figstep,hades,mm-safetybench}-*/examples/input.json`（3 份） | 三份 `input.json` 内容哈希完全一致（md5 `b62dc04f…`），疑似共用占位样本被复制。 | 抽成 1 个共享样本或保留 1 份删 2 份；若各自有意加注释说明。 | 排障手 |
| 14 | 🟡 | 配置一致性 | `pkg/config/config.go` vs `frontend/.env.example` | `.env.example` 缺 `CRYPTO_MASTER_KEY` 但被 `config.yaml`/`docker-compose.yml` 引用；部分 example 变量未被 `BindEnv` 绑定。 | 补 `CRYPTO_MASTER_KEY` 占位并注明必填；核对 example 与 `BindEnv` 清单一一对应。 | 产品官 |
| 15 | 🟡 | 架构/配置 | `internal/api/handler/maclaw_runtime.go:493,506`、`redteam_bridge.go:1504`、`redteam_llm_judge.go:477` | 后端两套配置读取：viper `BindEnv` 与散落 `os.Getenv`（REDTEAM_TARGET_*/JUDGE_*/SKILL_MODEL_PREFLIGHT_*）。风格不统一、易漏绑。 | 统一收口到 `Config` 结构体 + `BindEnv`；`os.Getenv` 解析逻辑下沉到 config 初始化。 | 产品官 |
| 16 | 🟡 | 代码坏味道 | `redteam_bridge.go`(2521)、`maclaw_runtime.go`(1595)、`redteam_artifact_service.go`(1533)、`maclawsrv_client.go`(1003) | 4 个「上帝文件」，单文件过大、审查定位困难。 | 按职责拆分（target_call/judge/report/payload_store 等）；可排期，不急。 | 产品官 |
| 17 | 🟡 | 日志一致性 | `internal/maclaw/skill_projection.go:128` | 用标准库 `log.Printf("[WARN] ...")` 绕过项目自有 `pkg/logger` 结构化日志。 | 改用 `pkg/logger` 的 `logger.Warn(...)`，统一字段化日志。 | 产品官 |
| 18 | 🟡 | 健壮性/超时 | `internal/maclaw/promptfoo_engine_client.go:507` | SSE 流式 `http.Client{}` 无任何 `Timeout`，依赖 caller context，上游挂起且无 deadline 时永久阻塞。 | 设 `MaxIdleConns`/`IdleConnTimeout` 或 `context.WithTimeout` 包裹流读取。 | 产品官 |
| 19 | 🟡 | 安全/密码学 | `internal/api/handler/maclaw_runtime.go:68,405`（grant 机制） | 红队执行 grant 走 mint/verify 流程未发现直接 `==` 比较，但建议确认密钥比较均为常量时间且令牌有 TTL/防重放。 | 若存在直接字符串比较改用 `crypto/subtle.ConstantTimeCompare`；确认 grant TTL 与防重放。 | 产品官 |
| 20 | 🟡 | 可维护性 | 仓库根（缺失 `.golangci.yml`） | 无 Go linter 配置（前端有 eslint），忽略错误、裸 `log`、未用变量无法自动拦截——#1-4、#17 正是此类。 | 引入 `golangci-lint`（errcheck/govet/gocyclo/goconst），CI 强制，先 warning 准入再收紧。 | 产品官 |
| 21 | 🟡 | 部署/可移植 | `docker-compose.local-maclaw.yml:4,10` | `build.context: ../../maclaw` 引用本仓库外目录（maclaw runtime 未入库），本地构建依赖外部路径。 | 在部署文档明确该依赖与获取方式；或作为 submodule/独立构建步骤说明。 | 产品官 |
| 22 | 🟡 | 代码坏味道 | `redteam_bridge.go:53`、`target_config_service.go:29`、`maclaw_skill.go:177`、`promptfoo_engine_client.go:356` | 超时魔法数字散落（`180*time.Second`、`30`、`60`、`15`、`600`），缺统一常量。 | 抽取包级 `const`（如 `defaultTargetTimeout`、`defaultProbeTimeout`）。 | 产品官 |
| 23 | 🟡 | 健壮性 | `promptfoo_strategy_catalog.go:181,184`、`promptfoo_remote_dep.go:128,131` | 加载内嵌 JSON 目录时 `panic(...)`（init/load 阶段），数据随提交属构建期不变量，但裸 panic 不如返回错误可控。 | 改为构造函数返回 `error`，由调用方 `log.Fatalf`，保持 fail-fast 但避免裸 panic。 | 产品官 |
| 24 | 🟡 | 遗留标记 | `internal/api/middleware/billing.go:11` | `// TODO: 记录 API 调用，写入计费流水` —— 全仓唯一 TODO，属计划功能注释。 | 实现计费流水或移除 TODO 注释，避免长期遗留。 | 排障手 |
| 25 | 🟡 | 误导性命名 | `internal/maclaw/client_legacy.go`（216 行） | 文件名含 "legacy" 易误判为死代码；实际为 U3-b 拆分出的活跃文件，方法被引用。 | 重命名为 `client_skill.go` 等更准确名称，防止误删。 | 排障手 |
| 26 | 🟡 | 资源命名 | `CC-BOS/examples/claude-examlpe-2.jpg`（被 `CC-BOS/README.md:134` 引用） | 文件名拼写错误 `examlpe`（应为 example），但已被引用。 | 改名并同步 README 引用，或保留但统一命名规范。 | 排障手 |
| 27 | 🟡 | 文档资源 | `CC-BOS/examples/*.jpg`（~38 张 demo 截图）+ `images/framework.png`、`Framework_2.pdf` | 多为 README 展示截图，部分模型若已弃用可能未被引用。 | 核对 README 引用，删除未引用截图（当前无法判定为死资源，列为低优先核对）。 | 排障手 |
| 28 | 🟢 | 依赖核查 | `evaluating_platform/go.mod` | 9 个直接依赖（gin/jwt/uuid/pgx/minio/redis/viper/crypto/text）**全部被 import**；无声明未使用 module。 | 定期 `go mod tidy` 维持整洁（非本次可删项）。 | 排障手 |
| 29 | 🟢 | 依赖核查 | `evaluating_platform/frontend/package.json` | 运行时依赖仅 6 个、devDeps 为标准工具链，无重叠/弃用依赖。 | 维持现状。 | 排障手 |

---

## ✅ 行动清单（具体可执行项）

| # | 行动 | 负责方 | 紧急度 | 期望完成 |
|---|------|--------|--------|----------|
| 1 | 修 repository 层 `json.Marshal/Unmarshal` 吞错：`marshalEngineRunBlobs`/`scanEngineRun`/`scanMaclawResourceRecords`/`template.go` 返回 error，调用处处理并告警 | 后端 | P1 | 本阶段收口前 |
| 2 | 生产模式密钥默认值启动断言：`main.go` 在 `Mode=="release"` 时强制 `JWT_SECRET`/`CRYPTO_MASTER_KEY` 非空且非默认值，否则 `log.Fatalf`（fail-closed） | 后端/DevOps | P1 | 本阶段收口前 |
| 3 | 补关键路径单测：`crypto/aes_test.go`+`keystore_test.go`、`repository` DAO 单测（含损坏 JSON）、`billing/service_test.go` + 鉴权 handler 测试 | 后端 | P1 | 下个迭代 |
| 4 | 统一 Skill 自校验：抽取参数化 `selfcheck.py`，删除根 4 份副本（`autodan/cipherchat/gptfuzzer/promptinject`），统一 `maclaw_skills` 入口 | 攻击 Skill 维护 | P2 | 下个迭代 |
| 5 | 修复畸形引号目录名 `"specs`→`specs`、`"audit-2026-10-09`→`audit-2026-10-09`（22 文件改名），确认无脚本依赖字面名 | 全员/DevOps | P2 | 下个迭代 |
| 6 | 引入 `golangci-lint` 并统一配置读取：收口散落 `os.Getenv` 到 `Config`+`BindEnv`；抽取超时魔法数字为常量 | 后端 | P2 | 下个迭代 |
| 7 | 规范备份流程：排除 `node_modules`/构建产物，恢复时 `npm ci`/`go mod download` 重建依赖 | DevOps | P2 | 备份策略调整时 |

---

## ⚠️ 待完善 / 已知局限

- **实时开发 WIP 未覆盖**：用户指示「正在开发的会话」在实时副本修改的代码不在范围；本审计仅针对已提交基线快照。若需覆盖最新演进模块（如近期 commit 涉及的策略/策略包能力），需另做增量审计。
- **node_modules 未做内部审计**：依赖包内部冗余/漏洞超出本次源码审计范围，仅作为备份体积冗余点名。
- **前端 service 未逐文件验证**：unhandled rejection 风险基于同模式推断，未逐一核对每个 `services/*.ts` 调用点的 `try/catch` 覆盖。
- **CC-BOS 截图未逐一核对**：未引用截图（#27）未做 README 引用全量比对，列为低优先核对项。
- **Skill 逻辑重复性**：根 4 skill 与 `maclaw_skills` 攻击类型完全不同，已确认无逻辑重复，冗余仅限 `selfcheck.py` 样板。

---

## 📚 成员产出索引

- gstack-investigator（排障手）原始产出：冗余+死代码专项审计（范围 535 跟踪文件；方法含 md5 内容哈希、Go 重复/未用扫描、Skill 样板比对、依赖 import 核对）
- gstack-product-reviewer（产品官）原始产出：代码质量&修改意见审计（19 条发现，覆盖错误处理/配置安全/可维护性/架构/前端健壮性/日志/超时/密码学）
- gstack-lead（主理人）范围界定与汇编：备份体积冗余观察、团队调度、本报告整合

---

> 本报告由软件工坊 AI 协作生成，关键决策请由工程负责人复核。本次仅做审计，未对任何文件进行修改。
