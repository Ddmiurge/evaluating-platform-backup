package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/model"
)

type MaclawAccountMappingRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawAccountMappingRepository(pool *pgxpool.Pool) *MaclawAccountMappingRepository {
	return &MaclawAccountMappingRepository{pool: pool}
}

// maclawAccountMappingColumns 与 scanMaclawAccountMapping 提取自
// GetByPlatformUserID/List 的重复 18 列清单与 Scan 目标（P2-14）。
const maclawAccountMappingColumns = `platform_user_id, platform_role, platform_email, platform_org_name,
		       maclaw_tenant_id, maclaw_user_id, maclaw_credential_id, maclaw_instance_id,
		       encrypted_api_key, encrypted_api_secret, credential_key_id,
		       encrypted_access_token, access_token_key_id, access_token_expires_at,
		       provisioning_status, COALESCE(last_error, ''), created_at, updated_at`

func scanMaclawAccountMapping(row pgx.Row) (*model.MaclawAccountMapping, error) {
	var out model.MaclawAccountMapping
	if err := row.Scan(
		&out.PlatformUserID,
		&out.PlatformRole,
		&out.PlatformEmail,
		&out.PlatformOrgName,
		&out.MaclawTenantID,
		&out.MaclawUserID,
		&out.MaclawCredentialID,
		&out.MaclawInstanceID,
		&out.EncryptedAPIKey,
		&out.EncryptedAPISecret,
		&out.CredentialKeyID,
		&out.EncryptedAccessToken,
		&out.AccessTokenKeyID,
		&out.AccessTokenExpiresAt,
		&out.ProvisioningStatus,
		&out.LastError,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *MaclawAccountMappingRepository) GetByPlatformUserID(ctx context.Context, id uuid.UUID) (*model.MaclawAccountMapping, error) {
	const query = `
		SELECT ` + maclawAccountMappingColumns + `
		FROM maclaw_account_mappings
		WHERE platform_user_id = $1
	`
	out, err := scanMaclawAccountMapping(r.pool.QueryRow(ctx, query, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query maclaw account mapping: %w", err)
	}
	return out, nil
}

func (r *MaclawAccountMappingRepository) Upsert(ctx context.Context, mapping *model.MaclawAccountMapping) error {
	const query = `
		INSERT INTO maclaw_account_mappings (
			platform_user_id, platform_role, platform_email, platform_org_name,
			maclaw_tenant_id, maclaw_user_id, maclaw_credential_id, maclaw_instance_id,
			encrypted_api_key, encrypted_api_secret, credential_key_id,
			encrypted_access_token, access_token_key_id, access_token_expires_at,
			provisioning_status, last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, NULLIF($16, ''))
		ON CONFLICT (platform_user_id) DO UPDATE SET
			platform_role = EXCLUDED.platform_role,
			platform_email = EXCLUDED.platform_email,
			platform_org_name = EXCLUDED.platform_org_name,
			maclaw_tenant_id = EXCLUDED.maclaw_tenant_id,
			maclaw_user_id = EXCLUDED.maclaw_user_id,
			maclaw_credential_id = EXCLUDED.maclaw_credential_id,
			maclaw_instance_id = EXCLUDED.maclaw_instance_id,
			encrypted_api_key = EXCLUDED.encrypted_api_key,
			encrypted_api_secret = EXCLUDED.encrypted_api_secret,
			credential_key_id = EXCLUDED.credential_key_id,
			encrypted_access_token = EXCLUDED.encrypted_access_token,
			access_token_key_id = EXCLUDED.access_token_key_id,
			access_token_expires_at = EXCLUDED.access_token_expires_at,
			provisioning_status = EXCLUDED.provisioning_status,
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`
	if mapping == nil {
		return fmt.Errorf("maclaw account mapping is required")
	}
	_, err := r.pool.Exec(ctx, query,
		mapping.PlatformUserID,
		mapping.PlatformRole,
		mapping.PlatformEmail,
		mapping.PlatformOrgName,
		mapping.MaclawTenantID,
		mapping.MaclawUserID,
		mapping.MaclawCredentialID,
		mapping.MaclawInstanceID,
		mapping.EncryptedAPIKey,
		mapping.EncryptedAPISecret,
		mapping.CredentialKeyID,
		mapping.EncryptedAccessToken,
		mapping.AccessTokenKeyID,
		mapping.AccessTokenExpiresAt,
		mapping.ProvisioningStatus,
		mapping.LastError,
	)
	if err != nil {
		return fmt.Errorf("upsert maclaw account mapping: %w", err)
	}
	return nil
}

func (r *MaclawAccountMappingRepository) List(ctx context.Context, limit, offset int) ([]model.MaclawAccountMapping, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	const query = `
		SELECT ` + maclawAccountMappingColumns + `
		FROM maclaw_account_mappings
		ORDER BY updated_at DESC
		LIMIT $1 OFFSET $2
	`
	rows, err := r.pool.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list maclaw account mappings: %w", err)
	}
	defer rows.Close()
	items := []model.MaclawAccountMapping{}
	for rows.Next() {
		var out model.MaclawAccountMapping
		if err := rows.Scan(
			&out.PlatformUserID,
			&out.PlatformRole,
			&out.PlatformEmail,
			&out.PlatformOrgName,
			&out.MaclawTenantID,
			&out.MaclawUserID,
			&out.MaclawCredentialID,
			&out.MaclawInstanceID,
			&out.EncryptedAPIKey,
			&out.EncryptedAPISecret,
			&out.CredentialKeyID,
			&out.EncryptedAccessToken,
			&out.AccessTokenKeyID,
			&out.AccessTokenExpiresAt,
			&out.ProvisioningStatus,
			&out.LastError,
			&out.CreatedAt,
			&out.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan maclaw account mapping: %w", err)
		}
		items = append(items, out)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate maclaw account mappings: %w", err)
	}
	return items, nil
}

func (r *MaclawAccountMappingRepository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM maclaw_account_mappings`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count maclaw account mappings: %w", err)
	}
	return count, nil
}
