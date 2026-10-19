# 调查报告：点击「确认执行」后报错 `failed to submit engine evaluation`

- 调查日期：2026-10-12
- 调查方式：纯代码/数据/日志取证 + 一次只读性复现（未修改任何代码、未改动数据库）
- 涉及版本：2026-10-12 从 `evaluating-platform-backup-deploy-20261012.zip` 同步并部署的新版本平台（zip 内文件时间戳 2026-10-12 15:37）
- 涉及会话：`sess_71180d3c-517e-4aa4-a252-8fc3184013de`（企业账号 `enterprise@demo.com`，被测对象：Dify 靶场助手，kind=agent）

---

## 1. 现象

用户在「Dify 靶场助手 · 系统提示与配置泄露专项评估方案」的 `plan_confirm` 卡片上点击「确认执行」后，前端弹出错误提示：

```
failed to submit engine evaluation
```

后端访问日志显示当天共有 **3 次确认请求全部失败**（均为 502，且都在几毫秒内返回，说明失败发生在本地、未触达 promptfoo 引擎网络调用）：

```
[GIN] 2026/10/12 - 18:30:46 | 502 |   4.40526ms | POST "/api/v1/maclaw/evaluation/sessions/sess_71180d3c-.../confirm"
[GIN] 2026/10/12 - 18:30:58 | 502 |   7.460232ms | POST "/api/v1/maclaw/evaluation/sessions/sess_71180d3c-.../confirm"
[GIN] 2026/10/12 - 18:31:59 | 502 |   7.434643ms | POST "/api/v1/maclaw/evaluation/sessions/sess_71180d3c-.../confirm"
```

---

## 2. 结论（根因）

**新版本代码要求 `engine_runs` 表有 `job_id` 列，但数据库没有该列——因为新版本自带的数据库迁移 `027_engine_run_job_link.sql` 没有被注册进后端的迁移清单，从未执行。**

具体是一处**版本内代码遗漏**（不是本次部署操作的问题）：

- 新版本引入了 U5 功能：把「确认执行」产生的 `pfj-<uuid>` 内存任务与已落库的 `engine_runs` 记录绑定，使 BFF 重启后前端进度可恢复。为此：
  1. 新增迁移 `evaluating_platform/migrations/027_engine_run_job_link.sql`，给 `engine_runs` 表增加 `job_id` 列、两个部分索引和 CHECK 约束，并把 `source` 取值域放宽为 `('wizard','chat','chat_confirm')`；
  2. Go 持久层 `internal/repository/engine_run.go` 的 INSERT/UPDATE 列清单（`engineRunColumns` / `engineRunWriteColumns`）**无条件写入了 `job_id` 列**。
- 但是执行迁移的入口 `cmd/server/main.go → runMigrations()` 维护的是一份**硬编码文件清单，只列到 `026_promptfoo_engine.sql`，没有加 `027`**。`pkg/db/postgres.go → MigrateUp()` 没有版本号跟踪，只是逐个执行清单里的 SQL 文件——没列出的迁移文件永远不会被执行。
- 后端启动时 `migrate_on_start: true` 正常生效（启动日志有 `database migration completed`），但执行的清单里没有 027，因此线上库的 `engine_runs` 表停留在 026 的结构（`job_id` 列不存在，`source` CHECK 仍只允许 `wizard/chat`）。
- 于是新代码 INSERT 时触发 PostgreSQL 错误 `SQLSTATE 42703: column "job_id" of relation "engine_runs" does not exist`，被 `PreparePromptfooEngineRun` 包装为 `persist engine run: ...`，最终由 `engine_confirm.go` 以 502 `engine_submit_failed`（"failed to submit engine evaluation"）返回给前端。

一句话：**代码升到了 027 的结构，数据库迁移清单停在 026。**

---

## 3. 复现与证据链

### 3.1 复现（只读性复现，未产生任何脏数据）

使用 `.env` 中的 `JWT_SECRET` 为企业账号构造合法 JWT，重放与用户完全相同的确认请求：

```bash
POST /api/v1/maclaw/evaluation/sessions/sess_71180d3c-517e-4aa4-a252-8fc3184013de/confirm
{"plan_message_id":"msg_c9d1fe7b-9e7e-4838-bf13-b985f53b905e","test_count":10}
```

响应（HTTP 502）：

```json
{
  "code": "engine_submit_failed",
  "detail": "persist engine run: ERROR: column \"job_id\" of relation \"engine_runs\" does not exist (SQLSTATE 42703)",
  "error": "failed to submit engine evaluation"
}
```

前端只展示 `error` 字段（`ChatPage.tsx → extractSendErrorMessage`），所以用户看到的只有 "failed to submit engine evaluation"，真正的数据库错误在 `detail` 里。

### 3.2 数据库现状

`\d engine_runs`（ep_postgres，evaluating_platform 库）的列清单（节选）：

```
id, platform_user_id, instance_id, session_id, source, engine, status,
judge_mode, purpose, plugins, strategies, target_id, num_tests,
planned_count, executed_count, current_stage, duration_ms, stage_durations,
token_usage, result, error_code, error_brief, started_at, completed_at,
created_at, updated_at
```

**没有 `job_id` 列。** 现存的 `source` CHECK 约束也仍是 026 版本：

```
engine_runs_source_check: CHECK ((source = ANY (ARRAY['wizard'::text, 'chat'::text])))
```

（而 027 会把它放宽为含 `chat_confirm`。）

当天 3 次失败在 `engine_runs` 表中**没有留下任何记录**（最近一条记录是 2026-10-08 的成功记录），与「失败发生在 INSERT 语句本身」完全吻合。

### 3.3 代码证据（新版本）

**① 迁移文件存在且幂等** —— `migrations/027_engine_run_job_link.sql`：

```sql
ALTER TABLE engine_runs DROP CONSTRAINT IF EXISTS engine_runs_source_check;
ALTER TABLE engine_runs ADD CONSTRAINT engine_runs_source_check
    CHECK (source IN ('wizard', 'chat', 'chat_confirm'));
ALTER TABLE engine_runs
    ADD COLUMN IF NOT EXISTS job_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_engine_runs_job_id ...
CREATE INDEX IF NOT EXISTS idx_engine_runs_user_job ...
...（另有 job_source_check / job_id_format_check 两个 CHECK）
```

**② 持久层无条件写 `job_id`** —— `internal/repository/engine_run.go`：

```go
const engineRunColumns = `
  id, platform_user_id, instance_id, session_id, source, job_id, engine, status, judge_mode, ...
`
const engineRunWriteColumns = `
  id, platform_user_id, instance_id, session_id, source, job_id, engine, status, judge_mode, ...
`
```

**③ 迁移清单没有 027** —— `cmd/server/main.go → runMigrations()`（第 452-475 行）：

```go
paths := []string{
    "migrations/001_init.sql",
    "migrations/003_tool_categories.sql",
    ...
    "migrations/025_attack_sample_categories.sql",
    "migrations/026_promptfoo_engine.sql",     // ← 清单到此为止，027 缺失
}
```

**④ 迁移执行器无版本跟踪** —— `pkg/db/postgres.go`：

```go
func MigrateUp(ctx context.Context, pool *pgxpool.Pool, migrationFile string) error {
    sql, err := os.ReadFile(migrationFile)   // 读文件
    ...
    _, err = pool.Exec(ctx, string(sql))     // 直接整文件执行，没有 schema_migrations 表
```

**⑤ 故障传播链** —— `internal/maclaw/promptfoo_engine_bridge.go → PreparePromptfooEngineRun()`：

```go
saved, err := b.engineRuns.Save(ctx, record)   // INSERT 含 job_id → 42703
if err != nil {
    return nil, fmt.Errorf("persist engine run: %w", err)   // ← detail 的前缀来源
}
```

`internal/api/handler/engine_confirm.go`（第 123-127 行）：

```go
prepared, err := h.engineBridge.PreparePromptfooEngineRun(...)
if err != nil {
    c.JSON(http.StatusBadGateway, gin.H{"error": "failed to submit engine evaluation",
        "code": "engine_submit_failed", "detail": err.Error()})   // ← 用户看到的报错
    return
}
```

### 3.4 版本对比（旧版本为什么没事）

对比同步前备份（`deploy-backups/20261012-163000-pre-sync-source.tar.gz`）：

- 旧版 `internal/repository/engine_run.go` 中 **`job_id` 出现次数为 0**（U5 是本次新增功能，旧代码不写该列，旧库结构够用）；
- 旧版 `migrations/` 目录里**没有 027 文件**（本次 zip 唯一新增的迁移就是 027，026 与旧版逐字节一致）；
- 旧版 `runMigrations()` 清单同样止于 026——因为旧代码只需要 026。

也就是说：**新版本补了迁移文件、补了写列代码，唯独漏改了 `runMigrations()` 的清单**，属于新版本自身的遗漏，与本次「同步 + 重建镜像 + 重启」的部署操作无关（部署流程本身没有跳过任何启动步骤）。

---

## 4. 完整故障链路（从点击按钮到报错）

```
用户点击「确认执行」
  └─ 前端 POST /api/v1/maclaw/evaluation/sessions/:id/confirm
       {plan_message_id: msg_c9d1fe7b-…, test_count: 10}
     └─ BFF ConfirmPlan（internal/api/handler/maclaw_runtime.go）
          ├─ 找到 plan_confirm 卡片（5 个 promptfoo 插件，纯引擎能力）
          └─ 进入引擎快速路径 confirmPromptfooEnginePlan（engine_confirm.go）
               └─ PreparePromptfooEngineRun（promptfoo_engine_bridge.go）
                    ├─ 引擎已配置 ✅
                    ├─ purpose 非空 ✅
                    ├─ U6 远程生成门禁校验 ✅（所选 5 个插件均非 gated，见 §5）
                    ├─ 自定义插件/策略解析 ✅（无 pf_plugin: 自定义包）
                    ├─ 读取被测目标（Dify agent 目标，enabled=true）✅
                    ├─ 生成模型凭据解析 ✅
                    └─ engineRuns.Save → INSERT … (…, job_id, …)
                         └─ PostgreSQL: 42703 column "job_id" does not exist
                              └─ 502 {"error":"failed to submit engine evaluation",
                                      "code":"engine_submit_failed",
                                      "detail":"persist engine run: ERROR: …"}
                                   └─ 前端 message.error("failed to submit engine evaluation")
```

---

## 5. 已排除的其他假设（取证过程）

调查初期曾在以下环节怀疑，均已被证据排除，记录如下：

| 假设 | 排除依据 |
|---|---|
| 所选插件命中 U6「需远程生成」门禁被拒 | 计划卡片选了 `prompt-extraction`、`pii`、`pii:social`、`prompt-injection`、`cross-session-leak`；对照 `testdata/promptfoo_remote_dep.json`（`catalog_marked_plugins`，共 90 个 gated）逐一核验，5 个全部 **NOT-MARKED**（`system-prompt-override`/`ascii-smuggling`/`indirect-prompt-injection` 才是 gated，MaClaw 规划时已主动排除并写进卡片备注）。若是门禁拒绝，错误码应是 `plugin_requires_remote_generation` 而非 `engine_submit_failed` |
| 新配置的 agent 被测目标不可用/未启用 | `GET /api/v1/maclaw/evaluation/targets` 返回目标 `25c8a2ac-…`（Dify靶场助手，kind=agent，`enabled:true`，`credential_secret_set:true`）；18:28:20 的 health-check 返回 200；`ProbeTarget` 走的就是同一条 `GetTargetWithSecret` 解密路径，目标读写链路正常 |
| 平台生成模型未配置 | `maclaw_model_defaults` 表存在 default 行（2026-09-28 更新）；且本错误若因生成凭据缺失，detail 应为 `platform generation model is not configured`，与实际 detail 不符 |
| promptfoo 引擎未启动/不可达 | `ep_promptfoo_engine` 容器 healthy，`/health` 返回 ok；且 502 在 4-7ms 内返回、`engine_runs` 无记录，说明根本没走到 `CreateRun` 的 HTTP 调用（若走到那里，失败前会先成功 INSERT 一条记录） |
| 本次部署操作破坏了数据库 | 数据卷 `postgres_data` 全程未动；缺列是结构性的（027 从未在这套库上执行过），与部署操作无关 |

---

## 6. 影响范围

在该库结构补齐之前，**所有需要持久化 `engine_runs` 的链路都会同样失败**，不限于聊天确认：

| 链路 | 入口 | 是否受影响 |
|---|---|---|
| 聊天 plan_confirm → 确认执行（本次故障） | `POST /api/v1/maclaw/evaluation/sessions/:id/confirm` | ✅ 受影响 |
| 自选评测向导 | `POST /api/v1/maclaw/engine/runs`（engine_run.go） | ✅ 受影响（同一 `EngineRunService.Save`） |
| MaClaw 工具路径提交引擎评测 | `RunPromptfooRedteamEvaluation`（redteam_bridge.go） | ✅ 受影响 |
| 引擎运行过程中的进度落库 UPDATE | `EngineRunService.Update`（repository UPDATE 同样含 `job_id`） | ✅ 受影响 |

不受影响：普通对话、能力目录搜索、目标配置、历史报告查看等不写 `engine_runs` 的功能（当前平台整体健康，`/api/v1/health` 正常）。

> 附注：日志中 18:25:51 还有一次另一会话 `sess_2c786e17-…` 的 `POST …/messages` 返回 504（3m0s）。那是 MaClaw 规划阶段的 LLM 长调用超时，与本报告的 502 属于**不同问题**，本报告不展开。

---

## 7. 修复建议（本文档仅建议，未执行）

两个方案，推荐方案 A（代码层根治，同时修复清单遗漏本身）：

### 方案 A：补注册迁移并重启（根治，推荐）

1. 在 `cmd/server/main.go` 的 `runMigrations()` 清单中，`"migrations/026_promptfoo_engine.sql"` 之后追加一行：
   ```go
   "migrations/027_engine_run_job_link.sql",
   ```
2. 重建 backend 镜像并重启（027 全文幂等：`ADD COLUMN IF NOT EXISTS`、`CREATE … INDEX IF NOT EXISTS`、`DROP CONSTRAINT IF EXISTS` + 重建约束，重复执行安全；`migrate_on_start: true` 会在启动时自动应用）。
3. 修复后应同步补一个防回归检查：例如在测试中断言「migrations/ 目录中的每个文件都出现在 runMigrations 清单里」（当前仓库没有任何机制拦截这种遗漏，027 正是这样漏掉的）。

### 方案 B：一次性手工执行迁移（临时救急）

不改代码，直接对 `evaluating_platform` 库执行 `migrations/027_engine_run_job_link.sql` 全文。该文件幂等，效果与方案 A 的启动迁移完全一致；但代码清单仍是漏的，下次若有新迁移仍可能重演，建议之后仍回到方案 A。

### 回滚（如需）

027 头注释给出了反向 SQL（删约束重建、`DROP COLUMN job_id`），仅影响 U5 的「重启后进度恢复」能力，不影响引擎评测本身。

### 修复后的验证方法

1. `sudo -n docker exec ep_postgres psql -U postgres -d evaluating_platform -c '\d engine_runs'` —— 确认出现 `job_id` 列、`engine_runs_source_check` 含 `chat_confirm`、存在 `idx_engine_runs_job_id` / `idx_engine_runs_user_job` / `engine_runs_job_source_check` / `engine_runs_job_id_format_check`；
2. 在同一会话重新点击「确认执行」（或重放 §3.1 请求）—— 预期返回 **202** 与 `pfj-` job；
3. `engine_runs` 表出现 `source='chat_confirm'`、`job_id='pfj-…'` 的新记录，评测进入执行并出报告；
4. 前端进度卡片正常轮询 `GET /api/v1/maclaw/evaluation/jobs/pfj-…`。

---

## 8. 附录：关键事实时间线

| 时间（2026-10-12，UTC+8） | 事件 |
|---|---|
| 17:30:52 | 新版本 backend 启动，执行注册清单内迁移（止于 026），日志 `database migration completed` |
| 18:28:20 | 用户保存 Dify agent 被测目标（POST /targets → 201），health-check 通过 |
| 18:28:24 | 创建评测会话 `sess_71180d3c-…` |
| 18:30:08 | MaClaw 生成 plan_confirm 卡片（系统提示与配置泄露专项，5 个 promptfoo 插件，test_count=10） |
| 18:30:46 / 18:30:58 / 18:31:59 | 3 次「确认执行」均 502 `engine_submit_failed`（4.4/7.5/7.4ms），`engine_runs` 无记录 |
| ~18:56 | 调查复现同一请求，取得完整 `detail`：`persist engine run: ERROR: column "job_id" of relation "engine_runs" does not exist (SQLSTATE 42703)` |

—— 报告完 ——
