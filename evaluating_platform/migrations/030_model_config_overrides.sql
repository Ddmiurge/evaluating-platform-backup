-- 030 · P26b 生成模型配置的租户镜像（D-3 方案 b）
-- 形状与 maclaw_model_defaults 完全一致（id 为 'user:<平台账号uuid>'），复用仓储的共享
-- 加密行助手。写入路径唯一：管理员门户保存账号模型配置成功推给 runtime 之后镜像；
-- 读路径：引擎 generation 凭据解析按 run 发起方读本表，无行回退平台默认。
-- 镜像失败不阻断保存（响应带 mirror_saved=false + ERROR 日志）：runtime 已生效而镜像
-- 滞后 = 漂移窗口，管理员重存一次即收敛（merge 语义幂等）。
CREATE TABLE IF NOT EXISTS maclaw_model_overrides (
    id TEXT PRIMARY KEY,
    encrypted_config BYTEA NOT NULL,
    config_key_id TEXT NOT NULL,
    updated_by UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
