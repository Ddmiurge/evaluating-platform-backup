# P28/E-4 合并前代码审查：embeddedbuild 构建工具与烘焙目录（2026-11-10）

> 范围：`git diff HEAD~1`（`f03353d` · P28/E-4 烤制目录读取端 + 向导内嵌分组 + 暂存/index 纯核心）
> **加** 未跟踪的 `evaluating_platform/cmd/embeddedbuild/`（E-4 部署机构建工具，尚未提交——发现 1–4 所在文件随其首次提交一并落地）。
> 基线：审查时 `origin/main == HEAD == f03353d`，工作区无已跟踪改动。
> 静态门在审查时已过：`go build ./...` 干净、`go test ./internal/maclaw/ -run 'Embedded|Stage'` 通过、`npm run build` 通过——**以下 6 条全部是逻辑级缺陷，不是构建级**。
> 性质：**合并前缺陷清单**。修复后逐条按各自的「反证」验收；本文件是 E-4 收口的验收判据来源。
> **状态（2026-11-10）：6 条已修**——批次生命周期收敛为 `cmd/embeddedbuild/batch.go`（§8 落地），发现 1 并集强化含 `disabled`（见 §1 修法补注），本地可测反证全绿（`batch_test.go` 11 条 + `embedded_build_test.go`/`embedded_plugin_test.go` 发现 5 用例 + flag 冒烟）；**DB/文件级反证归 E-6 上机验收**，验收时仍以本文件各条「反证」段为准。
> 设计依据：`evaluating_platform/docs/architecture/embedded-plugin-track.md`（§4 状态机六条硬规则、§5 时序图 D1/D3、§6 D-6「守卫不绿不部署」/ D-10「标记制构建」）。

---

## 0. 一览

| # | 严重度 | 位置 | 一句话 |
|---|---|---|---|
| 1 | **HIGH** | `cmd/embeddedbuild/main.go:96`（runStage） | 暂存只收 `build_requested` 且先清树，**把已烘焙的 testing/approved 包从目录里洗掉**，且状态机无回边可恢复 |
| 2 | MEDIUM | `cmd/embeddedbuild/main.go:159`（runFinalize） | 定格时**重新查队列**而非消费本批暂存清单，在途时新入队的包被标成「测试中」但从未部署，永久卡死 |
| 3 | MEDIUM | `cmd/embeddedbuild/main.go:163` | `NO_QUEUE` 早退在移除处理**之前**——仅标记移除、无其他包排队时，行永远停在 `remove_requested`，**被移除的代码留在镜像里** |
| 4 | MEDIUM | `cmd/embeddedbuild/main.go:21`（文件头文档） | 文档写的失败命令 `-finalize -fail` **标志不存在**，按文档操作在失败路径上崩溃；裸 `-finalize` 静默等于失败语义 |
| 5 | MEDIUM | `internal/maclaw/embedded_build.go:60` | 烘焙目录的 `promptfoo_version` 解析后**读路径从不校验**——基座升级后旧包照常可选，跑到引擎才中途失败（违反「判定放请求边界」纪律） |
| 6 | LOW | `internal/api/handler/engine_run.go:161` | `SetEngineInstances` 的文档注释错位到 `SetEmbeddedCatalog` 头上 |

---

## 1. HIGH · 暂存只收 `build_requested`，会把已烘焙包洗出目录

**位置**：`cmd/embeddedbuild/main.go:96`（`runStage`）——`ListAll(build_requested)` → `os.RemoveAll` 暂存树（:125）→ 仅用本批重写 `internal/maclaw/testdata/embedded_plugins.json`（:136-147）。

**失败场景**：

1. 批次 1 烘焙插件 A（finalize → `testing`，镜像与烘焙目录都含 A）；
2. 之后插件 B 被点了「请求构建」（`build_requested`）；
3. 下一次 `-stage`：清树 → 只暂存 B → 目录文件只剩 B；
4. 重建镜像后 **A 从向导「内嵌」分组和目录端点静默消失**，但 A 的数据库行仍是 `testing`/`approved`。

**为什么不可自恢复**：状态机没有从 `testing`/`approved` 回 `build_requested` 的边（`RequestBuild` 只接受 `approved_for_build`/`build_failed`，见 `embedded_plugin_lifecycle.go:11-19`）。唯一恢复途径 = 专家重新上传走全套审批——与「重传是例外、不是常规」的设计相悖。

**与设计矛盾**：D3/治理语义「移除 → 下次构建剔除 → 目录消失」的**前提**是「未标记移除的包在后续构建中存活」；本实现把「本批」当成了「全部」。

**修法**：暂存与目录生成都基于**并集** = `build_requested ∪ testing ∪ approved`（减去 `remove_requested`/`removed`）。移除项自然从并集排除——「removals ride the same batch」的注释（:21 附近）在并集语义下才成立。

> **修法补注（2026-11-10 实施时强化）**：并集还必须含 **`disabled`**。状态机里 `disabled` 只有出边 `→approved`（Enable）与 `→remove_requested`，`RequestBuild` 只收 `approved_for_build|build_failed`——若一次无关重建把 disabled 包洗出镜像，Enable 后是 approved 但不在目录、**没有任何按钮能重新烘焙**，且违反 D-5「停用只拦选择、镜像里代码不删」。`pending_initial_review`/`rejected`/`approved_for_build`/`build_failed` 排除均安全（有前向边、目录端点本就隐藏，`rejected` 行还可能 `resource_id=""` 不可解密）。落地：`planStage` 的 `stageableStates` 四态并集，`batch_test.go:TestPlanStageUnionIncludesBakedStates` 为可执行规格。

**反证（修复验收）**：烘焙 A 后仅让 B 入队 → `-stage` 后暂存树与 `embedded_plugins.json` **仍含 A、含 B**；再把 A 标记移除 → `-stage` 后 A 消失、B 仍在。

## 2. MEDIUM · finalize 重新查队列，而非定格本批暂存清单

**位置**：`cmd/embeddedbuild/main.go:159`（`runFinalize`）——再次 `ListAll(build_requested)` 决定谁进 `testing`。其实 `index.json` 已携带本批 id 列表与批次号（暂存时就写了）。

**失败场景**：一个 stage→deploy→finalize 批次在途时，管理员对包 C 点了「请求构建」→ finalize 把 **C 标成 `testing`（「已烤进镜像、已在线」）**，但 C 从未被暂存或部署：

- C 不在烘焙目录 → 选择闸按 `testing` 放行白名单账号，**选了却不在目录里**，行为不可解释；
- `testing` 无回边 → 永久卡死，只能专家重传。

这违反状态机注释自己写的语义：「`testing` means **baked AND live** — never "baked but not deployed"」（:157 注释）。

**修法**：finalize 只消费**本批 `index.json` 的 id 列表**（批次号一并落事件），队列里后来的 `build_requested` 行原样留下给下一批。

**反证**：stage 后、finalize 前对新包 D 执行 `RequestBuild` → `-finalize -ok` 后 **D 仍为 `build_requested`**，仅 index 内的行进 `testing`。

## 3. MEDIUM · 空队列早退跳过移除处理

**位置**：`cmd/embeddedbuild/main.go:163`——`len(rows)==0 → fmt.Println("NO_QUEUE"); return nil`，位于 remove_requested 处理块（:184）**之前**。

**失败场景**：管理员把一个 `approved` 包标记移除（设计语义：「移除 = 下次构建剔除」），此时没有其他包排队：

1. `-stage` → `NO_QUEUE`，不动暂存树/镜像；
2. `-finalize -ok` → 同样 `NO_QUEUE`，**在移除循环之前返回**；
3. 行永远停在 `remove_requested`（审计断链），**被移除的插件代码继续留在已发布镜像里**——直到恰好有无关包进队列才顺带被处理。

与 D3 时序图「②彻底移除 → 下次构建：从镜像移除 + 实测表回退」直接冲突：移除被错误地绑在「有构建需求」这个偶然事件上。

**修法**：移除处理必须**无条件执行**（stage 侧：并集本来就排除移除项；finalize 侧：移除循环在队列空时也跑）。队列为空但存在 `remove_requested` 时不算 `NO_QUEUE`——那是一次合法的「仅移除」批次。

**反证**：只对一个 `approved` 包执行 `remove`，不点任何「请求构建」→ 跑 stage+finalize 后该行 = `removed`，且（修复 1 之后）下一次含并集的 stage 产物中该 id 消失。

## 4. MEDIUM · 文档承诺的失败命令不存在

**位置**：`cmd/embeddedbuild/main.go:21`——文件头写：

```
go run ./cmd/embeddedbuild -finalize -fail -reason=…  (any step failed)
```

但 flags 只有 `stage / finalize / ok / reason / config / root`（:49-55），**没有 `-fail`**。按文档操作：Go flag 包报 `flag provided but not defined: -fail`、退出码 2——**编排者在失败路径上崩溃**，行停留在 `build_requested`；叠加发现 2 的毒化队列（若已有行被误标），会阻塞后续所有批次。设计要求的「批次回构建失败+摘要」（D-6）永远不发生。

连带问题：裸 `-finalize` 因 `ok` 默认 `false` **静默等于失败语义**——「什么都不加跑一下」的直觉行为是把整批标失败。

**修法**（二选一，推荐前者）：

- 定义 `-fail` 标志（与 `-ok` 互斥校验），使文档命令可运行；或
- 改文档/`-reason` 帮助文本给出真实调用（如 `-finalize -reason=…` 即失败、`-finalize -ok` 才成功），并在 `ok=false && reason==""` 时**拒绝执行**（没有失败理由的失败定格不该发生）。

**反证**：按文件头文档原样执行失败命令 → 进程退出码 0，且目标行 = `build_failed`、事件带 reason；`-finalize`（无参数、无 reason）→ 拒绝执行并提示，不改任何行。

## 5. MEDIUM · `promptfoo_version` 读路径无校验

**位置**：`internal/maclaw/embedded_build.go:60`——烘焙目录的 `promptfoo_version` 被解析进 `embeddedPluginsFile.PromptfooVersion`（`mustParseEmbeddedPluginCatalog`，:66）后**再无任何读取方**；与 `promptfoo_plugin_legality.json` 基座版本的比对只存在于 stage 时的 `ValidateEmbeddedManifestForCatalog`（`embedded_plugin_package.go:205`），而 stage 只对 `build_requested` 行运行。

**失败场景**：平台升级 promptfoo 基座并重生成合法表，期间没有包在烘焙（`NO_QUEUE` 不动目录文件）→ 旧基座校验过的已烘焙插件**继续被目录端点提供、继续通过状态闸**（`approved` → 放行）→ run 派发到引擎、在 `assertPluginIdsAreAccepted`/加载期**中途失败**——正是平台「判定放请求边界、不产生 run 行」规则（L2 提交闸、`ValidateEngineRunPluginLegality`）要消灭的失败形态，这里漏了一条读路径。

**修法**：读路径（`GET /maclaw/engine/embedded-catalog` 过滤，**或** `GuardEmbeddedPluginSelection` 拒绝）比对记录版本与当前基座，不一致即隐藏/拒绝，拒绝码命名版本不匹配（如 `plugin_base_mismatch`），走 `writeEnginePreflightRejection`。

**反证**：手工把 `testdata/embedded_plugins.json` 的 `promptfoo_version` 改成旧值 → 目录端点不再返回该条，且（若走闸）选择该 id 的提交被 409 拒绝、码明确；改回当前版本恢复。

## 6. LOW · 注释错位

**位置**：`internal/api/handler/engine_run.go:161`——`SetEngineInstances` 的文档注释（P26a 租户引擎注册表，:161-164）现在直接压在 `func SetEmbeddedCatalog`（:167）头上；`SetEngineInstances`（:229）本体无注释。godoc 会把租户引擎注册表的文档归属给目录 setter。

**修法**：把该注释块移回 `SetEngineInstances` 之上（`SetEmbeddedCatalog` 保留自己的 E-4 注释）。

**反证**：`go doc`/肉眼确认两函数各自注释归位；无需测试。

---

## 7. 审查过、**不**成立的点（备查，勿重复报告）

- 路由注册位置（admin `{}` 组内注册，enterprise/admin 中间件正确生效）；
- 内嵌 id 过 `ValidateEngineRunPluginLegality`（未知 id 合法是设计使然——目录是 promptfoo 接受面的子集）与 `totalPluginFanout` 默认值；
- 向导提交裸 `item.id`（`GuardEmbeddedPluginSelection` + `writeEnginePreflightRejection` 兜住 `EmbeddedSelectionRefusalError`）；
- `extractZipTo` 的 zip-slip（解压前校验，前缀/clean 检查成立）；
- 单文件读取上限未复核总长度（不可达——包校验已约束解压总大小）；
- `EmbeddedPluginCatalog()` 共享切片的变更风险（当前读方只读）；
- 前端分组/标签代码（`npm run build` 通过）。

## 8. 修复落点建议（供拆分文件时参考）

发现 1–4 同属一族：**「本批是谁」没有唯一事实源**（暂存、定格、移除三处各自查库）。与其在 `runStage`/`runFinalize` 里各修一遍，不如把批次生命周期收敛为一个显式对象（如 `batch.go`：`index.json` 为唯一事实源，stage 建批、finalize 消费批、移除批无条件执行），三处改为读它——这同时是文件拆分的自然切口（`main.go` 目前 7.5 KB 尚可，但批次逻辑已在长大）。
