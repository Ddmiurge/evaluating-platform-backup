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
