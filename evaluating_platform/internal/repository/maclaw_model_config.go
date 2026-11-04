package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/model"
)

const maclawDefaultModelConfigID = "default"

type MaclawModelConfigRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawModelConfigRepository(pool *pgxpool.Pool) *MaclawModelConfigRepository {
	return &MaclawModelConfigRepository{pool: pool}
}

// GetDefault 复用单行加密配置表共享实现（P2-12）。
func (r *MaclawModelConfigRepository) GetDefault(ctx context.Context) (*model.MaclawModelDefault, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, encrypted_config, config_key_id, updated_by, created_at, updated_at
		FROM maclaw_model_defaults
		WHERE id = $1
	`, maclawDefaultModelConfigID)
	out, err := scanEncryptedConfigRow(row)
	if err != nil {
		return nil, fmt.Errorf("query maclaw default model config: %w", err)
	}
	if out == nil {
		return nil, nil
	}
	return &model.MaclawModelDefault{
		ID: out.ID, EncryptedConfig: out.EncryptedConfig, ConfigKeyID: out.ConfigKeyID,
		UpdatedBy: out.UpdatedBy, CreatedAt: out.CreatedAt, UpdatedAt: out.UpdatedAt,
	}, nil
}

func (r *MaclawModelConfigRepository) UpsertDefault(ctx context.Context, encrypted []byte, keyID string, updatedBy *uuid.UUID) error {
	return upsertEncryptedConfigRow(ctx, r.pool, "maclaw_model_defaults", maclawDefaultModelConfigID, encrypted, keyID, updatedBy, "upsert maclaw default model config")
}

// ── P26b · 租户模型配置镜像（D-3=b）─────────────────────────────────────────
// 表形状与 defaults 行完全一致，id 用 "user:<uuid>" 命名空间；复用共享加密行助手。

func modelOverrideID(userID uuid.UUID) string { return "user:" + userID.String() }

// GetOverride returns the tenant's mirrored model config, or (nil, nil) when the
// account has never been saved through the admin portal since P26b (the
// engine then falls back to the platform default — the pre-P26b behavior).
func (r *MaclawModelConfigRepository) GetOverride(ctx context.Context, userID uuid.UUID) (*model.MaclawModelDefault, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, encrypted_config, config_key_id, updated_by, created_at, updated_at
		FROM maclaw_model_overrides
		WHERE id = $1
	`, modelOverrideID(userID))
	out, err := scanEncryptedConfigRow(row)
	if err != nil {
		return nil, fmt.Errorf("query maclaw model override: %w", err)
	}
	if out == nil {
		return nil, nil
	}
	return &model.MaclawModelDefault{
		ID: out.ID, EncryptedConfig: out.EncryptedConfig, ConfigKeyID: out.ConfigKeyID,
		UpdatedBy: out.UpdatedBy, CreatedAt: out.CreatedAt, UpdatedAt: out.UpdatedAt,
	}, nil
}

func (r *MaclawModelConfigRepository) UpsertOverride(ctx context.Context, userID uuid.UUID, encrypted []byte, keyID string, updatedBy *uuid.UUID) error {
	return upsertEncryptedConfigRow(ctx, r.pool, "maclaw_model_overrides", modelOverrideID(userID), encrypted, keyID, updatedBy, "upsert maclaw model override")
}
