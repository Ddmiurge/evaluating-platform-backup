# 调查报告（续）：确认执行修复后又遇 `failed to persist engine job`

- 调查日期：2026-10-13
- 前置背景：`audits/audit-2026-10-12-engine-confirm-failure.md`（027 迁移未注册导致 `engine_submit_failed`），该问题已由仓库 [Ddmiurge/evaluating-platform-backup](https://github.com/Ddmiurge/evaluating-platform-backup) `main` 分支提交 `e63d2e2` 修复
- 调查方式：纯代码/数据/日志取证 + 一次确认请求重放（未修改任何代码、未改动数据库）
- 涉及版本：服务器已同步 `ddmiurge/main @ e63d2e2` 并重建 backend（2026-10-13 10:31）
- 涉及会话：`sess_71180d3c-517e-4aa4-a252-8fc3184013de`（企业账号 `enterprise@demo.com`，被测对象：Dify 靶场助手，kind=agent）

---

## 1. 现象

027 修复部署后重放同一「确认执行」请求，报错从昨天的 502 `engine_submit_failed` **变成了**：

```json
HTTP 500
{"code":"engine_job_persist_failed","error":"failed to persist engine job"}
```

后端日志（本次有明确错误日志）：

```
2026/10/13 10:33:40 [ERROR] promptfoo engine job link failed
  (run=er-63f78f83f95f41429a66fee5de99c6bf job=pfj-ede40889-0a16-47ae-bb88-5b7a2acd8fd5
   user=df98c402-8f90-485e-af9c-ef771183b746):
  link engine job: ERROR: could not determine data type of parameter $3 (SQLSTATE 42P18)
```

**先说结论的重要前提：昨天的问题确实修好了。** 本次请求已经走过了昨天失败的环节（`engine_runs` 成功落库、引擎成功执行完 10 条用例），是**再往后一步**的新问题。

## 2. 根因

`internal/repository/engine_run.go → LinkJob()`（第 229-241 行）的 SQL **参数编号写错**：SQL 文本引用了 `$4`，但调用只传了 3 个参数，`$3` 在 SQL 中从未出现：

```go
const query = `
    UPDATE engine_runs SET
        job_id = $4,                 ← 引用 $4
        source = 'chat_confirm',
        updated_at = NOW()
    WHERE id = $1
      AND platform_user_id = $2
      AND (job_id = '' OR job_id = $4)  ← 又是 $4
    RETURNING ` + engineRunColumns
record, err := scanEngineRun(r.pool.QueryRow(ctx, query, runID, userID, jobID))
                                                        // ↑ 只传了 3 个参数
```

pgx 按位置绑定：`$1=runID`、`$2=userID`、`$3=jobID`。但 SQL 里 `jobID` 应该出现的位置全写成了 `$4`，于是：

- PostgreSQL 收到 Parse 请求，声明了 3 个参数（$1/$2/$3）；
- SQL 文本中 `$3` 没有任何使用处 → **无法推断 $3 的数据类型** → Parse 阶段直接报 `SQLSTATE 42P18: could not determine data type of parameter $3`；
- （顺带地，`$4` 也从未被绑定——即使 $3 不报错，$4 也会因参数不存在而失败。）

属典型笔误：应为 `SET job_id = $3 … AND (job_id = '' OR job_id = $3)`，或改为一致的四参数编号。该文件在 `ddmiurge/main`（e63d2e2）即如此；**已检查 `codex/redteam-platform-20261009`、`codex/unify-U1`、`codex/unify-U2` 三个分支，均无此文件的修复版本**（这几个分支尚无该文件）。

## 3. 为什么昨天没发现

昨天（10-12）的失败发生在更早的 `engineRuns.Save`（INSERT），`LinkJob` 从未被执行到——这是一条**从未被任何真实链路踩到的代码**：

- 单测层面：`engine_run_service_test.go` / `engine_job_persistence_test.go` 用的是 `fakeEngineRunStore`（内存 fake），**没有对着真 PostgreSQL 跑过这条 SQL**，参数编号错误在 fake 上不可见；
- 本地环境：`docs/LOCAL_TEST_ENV.md` 记录的本地全量测试是「无 Docker」环境，同样不会连真库。

## 4. 当前链路的实际状态（重要：引擎已真实执行完毕）

确认执行的执行顺序是：`Save（落库）→ CreateRun（提交引擎）→ 生成 pfj- 任务 → LinkJob（绑定，本次失败点）→ 后台等待+进度镜像`。

本次 `Save` 和 `CreateRun` 都成功，因此：

- 引擎侧已完整跑完这次评测（直接查 promptfoo-engine 容器确认）：

```json
GET http://promptfoo-engine:8090/runs/er-63f78f83f95f41429a66fee5de99c6bf
{"phase":"succeeded","planned_count":10,"executed_count":10,"duration_ms":51999,
 "result":{"totals":{"probes":10,"attack_success":0,"pass_rate":1}, ...}}
```

  10 条用例全部执行（Dify 智能体 0 条泄露，全部抵抗成功）——**目标调用已真实消耗**；
- 但 LinkJob 失败后 BFF 直接返回 500，**没有**启动 `waitPromptfooEngineJob` 后台协程：无进度镜像、无报告编译、无证据落库；
- `engine_runs` 表留下一条永久卡在 `queued` 的孤儿记录（`er-63f78f83…`，`job_id=''`）；内存 job 被 `MarkPersistFailed` 标记失败（重启即消失）。

## 5. 影响范围

| 链路 | 是否受影响 |
|---|---|
| 聊天 plan_confirm → 确认执行（`confirmPromptfooEnginePlan`，LinkJob 唯一调用方） | ✅ **每次必现 500**，且都发生在引擎已消耗完目标调用之后 |
| 自选评测向导（`POST /api/v1/maclaw/engine/runs`） | ❌ 不经过 LinkJob，不受影响 |
| MaClaw 工具路径（`RunPromptfooRedteamEvaluation`） | ❌ 不经过 LinkJob，不受影响 |

即：**只有「确认执行」入口被此问题挡死**，且每次重试都会真实消耗一轮引擎评测（目标 LLM 调用 + 判定），建议在修复前不要反复点「确认执行」。

## 6. 修复建议（本文档仅建议，未执行；需在 Ddmiurge 仓库修改）

1. **SQL 参数编号改为三参一致**（最小改动）：

   ```sql
   UPDATE engine_runs SET
       job_id = $3,
       source = 'chat_confirm',
       updated_at = NOW()
   WHERE id = $1
     AND platform_user_id = $2
     AND (job_id = '' OR job_id = $3)
   RETURNING …
   ```

   传参保持 `(ctx, query, runID, userID, jobID)` 不变。
2. 建议补一个**针对真实 PostgreSQL 的集成测试**（或至少在 CI 中用 ephemeral postgres 跑一遍 `LinkJob` 的幂等/互斥两个用例）——本次与 027 两次问题同属一类：fake/无库环境测不出的"代码与数据库契约漂移"。
3. 修复后对存量数据的处理：孤儿记录 `er-63f78f83…`（status=queued、job_id=''）不影响新运行（部分唯一索引只约束 `job_id <> ''` 的行），可保留观察或手工清理，无需数据迁移。

## 7. 修复后的验证方法

1. 同一会话重新点击「确认执行」（或重放 §1 请求）→ 预期 **202**，返回 `pfj-` job；
2. `engine_runs` 出现 `source='chat_confirm'`、`job_id='pfj-…'` 的记录，且随引擎进度更新 status（queued→…→succeeded）；
3. 前端进度卡片正常轮询并最终展示报告；
4. `docker logs ep_backend` 不再出现 `promptfoo engine job link failed`。

## 8. 附：两次问题的关系

| | 第一次（10-12） | 第二次（本次，10-13） |
|---|---|---|
| 报错 | 502 `engine_submit_failed` | 500 `engine_job_persist_failed` |
| 挂点 | `PreparePromptfooEngineRun → Save`（INSERT） | `LinkJob`（UPDATE 绑定 pfj-↔run） |
| 根因 | 027 迁移未注册，`job_id` 列不存在 | SQL 参数编号 `$4`/`$3` 笔误 |
| 引擎是否执行 | ❌ 未触达 | ✅ 已执行完（10/10 用例） |
| 修复方 | 仓库 `e63d2e2`（已部署验证 ✅） | 待仓库修复 |

—— 报告完 ——
