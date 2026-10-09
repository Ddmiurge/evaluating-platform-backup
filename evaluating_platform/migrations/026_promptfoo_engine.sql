-- 026_promptfoo_engine.sql — persistence for promptfoo-engine runs and
-- reports (Phase 0; §9.1 of the merge plan).
--
-- Safety contract (mirrors the engine adapter's redact.ts boundary):
--   * No target prompts, responses, payloads, credentials, or engine-local
--     paths are ever stored here. Only safe progress metadata and the
--     redacted aggregate result (counts / severities / rates) survive.

-- ═══ 1. engine_runs：引擎任务生命周期（AI 对话链路与自选评测链路统一） ═══
CREATE TABLE IF NOT EXISTS engine_runs (
    id                TEXT PRIMARY KEY,                    -- er-<uuid>
    platform_user_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id       TEXT NOT NULL DEFAULT '',
    session_id        TEXT NOT NULL DEFAULT '',            -- AI 对话链路关联；自选评测链路为空
    source            TEXT NOT NULL DEFAULT 'wizard',      -- wizard | chat
    engine            TEXT NOT NULL DEFAULT 'promptfoo',
    status            TEXT NOT NULL DEFAULT 'queued',      -- queued|generating_tests|executing_probes|engine_judging|compiling_report|succeeded|failed|canceled
    judge_mode        TEXT NOT NULL DEFAULT 'auto',        -- auto|promptfoo_native|platform_rejudge
    purpose           TEXT NOT NULL DEFAULT '',
    plugins           JSONB NOT NULL DEFAULT '[]'::jsonb,   -- [{id,label,config}]（无原文）
    strategies        JSONB NOT NULL DEFAULT '[]'::jsonb,
    target_id         TEXT NOT NULL DEFAULT '',
    num_tests         INTEGER NOT NULL DEFAULT 0,
    planned_count     INTEGER NOT NULL DEFAULT 0,
    executed_count    INTEGER NOT NULL DEFAULT 0,
    current_stage     TEXT NOT NULL DEFAULT '',
    duration_ms       BIGINT NOT NULL DEFAULT 0,
    stage_durations   JSONB NOT NULL DEFAULT '{}'::jsonb,
    token_usage       JSONB NOT NULL DEFAULT '{"generation":0,"judging":0,"target":0}'::jsonb,
    result            JSONB NOT NULL DEFAULT '{}'::jsonb,  -- SafeRunResult（脱敏汇总）
    error_code        TEXT NOT NULL DEFAULT '',            -- 脱敏错误码
    error_brief       TEXT NOT NULL DEFAULT '',            -- 脱敏错误摘要
    started_at        TIMESTAMPTZ,
    completed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT engine_runs_source_check CHECK (source IN ('wizard', 'chat')),
    CONSTRAINT engine_runs_status_check CHECK (status IN (
        'queued', 'generating_tests', 'executing_probes',
        'engine_judging', 'compiling_report',
        'succeeded', 'failed', 'canceled')),
    CONSTRAINT engine_runs_judge_mode_check CHECK (judge_mode IN ('auto', 'promptfoo_native', 'platform_rejudge'))
);
CREATE INDEX IF NOT EXISTS idx_engine_runs_user_created ON engine_runs(platform_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_engine_runs_status ON engine_runs(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_engine_runs_session ON engine_runs(session_id) WHERE session_id <> '';

DROP TRIGGER IF EXISTS trg_engine_runs_updated_at ON engine_runs;
CREATE TRIGGER trg_engine_runs_updated_at
    BEFORE UPDATE ON engine_runs
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
