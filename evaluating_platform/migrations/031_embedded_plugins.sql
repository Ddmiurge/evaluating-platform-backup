-- 031 · P28 内嵌插件轨道（docs/architecture/embedded-plugin-track.md）
-- 状态行=状态机当前态（列表以它为骨架：被拒的包没有资源行也照样可见——
-- 非法代码永不停库，但驳回原因要给专家看）；事件表=不可变审计；
-- 测试白名单=「测试中」阶段谁能看见/选到。
CREATE TABLE IF NOT EXISTS maclaw_embedded_plugins (
    owner_user_id UUID NOT NULL,               -- 归属记在状态表：被拒的包没有资源行
    name TEXT NOT NULL DEFAULT '',             -- 展示名：被拒包无资源行，名字只能记在这
    summary TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',      -- 通过校验后才有资源行；驳回态为空串
    package_id TEXT NOT NULL,                  -- manifest.id，全局唯一（并发同名靠唯一索引兜底）
    state TEXT NOT NULL,                       -- 见 embedded_plugin_lifecycle.go 状态常量
    version TEXT NOT NULL DEFAULT '1',
    auto_gate JSONB NOT NULL DEFAULT '{}',     -- 最近一次自动关卡结果（安全摘要：通过/原因/测试统计，无正文）
    review_reason TEXT NOT NULL DEFAULT '',    -- 管理员「需修改」意见（必填时非空）
    reviewer_user_id UUID,
    build_requested_by UUID,
    build_requested_at TIMESTAMPTZ,
    final_approved_by UUID,
    final_approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_maclaw_embedded_plugins_package
    ON maclaw_embedded_plugins(package_id);

CREATE TABLE IF NOT EXISTS maclaw_embedded_plugin_events (
    id BIGSERIAL PRIMARY KEY,
    package_id TEXT NOT NULL,
    version TEXT NOT NULL,
    event TEXT NOT NULL,                       -- uploaded/auto_gate_rejected/initial_approved/…
    actor_user_id UUID,
    reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_maclaw_embedded_plugin_events_package
    ON maclaw_embedded_plugin_events(package_id, id);

CREATE TABLE IF NOT EXISTS maclaw_embedded_testers (
    platform_user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    granted_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
