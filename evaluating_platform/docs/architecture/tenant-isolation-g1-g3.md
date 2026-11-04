# 租户隔离 G-1~G-3（P26 立项）设计稿

> 状态：**P26a/P26a.1 已落地并上机（47/48 号）；P26b 已落地（本地，待上机）；P26c/d 未做**（2026-11-04）。47 号另发现 backend compose 漏透传 `MACLAW_TENANT_ENGINES`（P26a.1 已修）。
> 已落地范围（默认关，`MACLAW_TENANT_ENGINES=0` 时逐字节等同 P25 行为）：迁移 029
> （映射表 `engine_base_url/token_ciphertext/token_key_id` + run 表 `engine_base_url`）、
> `EngineInstanceRegistry`、wizard 与 confirm 两扇门的按用户路由 + 代码包双条件拒绝、
> run 生命周期粘在起跑实例上（记录 sticky，消失即拒不清空）。
> 未做：P26b 生成 key 租户化、P26c token 生成/注入 + compose 白名单模板、P26d 上机判据。
> 前置决策（主理人 2026-11-02）：代码包是关键功能需求；验证环境（ai02）已获准开闸
> （47 号补跑），生产/共享部署开闸以本文为资格线。
> 承接：`strategy-code-packages.md` §2（Gate 定义）、§14（S-2 独立立项）；43–46 号记录。

---

## 0. 一页结论

| 问题 | 答案 |
|---|---|
| 要买什么 | 三个闸（G-1 每租户引擎 / G-2 生成 key 按租户 / G-3 实例级 token），买的是「专家代码包可以在**多方共用的部署**上跑」这件事的资格 |
| 现状凭什么不够 | 代码策略 `action` 在全局唯一引擎进程里执行；该进程持有**平台默认生成模型 key**、所有租户 run 的 workdir（含已物化靶机 key 的 `redteam.yaml`）、共享 `promptfoo.db`、全局 bearer token —— 任一专家包一旦作恶，可达面 = 整台部署 |
| 最大的意外便宜 | **G-2 几乎不用动引擎**：目标 key 早已是逐 run 的 `EngineOneShotCredentials` 注入（`promptfoo_engine_client.go:46-76`），generation 也在同一结构里；要改的只是 BFF 侧 resolver 的**输入**（从「平台默认」到「租户有效配置」） |
| 实例化怎么选 | **D-1 推荐静态 compose 声明**：只给「要用代码包的租户」起专属实例，其余租户留在共享实例上（共享实例强制拒绝代码包）。按需拉起要挂 docker socket——那个洞比要防的东西更大，v1 拒绝 |
| 做完能多收什么 | 附带收益：租户间并行，40 号记录 §3.1 的「长跑阻塞所有人」被 D-1 顺手解决（每实例仍是单 worker，但只串自己租户） |
| 本批交付 | 注册表 + 路由（P26a）→ 生成 key 租户化（P26b）→ 实例 token（P26c）→ 上机判据（P26d/48 号）。**47 号（开闸+代码包端到端）先行，不等本文** |

**一条不能含糊的话**：隔离的度量单位是「一个恶意代码策略**读得到什么**」。三闸全部到位前，
「共享部署」的定义就是「所有专家代码在同一个持钥进程里跑」；到位后才是
「专家代码只在本租户的引擎、只拿本租户的 key、只被本租户的 token 驱动」。

---

## 1. 目标与非目标

### 1.1 目标

1. G-1：执行专家代码的引擎实例与租户一一对应，文件系统、promptfoo 数据库、进程 env 互不可达；
2. G-2：一次 run 的**生成模型凭据**取自该租户的有效配置，平台默认只做兜底，且任何引擎实例的环境里不存在「别人的 key」；
3. G-3：一个实例的 bearer token 只能驱动它自己；BFF 侧凭据注册表化，token 由平台生成、加密存储；
4. 未注册专属实例的租户继续用共享实例跑**普通（非代码包）评测**——隔离不能变成全员付费的常驻成本。

### 1.2 非目标（本批不做）

| 不做 | 理由 |
|---|---|
| K8s / 动态编排 / 按需拉起缩容 | ai02 是单机 compose；按需起容器需要 dockerd 通道，其攻击面大于代码包本身（设计稿 §13 R-5 的「独立立项」评估过成本后仍未选它，见 D-1） |
| 租户内多 worker 并行 | 共享 promptfoo.db 的终态删除互踩问题（P2-0 已定论）在租户内依然存在；每实例单 worker 是**特性**不是欠账 |
| 靶机凭据、evidence、报告的边界变化 | 它们已经逐 run/逐 owner 处理，本文不动 `save_redteam_evidence`/报告清洗的任何规则 |
| 同步死循环防护 | `while(true)` 占死 worker 是进程内执行的固有代价（§15.3b 已声明）；G-1 把影响半径缩到本租户，这就是全部能做的 |
| 跨租户共享「已发布包」执行体 | 发布目录本来就只有安全元数据；执行永远在消费方租户自己的实例里 |

---

## 2. 现状（取证，2026-11-02）

| # | 项 | 事实 | 出处 |
|---|---|---|---|
| S-1 | 引擎实例 | compose 静态单服务 `promptfoo-engine`，named volume `promptfoo_engine_data:/data/runs` 全部署共享 | `docker-compose.yml`（engine 块 volumes / env `PROMPTFOO_ENGINE_DATA_ROOT=/data/runs`） |
| S-2 | 并发 | `ENGINE_WORKER_COUNT = 1`，run 构造上串行；跨租户排队 | AGENTS「Plan A 已删」条、engine `manager/` |
| S-3 | 鉴权 | 全局一对：BFF `cfg.Maclaw.EngineBaseURL`/`EngineBearerToken` ↔ 引擎 `PROMPTFOO_ENGINE_BEARER_TOKEN`（fail-closed ≥16 字符） | `pkg/config/config.go:84`、`cmd/server/main.go:165`、engine `src/env.ts:135` |
| S-4 | 生成 key | `ResolveEngineGenerationCredential` 调 `GetDefaultRuntimeConfig(ctx)`——**无账号入参**，全平台一把 | `internal/maclaw/engine_credential_materializer.go:85-115` |
| S-5 | 目标 key | **已经逐 run 注入**：`EngineOneShotCredentials{Target, Generation}` 随 `EngineRunRequest` 下发，引擎不落库不回显 | `internal/maclaw/promptfoo_engine_client.go:46-76,100-114` |
| S-6 | 租户模型配置 | per-account 通道存在但在 **MaClaw runtime 侧**：admin `PUT /maclaw/accounts/:userId/config` → 实例 `/api/v1/config`；平台表 `maclaw_model_defaults` 全平台仅一行 | `cmd/server/main.go:429-436`、`admin_maclaw.go:122-148`、`model/maclaw_model_config.go` |
| S-7 | 代码包闸 | `MACLAW_STRATEGY_CODE_PACKAGES`（默认 0）同时约束 BFF 发布与引擎执行；44/45/46 号实测关闭态 409 | compose `${MACLAW_STRATEGY_CODE_PACKAGES:-0}` |
| S-8 | workdir 生命周期 | `/data/runs/<run-id>/` 终态即删（含 cache/、redteam.yaml、custom-* ） | `promptfooRunArtifactPaths` 链路、AGENTS 缓存条 |

**结论**：G-2 的真实工作量 = S-4 换一个带账号的解析入口 + S-6 那条已有通道成为可读源（或平台侧镜像存储）；G-1/G-3 的工作量集中在**实例注册与路由**（S-1/S-3 的单值变多值）。

---

## 3. 决策点

### D-1 · 专属实例怎么存在（G-1 的形态）

| 方案 | 说明 | 判定 |
|---|---|---|
| A. 静态 compose 声明制 | 为「白名单租户」（要发代码包的）各写一个 `promptfoo-engine-<slug>` 服务块：独立 named volume、独立 bearer env、`PROVISIONING 时只起白名单`。加租户 = 改 compose + `up -d`（需运维动作，可接受，租户数是个位数） | **推荐** |
| B. 按需拉起 | BFF 经 docker socket 动态 create/start 容器 | **拒绝**：挂载 dockerd 等于给平台一个能操作全机容器的通道——比它要防的「专家代码」权限大得多；且冷启动预算、孤儿容器回收全是新面 |
| C. 同容器进程隔离 | 一容器内多 worker、按 run 起沙箱进程 | **拒绝**：容器加固管不到「同进程读取」（§7 原话），换线程/子进程不改 key 可达面 |

**验收**：两个租户各跑一轮，A 的实例里 `ls /data/runs` 只有 A 的目录；A 容器 env 里没有 B 的任何凭据。

### D-2 · 路由与注册表（三闸的公共地基）

- `maclaw_account_mappings` 增两列（migration，延续 028 的「加列不回填」风格）：
  `engine_base_url TEXT NOT NULL DEFAULT ''`、`engine_token_ciphertext BYTEA`（复用 crypto master key 基建，浏览器永不可见）。
- 新 `EngineClientResolver`（`internal/maclaw`）：按 **run 发起方**（企业账号，wizard 与 confirm 两条入口同一个 key——这是 P10/P11「两条门一个解析结果」教训第 N 次应用）解析 `(baseURL, token)` → 构造 `PromptfooEngineClient`；空列回退共享实例。
- BFF 侧所有直接 `engineClient.xxx` 的调用点改走 resolver（grep 清单在 P26a 任务里枚举，禁止遗漏式改造）。
- **共享实例的拒绝**：resolver 回退到共享实例时，请求含 `custom_strategy_code` 必须本地拒绝（409 `code_strategy_requires_dedicated_engine`，走 `writeEnginePreflightRejection`），根本不发给共享实例；引擎侧再设一道（`MACLAW_STRATEGY_CODE_PACKAGES` 在共享实例保持 0）——**开关 AND 注册表双条件**，单侧失守不开门。

### D-3 · 生成 key 按租户（G-2）

- 新增 `ResolveEngineGenerationCredentialForAccount(ctx, store, accountID)`：
  1. 读该租户的有效模型配置——**已定案 (b) 平台侧镜像（主理人 2026-11-04 授权按计划默认推进）**：
     (a) 经 S-6 现通道从 MaClaw 实例 `/api/v1/config` 读（单一事实源，不加新表；缺点是每次 run 多一跳、实例不可达时行为要定义）；
     (b) admin 保存 account config 时平台侧镜像一行加密（读快；缺点是与 runtime 出现双份事实，需要同步纪律——历史上 `maclaw_skill_shadows` 就是这类投影，有先例可循）；
     **倾向 (b)**：run 热路径不依赖 runtime 可达，且镜像行由保存动作单点写，双源窗口小。
  2. 无覆盖 → 平台默认（今天的行为，兜底语义与 P16 目标 kind 的「未知不猜」同构）。
- 引擎零改动（S-5：generation 已在逐 run 注入的结构里）。
- 判定依据：租户 A 的 `redteam.yaml` 里 generation key == A 的有效配置；改 B 的配置不动 A；未配置的租户回退默认且 run 记录里可诊断（不新增错误码，复用现有 generation 缺失路径）。

### D-4 · 实例级 token（G-3）

- BFF provisioning 白名单租户时生成随机 token（≥32 字符），密文进 D-2 列；compose 服务块经 `env_file` 注入（不进 git）。
- 引擎不改校验代码（它只比对自身 env 的那一把）；**改的是运维文档**：一台实例一把 token。
- 全局 `MACLAW_ENGINE_BEARER_TOKEN` 保留，语义收窄为「仅共享实例」；旧值不轮换（轮换是运维事件，写进 cutover checklist）。
- 判定依据：拿 A 实例 token 请求共享实例 `/runs` → 401/403（引擎现状即如此，要做的只是验一遍没开后门）；反向同。

### D-5 · 数据面

- named volume 逐实例：`promptfoo_engine_data_<slug>:/data/runs`；`PROMPTFOO_ENGINE_DATA_ROOT` 不变。
- workdir 终态删除语义（S-8）不动；`report_sanitize.go` 的 `/data/runs/` 清洗模式对带 slug 的宿主路径同样命中（宿主路径不出现在报告，引擎容器内路径是统一的 `/data/runs/...`，无需改正则——P26d 复核这条即可）。

---

## 4. 交付拆分

| 步 | 内容 | 守卫（每条先跑红） | 依赖 |
|---|---|---|---|
| P26a | D-2 注册表 + `EngineClientResolver` + 共享实例代码包本地拒绝 | **已落地并上机（47 号）**：路由单测（两租户解析出两 client，URL/token 断言）；含 `custom_strategy_code` 且无专属实例 → 409 且**未发出 HTTP**（反证：删拒绝分支 → 门侧测试变红，已跑）；wizard/confirm 两入口同一解析结果；生命周期按 run 记录的实例路由，消失即拒不清空 | 迁移 029（已落地） |
| P26b | D-3 生成 key 租户化（(b) 镜像方案） | **已落地（本地，待上机）**：`maclaw_model_overrides`（迁移 030，复用共享加密行助手）；service 五测（镜像优先/掩码回读本租户/坏行响亮失败/legacy 兼容/uuid.Nil）+ 两扇门各一条 e2e（wizard 真栈、bridge Prepare），反证（调用点回退旧解析→红）已跑 | P26a |
| P26c | D-4 token 生成/注入 + compose 白名单模板（`docker-compose.tenant-engines.yml` 样例）+ 文档 | 拿 A token 打共享实例 401（上机判据，本地 fake 断言 header 值不同即可） | P26a |
| P26d | 上机判据全跑（§5）+ **生产开闸条件写入 AGENTS/上线清单** | 见 §5 表 | P26a–c、47 号先完成 |

---

## 5. 验收判据（P26d / 48 号上机）

| # | 判据 | 方法 |
|---|---|---|
| 1 | 跨实例不可读 | A、B 各跑一轮；A 的代码策略里 `readdir /data/runs` 写入返回值观察——只有 A 的目录（设计稿 §11.2 的原案） |
| 2 | token 不通用 | A 实例 token 请求共享实例与 B 实例 → 均 401/403 |
| 3 | 生成 key 隔离 | A 的 run 请求体 generation.key ≠ B 的 ≠ 平台默认（库/日志均不落 key，取证走容器 env 与受控回显） |
| 4 | 确定性 | 同代码策略两次运行输出指纹一致（沿用 §11.2-3） |
| 5 | 停用即拒 | 治理停用 A 的包 → 下一次 run 409（无缓存窗口，沿用现有 resolver 语义） |
| 6 | 共享实例不开代码包 | 全局开关临时置 1 的对照实验：无专属实例的租户发代码包 → 409 `code_strategy_requires_dedicated_engine`，请求未出 BFF |
| 7 | 回归 | 43 号八判据 + 45/46 号全判据在专属实例上复跑绿 |

---

## 6. 成本与运维

| 项 | 量化 |
|---|---|
| 常驻成本 | 每专属实例 ≈ 一个 node 进程 + 各自卷；按现有引擎容器实测内存定（P26c 上机时记 `docker stats` 基线）；白名单制保证只给需要的租户花 |
| 运维动作 | 加/减白名单租户 = 改 `docker-compose.tenant-engines.yml` + `up -d`（engine 重启必须 `--force-recreate` 的既有规矩照用） |
| 部署自检 | 46 号 DNS 提醒（cutover checklist）对每个实例同样成立：新实例服务块也要带 `dns:` |
| 迁移 | 无数据迁移；共享实例继续服务非代码包租户，白名单租户的代码包 run 走专属实例、普通 run 也走专属实例（一致性优先，不搞混跑路由特例） |

## 7. 风险登记

| # | 风险 | 等级 | 处置 |
|---|---|---|---|
| R-1 | compose 静态声明与账号映射表脱节（表里有 URL，容器没起） | 中 | 启动期 BFF 对每个注册实例探活 `GET /health`，失败即 run 拒绝（不发不可达实例）；探活结果进 admin 健康摘要 |
| R-2 | (b) 镜像配置与 MaClaw runtime 实际配置漂移 | 中 | 保存动作单点双写（admin handler），读侧只用镜像；漂移检测 = account config test 端点顺带比对（已有 test 路由可复用） |
| R-3 | token 泄漏面从「一把」变「N 把」 | 低 | 每把只对自己实例有效，本身就是 G-3 的目的；存储走既有加密列 |
| R-4 | 白名单扩张使「共享实例只跑非代码包」名存实亡 | 低 | admin 资源治理页显示哪些租户有专属实例（本批顺手加列即可） |

## 8. 做完仍然成立的事（别夸大）

- 租户内仍单 worker 串行（含同步死循环占死自己的 worker——影响半径 = 本租户）；
- 报告/证据/错误文本的全部既有禁令不变；`error_brief` 仍只走 run DTO；
- 「代码包 = 执行专家代码的完整信任档」这句话不变，G-1~G-3 改变的是**信任的边界从整台部署收缩到一个租户**。
