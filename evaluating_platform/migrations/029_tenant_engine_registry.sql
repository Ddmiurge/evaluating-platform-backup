-- 029 · P26a 租户引擎注册表（G-1/G-3 的路由地基）
-- 加列不回填（028 风格）：空列 = 走共享实例 = 与今天逐字节一致。
-- 专属实例的登记是运维动作（compose + 本表），生产写入路径在 P26c（token 生成）。
ALTER TABLE maclaw_account_mappings
    ADD COLUMN IF NOT EXISTS engine_base_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS engine_token_ciphertext BYTEA,
    ADD COLUMN IF NOT EXISTS engine_token_key_id TEXT NOT NULL DEFAULT '';

-- run 记住它起跑的实例：生命周期（GetRun/Cancel/Stream/poll）必须打在同一个实例上，
-- 否则 remap/重启会把在跑的 run 打到另一台引擎（404 → 幽灵排队）。
-- 写入是 sticky 的（repository.Update COALESCE），一旦置上永不被进度写清空——
-- 与 job_id 的 U5 教训同构。
ALTER TABLE engine_runs
    ADD COLUMN IF NOT EXISTS engine_base_url TEXT NOT NULL DEFAULT '';
