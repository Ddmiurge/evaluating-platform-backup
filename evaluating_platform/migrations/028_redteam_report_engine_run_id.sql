-- 028_redteam_report_engine_run_id.sql
--
-- 报告与引擎 run 的真实关联键。
--
-- `run_id` 存的是**规划器**给上游的 id：confirm 快速路径里是计划消息 id（`msg_…`），
-- P11 之前是平台旧链路的 `run_…`。引擎 run id（`er-…`）从来没有进过这一列 ——
-- ai02 实测（2026-10-27）72 行报告里 `run_id LIKE 'er-%'` 为 0，而 43/43 份报告都能
-- 通过 `metadata->>'engine_run_id'` 对上一条 engine_runs。
--
-- 用元数据键当 join 键是隐式约定：改名即断，且无法建索引。这里补一列显式关联键，
-- 由 CompileRedteamReport 写入；读取时两种形态都接受（见 GetReportByRunID），
-- 历史行不需要回填。
ALTER TABLE maclaw_redteam_reports
    ADD COLUMN IF NOT EXISTS engine_run_id TEXT NOT NULL DEFAULT '';

-- 读取路径永远是 (owner, engine_run_id)；部分索引跳过未回填的历史行。
CREATE INDEX IF NOT EXISTS idx_maclaw_redteam_reports_engine_run
    ON maclaw_redteam_reports(platform_user_id, engine_run_id)
    WHERE engine_run_id <> '';
