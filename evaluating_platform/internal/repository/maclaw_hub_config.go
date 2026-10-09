package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/model"
)

const maclawHubConfigID = "default"

type MaclawHubConfigRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawHubConfigRepository(pool *pgxpool.Pool) *MaclawHubConfigRepository {
	return &MaclawHubConfigRepository{pool: pool}
}

func (r *MaclawHubConfigRepository) GetHubConfig(ctx context.Context) (*model.MaclawHubConfigRecord, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, encrypted_config, config_key_id, updated_by, created_at, updated_at
		FROM maclaw_hub_configs
		WHERE id = $1
	`, maclawHubConfigID)
	out, err := scanEncryptedConfigRow(row)
	if err != nil {
		return nil, fmt.Errorf("query maclaw hub config: %w", err)
	}
	if out == nil {
		return nil, nil
	}
	return &model.MaclawHubConfigRecord{
		ID: out.ID, EncryptedConfig: out.EncryptedConfig, ConfigKeyID: out.ConfigKeyID,
		UpdatedBy: out.UpdatedBy, CreatedAt: out.CreatedAt, UpdatedAt: out.UpdatedAt,
	}, nil
}

func (r *MaclawHubConfigRepository) UpsertHubConfig(ctx context.Context, encrypted []byte, keyID string, updatedBy *uuid.UUID) error {
	return upsertEncryptedConfigRow(ctx, r.pool, "maclaw_hub_configs", maclawHubConfigID, encrypted, keyID, updatedBy, "upsert maclaw hub config")
}

// ── 单行加密配置表共享实现（P2-12：maclaw_hub_configs ≡ maclaw_model_defaults）──

type encryptedConfigRow struct {
	ID              string
	EncryptedConfig []byte
	ConfigKeyID     string
	UpdatedBy       *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func scanEncryptedConfigRow(row pgx.Row) (*encryptedConfigRow, error) {
	var out encryptedConfigRow
	err := row.Scan(&out.ID, &out.EncryptedConfig, &out.ConfigKeyID, &out.UpdatedBy, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

func upsertEncryptedConfigRow(ctx context.Context, pool *pgxpool.Pool, table, id string, encrypted []byte, keyID string, updatedBy *uuid.UUID, errPrefix string) error {
	const query = `
		INSERT INTO %s (id, encrypted_config, config_key_id, updated_by)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
			encrypted_config = EXCLUDED.encrypted_config,
			config_key_id = EXCLUDED.config_key_id,
			updated_by = EXCLUDED.updated_by,
			updated_at = NOW()
	`
	if _, err := pool.Exec(ctx, fmt.Sprintf(query, table), id, encrypted, keyID, updatedBy); err != nil {
		return fmt.Errorf("%s: %w", errPrefix, err)
	}
	return nil
}
