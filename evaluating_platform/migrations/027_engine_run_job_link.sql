-- 027_engine_run_job_link.sql — U5：把 confirm fast path 的 `pfj-` 内存 job
-- 落到 engine_runs，使 BFF 重启后前端进度可恢复。
--
-- 背景（U5 批次目标）：
--   聊天里点「确认执行」后，引擎链路用 `pfj-` 前缀的**内存 job** 追踪进度。
--   进程一重启，PlatformEngineJobStore 全清 → 前端轮询
--   GET /api/v1/maclaw/evaluation/jobs/pfj-… 拿不到 job，进度丢失。
--   引擎侧其实已经在 engine_runs 里持久化了 run（见 026 §1），缺的只是
--   「浏览器持有的 job id ↔ 已落库 run」这一条可恢复的链接。
--
-- 本迁移做两件事（**只增不删**，符合 DD-8=B：历史 11 张死表保留不 DROP）：
--   1. engine_runs 增加 job_id 列 + 唯一索引（幂等的**数据库级**保证）。
--   2. engine_runs.source 的取值域从 ('wizard','chat') 放宽到
--      ('wizard','chat','chat_confirm')，区分「聊天确认执行」与「自选评测」。
--
-- ── 安全契约（沿用 026 头注释 / 引擎侧 redact.ts 边界）──────────────
--   job_id 是 BFF 自己生成的 `pfj-<uuid>` 标识符，不是用户内容。
--   本迁移**不新增任何**存放 prompt / payload / 目标响应 / 凭据的列；
--   进度语义完全复用 engine_runs 既有的安全列（counts / stage / duration /
--   脱敏 error_code），因此不会扩大泄漏面。
--
-- ── 幂等性 ──────────────────────────────────────────────────────
--   job_id 的写入走「带守卫的 UPDATE」而非 INSERT：
--       UPDATE … SET job_id = $3
--        WHERE id = $1 AND platform_user_id = $2
--          AND (job_id = '' OR job_id = $3)
--   同一 job 重复写入任意多次，行数恒为 1、值恒不变。
--   下面的唯一索引是第二道防线：即使上层逻辑出错，job_id 也不可能
--   绑到两条 run 上（不会产生重复 run）。
--
-- ── 回滚 ────────────────────────────────────────────────────────
--   无 down migration（与 026 一致）。回滚以反向 SQL 人工执行：
--     ALTER TABLE engine_runs DROP CONSTRAINT engine_runs_source_check;
--     ALTER TABLE engine_runs ADD CONSTRAINT engine_runs_source_check
--         CHECK (source IN ('wizard', 'chat'));
--     ALTER TABLE engine_runs DROP COLUMN job_id;
--   ⚠️ 先 DROP COLUMN job_id 会让已落库的 job↔run 链接丢失，重启后
--      进度恢复能力随之消失（引擎侧 run 记录本身不受影响）。

-- ─── 1. source 取值域放宽：新增 chat_confirm ───────────────────────
-- 区分「聊天里点确认执行」(chat_confirm) 与「自选评测」链路：
--   wizard        自选评测（POST /maclaw/engine/runs，无 session_id）
--   chat          带 session_id 的引擎提交
--   chat_confirm  U5：confirm fast path 的 pfj- job（有 pfj- job_id）
ALTER TABLE engine_runs DROP CONSTRAINT IF EXISTS engine_runs_source_check;
ALTER TABLE engine_runs ADD CONSTRAINT engine_runs_source_check
    CHECK (source IN ('wizard', 'chat', 'chat_confirm'));

-- ─── 2. job_id：pfj- 内存 job ↔ engine_runs 的可恢复链接 ────────────
-- 默认 '' 表示「该 run 不是由 pfj- job 发起」，与历史行兼容（NOT NULL
-- DEFAULT '' 保证存量数据无需回填即可满足约束）。
ALTER TABLE engine_runs
    ADD COLUMN IF NOT EXISTS job_id TEXT NOT NULL DEFAULT '';

-- 幂等第二道防线：一个 pfj- job 最多绑定一条 run。
-- 部分唯一索引（WHERE job_id <> ''）：非 pfj- 链路（job_id = ''）不受约束，
-- 因此自选评测链路可以照旧写入任意多条。
CREATE UNIQUE INDEX IF NOT EXISTS idx_engine_runs_job_id
    ON engine_runs(platform_user_id, job_id)
    WHERE job_id <> '';

-- 重启恢复的读取路径：按 (用户, job_id) 直查。
-- 与上面的唯一索引同列序，planner 可复用。
CREATE INDEX IF NOT EXISTS idx_engine_runs_user_job
    ON engine_runs(platform_user_id, job_id)
    WHERE job_id <> '';

-- ─── 3. 防回归护栏（数据层，非代码层）─────────────────────────────
-- 带 job_id 的行必须是 confirm fast path 链路：source 必须是 chat_confirm。
-- 这样即使上层误把 wizard 链路和 pfj- job 混写，数据库也会拒绝。
ALTER TABLE engine_runs DROP CONSTRAINT IF EXISTS engine_runs_job_source_check;
ALTER TABLE engine_runs ADD CONSTRAINT engine_runs_job_source_check
    CHECK (job_id = '' OR source = 'chat_confirm');

-- job_id 必须遵循 `pfj-<uuid>` 形态，杜绝把任意用户内容写进这一列。
-- 这是「零 payload 泄漏」在 schema 层的兜底：非 pfj- 形态直接拒写。
ALTER TABLE engine_runs DROP CONSTRAINT IF EXISTS engine_runs_job_id_format_check;
ALTER TABLE engine_runs ADD CONSTRAINT engine_runs_job_id_format_check
    CHECK (
        job_id = ''
        OR (job_id LIKE 'pfj-%' AND length(job_id) = 4 + 36)
    );