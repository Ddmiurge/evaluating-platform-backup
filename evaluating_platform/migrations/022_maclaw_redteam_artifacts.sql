CREATE TABLE IF NOT EXISTS maclaw_redteam_evidence (
    id               TEXT        PRIMARY KEY,
    handle           TEXT        NOT NULL UNIQUE,
    platform_user_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id      TEXT        NOT NULL DEFAULT '',
    session_id       TEXT        NOT NULL DEFAULT '',
    run_id           TEXT        NOT NULL DEFAULT '',
    kind             TEXT        NOT NULL DEFAULT 'artifact',
    title            TEXT        NOT NULL DEFAULT '',
    summary          TEXT        NOT NULL DEFAULT '',
    metadata         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_maclaw_redteam_evidence_user_run
    ON maclaw_redteam_evidence(platform_user_id, run_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_maclaw_redteam_evidence_user_instance
    ON maclaw_redteam_evidence(platform_user_id, instance_id, created_at DESC);

DROP TRIGGER IF EXISTS trg_maclaw_redteam_evidence_updated_at ON maclaw_redteam_evidence;
CREATE TRIGGER trg_maclaw_redteam_evidence_updated_at
    BEFORE UPDATE ON maclaw_redteam_evidence
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TABLE IF NOT EXISTS maclaw_redteam_reports (
    id               TEXT        PRIMARY KEY,
    handle           TEXT        NOT NULL UNIQUE,
    platform_user_id UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id      TEXT        NOT NULL DEFAULT '',
    session_id       TEXT        NOT NULL DEFAULT '',
    run_id           TEXT        NOT NULL DEFAULT '',
    title            TEXT        NOT NULL DEFAULT '',
    summary          TEXT        NOT NULL DEFAULT '',
    risk_level       TEXT        NOT NULL DEFAULT '',
    safety_score     DOUBLE PRECISION,
    -- 逐条评测结果。2026-10-16 起每条 finding 持有**原文**：
    --   title = 攻击载荷、description = 目标回答、suggestion = 判定理由、
    --   turns = 多轮转录。凭据值与本部署自己的路径仍在写入前清除
    --   （sanitizeRedteamArtifactText）。这是产品决定：看不到原文的报告不是报告，
    --   见 AGENTS.md 的 maclaw Upgrade Boundary 与
    --   audit-2026-10-09/23-聊天链路报告原文与promptfoo对齐.md。
    -- 上面那张 maclaw_redteam_evidence 表**不受此影响**，仍然只存摘要。
    -- 装得下，无需迁移。
    findings         JSONB       NOT NULL DEFAULT '[]'::jsonb,
    evidence_handles JSONB       NOT NULL DEFAULT '[]'::jsonb,
    metadata         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_maclaw_redteam_reports_user_run
    ON maclaw_redteam_reports(platform_user_id, run_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_maclaw_redteam_reports_user_instance
    ON maclaw_redteam_reports(platform_user_id, instance_id, created_at DESC);

DROP TRIGGER IF EXISTS trg_maclaw_redteam_reports_updated_at ON maclaw_redteam_reports;
CREATE TRIGGER trg_maclaw_redteam_reports_updated_at
    BEFORE UPDATE ON maclaw_redteam_reports
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();
