package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/model"
)

type MaclawResourcePublicationRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawResourcePublicationRepository(pool *pgxpool.Pool) *MaclawResourcePublicationRepository {
	return &MaclawResourcePublicationRepository{pool: pool}
}

func (r *MaclawResourcePublicationRepository) UpsertPublication(ctx context.Context, item *model.MaclawResourcePublication) error {
	assessmentTypes, err := json.Marshal(item.AssessmentTypes)
	if err != nil {
		return fmt.Errorf("marshal assessment types: %w", err)
	}
	tags, err := json.Marshal(item.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	const query = `
		INSERT INTO maclaw_resource_publications (
			source_expert_user_id, source_maclaw_tenant_id, source_resource_id, source_resource_handle,
			source_version, name, kind, status, enabled, summary, assessment_types, tags, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, $13::jsonb)
		ON CONFLICT (source_resource_id) DO UPDATE SET
			source_expert_user_id = EXCLUDED.source_expert_user_id,
			source_maclaw_tenant_id = EXCLUDED.source_maclaw_tenant_id,
			source_resource_handle = EXCLUDED.source_resource_handle,
			source_version = EXCLUDED.source_version,
			name = EXCLUDED.name,
			kind = EXCLUDED.kind,
			status = EXCLUDED.status,
			enabled = EXCLUDED.enabled,
			summary = EXCLUDED.summary,
			assessment_types = EXCLUDED.assessment_types,
			tags = EXCLUDED.tags,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
	`
	_, err = r.pool.Exec(ctx, query,
		item.SourceExpertUserID,
		item.SourceMaclawTenantID,
		item.SourceResourceID,
		item.SourceResourceHandle,
		item.SourceVersion,
		item.Name,
		item.Kind,
		item.Status,
		item.Enabled,
		item.Summary,
		string(assessmentTypes),
		string(tags),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("upsert maclaw resource publication: %w", err)
	}
	return nil
}

func (r *MaclawResourcePublicationRepository) MarkPublicationUnavailable(ctx context.Context, sourceExpertUserID uuid.UUID, sourceResourceID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE maclaw_resource_publications
		SET enabled = false, status = 'archived', updated_at = NOW()
		WHERE source_expert_user_id = $1 AND source_resource_id = $2
	`, sourceExpertUserID, strings.TrimSpace(sourceResourceID))
	if err != nil {
		return fmt.Errorf("mark maclaw resource publication unavailable: %w", err)
	}
	return nil
}

func (r *MaclawResourcePublicationRepository) ListPublished(ctx context.Context, q maclaw.EvaluationResourceQuery) ([]model.MaclawResourcePublication, error) {
	conds := []string{"enabled = true", "status = 'published'"}
	args := []any{}
	if q.Kind != "" {
		args = append(args, string(q.Kind))
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if tokens := resourcePublicationSearchTokens(q.Query); len(tokens) > 0 {
		searchConds := make([]string, 0, len(tokens))
		for _, token := range tokens {
			args = append(args, "%"+token+"%")
			idx := len(args)
			searchConds = append(searchConds, fmt.Sprintf("(name ILIKE $%d OR summary ILIKE $%d OR assessment_types::text ILIKE $%d OR tags::text ILIKE $%d OR metadata::text ILIKE $%d)", idx, idx, idx, idx, idx))
		}
		conds = append(conds, "("+strings.Join(searchConds, " OR ")+")")
	}
	limitSQL := ""
	if q.Limit > 0 {
		args = append(args, q.Limit)
		limitSQL = fmt.Sprintf(" LIMIT $%d", len(args))
	}
	query := `
		SELECT ` + resourcePublicationColumns + `
		FROM maclaw_resource_publications
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY updated_at DESC` + limitSQL
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list maclaw resource publications: %w", err)
	}
	defer rows.Close()
	return scanResourcePublications(rows)
}

func resourcePublicationSearchTokens(query string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	replacer := strings.NewReplacer("-", " ", "_", " ", "/", " ", ",", " ", ";", " ", "|", " ")
	parts := strings.Fields(replacer.Replace(query))
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) < 2 || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}

func (r *MaclawResourcePublicationRepository) ListAdmin(ctx context.Context, q maclaw.EvaluationResourceQuery, includeInactive bool) ([]model.MaclawResourcePublication, error) {
	conds := []string{"1=1"}
	args := []any{}
	if !includeInactive {
		conds = append(conds, "enabled = true", "status = 'published'")
	}
	if q.Kind != "" {
		args = append(args, string(q.Kind))
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if strings.TrimSpace(q.Query) != "" {
		args = append(args, "%"+strings.TrimSpace(q.Query)+"%")
		conds = append(conds, fmt.Sprintf("(name ILIKE $%d OR summary ILIKE $%d)", len(args), len(args)))
	}
	limitSQL := ""
	if q.Limit > 0 {
		args = append(args, q.Limit)
		limitSQL = fmt.Sprintf(" LIMIT $%d", len(args))
	}
	query := `
		SELECT ` + resourcePublicationColumns + `
		FROM maclaw_resource_publications
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY updated_at DESC` + limitSQL
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list admin maclaw resource publications: %w", err)
	}
	defer rows.Close()
	return scanResourcePublications(rows)
}

func (r *MaclawResourcePublicationRepository) UpdateGovernance(ctx context.Context, sourceResourceID string, enabled *bool, status string) (*model.MaclawResourcePublication, error) {
	sourceResourceID = strings.TrimSpace(sourceResourceID)
	if sourceResourceID == "" {
		return nil, fmt.Errorf("source resource id is required")
	}
	current, err := r.getBySourceResourceID(ctx, sourceResourceID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, nil
	}
	nextEnabled := current.Enabled
	if enabled != nil {
		nextEnabled = *enabled
	}
	nextStatus := current.Status
	if strings.TrimSpace(status) != "" {
		nextStatus = strings.TrimSpace(status)
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE maclaw_resource_publications
		SET enabled = $2, status = $3, updated_at = NOW()
		WHERE source_resource_id = $1
	`, sourceResourceID, nextEnabled, nextStatus)
	if err != nil {
		return nil, fmt.Errorf("update maclaw resource publication governance: %w", err)
	}
	return r.getBySourceResourceID(ctx, sourceResourceID)
}

func (r *MaclawResourcePublicationRepository) Count(ctx context.Context) (published int, total int, err error) {
	err = r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE enabled = true AND status = 'published'),
			COUNT(*)
		FROM maclaw_resource_publications
	`).Scan(&published, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("count maclaw resource publications: %w", err)
	}
	return published, total, nil
}

// resourcePublicationColumns 提取自 4 处手写列清单（P2-13）。
const resourcePublicationColumns = `source_expert_user_id, source_maclaw_tenant_id, source_resource_id, source_resource_handle,
		       source_version, name, kind, status, enabled, summary, assessment_types, tags, metadata,
		       created_at, updated_at`

// queryFirstPublication 统一单条发布的查询/扫描/空结果语义（P2-13）。
func (r *MaclawResourcePublicationRepository) queryFirstPublication(ctx context.Context, whereSQL string, arg any) (*model.MaclawResourcePublication, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ` + resourcePublicationColumns + `
		FROM maclaw_resource_publications
		WHERE ` + whereSQL, arg)
	if err != nil {
		return nil, fmt.Errorf("query maclaw resource publication: %w", err)
	}
	defer rows.Close()
	items, err := scanResourcePublications(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

func (r *MaclawResourcePublicationRepository) getBySourceResourceID(ctx context.Context, sourceResourceID string) (*model.MaclawResourcePublication, error) {
	return r.queryFirstPublication(ctx, "source_resource_id = $1", sourceResourceID)
}

func (r *MaclawResourcePublicationRepository) GetPublishedByHandle(ctx context.Context, handle string) (*model.MaclawResourcePublication, error) {
	return r.queryFirstPublication(ctx, "(source_resource_handle = $1 OR source_resource_id = $1) AND enabled = true AND status = 'published'", strings.TrimSpace(handle))
}

type MaclawResourceShadowRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawResourceShadowRepository(pool *pgxpool.Pool) *MaclawResourceShadowRepository {
	return &MaclawResourceShadowRepository{pool: pool}
}

func (r *MaclawResourceShadowRepository) GetShadow(ctx context.Context, enterpriseUserID uuid.UUID, sourceResourceID, sourceVersion string) (*model.MaclawResourceShadow, error) {
	const query = `
		SELECT enterprise_user_id, enterprise_maclaw_tenant_id, source_expert_user_id, source_resource_id,
		       source_version, shadow_resource_id, shadow_resource_handle, sync_status, COALESCE(last_error, ''),
		       created_at, updated_at
		FROM maclaw_resource_shadows
		WHERE enterprise_user_id = $1 AND source_resource_id = $2 AND source_version = $3
	`
	var out model.MaclawResourceShadow
	err := r.pool.QueryRow(ctx, query, enterpriseUserID, strings.TrimSpace(sourceResourceID), strings.TrimSpace(sourceVersion)).Scan(
		&out.EnterpriseUserID,
		&out.EnterpriseMaclawTenantID,
		&out.SourceExpertUserID,
		&out.SourceResourceID,
		&out.SourceVersion,
		&out.ShadowResourceID,
		&out.ShadowResourceHandle,
		&out.SyncStatus,
		&out.LastError,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query maclaw resource shadow: %w", err)
	}
	return &out, nil
}

func (r *MaclawResourceShadowRepository) UpsertShadow(ctx context.Context, item *model.MaclawResourceShadow) error {
	const query = `
		INSERT INTO maclaw_resource_shadows (
			enterprise_user_id, enterprise_maclaw_tenant_id, source_expert_user_id, source_resource_id,
			source_version, shadow_resource_id, shadow_resource_handle, sync_status, last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, ''))
		ON CONFLICT (enterprise_user_id, source_resource_id, source_version) DO UPDATE SET
			enterprise_maclaw_tenant_id = EXCLUDED.enterprise_maclaw_tenant_id,
			source_expert_user_id = EXCLUDED.source_expert_user_id,
			shadow_resource_id = EXCLUDED.shadow_resource_id,
			shadow_resource_handle = EXCLUDED.shadow_resource_handle,
			sync_status = EXCLUDED.sync_status,
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`
	_, err := r.pool.Exec(ctx, query,
		item.EnterpriseUserID,
		item.EnterpriseMaclawTenantID,
		item.SourceExpertUserID,
		item.SourceResourceID,
		item.SourceVersion,
		item.ShadowResourceID,
		item.ShadowResourceHandle,
		item.SyncStatus,
		item.LastError,
	)
	if err != nil {
		return fmt.Errorf("upsert maclaw resource shadow: %w", err)
	}
	return nil
}

func (r *MaclawResourceShadowRepository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM maclaw_resource_shadows`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count maclaw resource shadows: %w", err)
	}
	return count, nil
}

func scanResourcePublications(rows pgx.Rows) ([]model.MaclawResourcePublication, error) {
	items := []model.MaclawResourcePublication{}
	for rows.Next() {
		var item model.MaclawResourcePublication
		var assessmentTypesRaw, tagsRaw, metadataRaw []byte
		if err := rows.Scan(
			&item.SourceExpertUserID,
			&item.SourceMaclawTenantID,
			&item.SourceResourceID,
			&item.SourceResourceHandle,
			&item.SourceVersion,
			&item.Name,
			&item.Kind,
			&item.Status,
			&item.Enabled,
			&item.Summary,
			&assessmentTypesRaw,
			&tagsRaw,
			&metadataRaw,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan maclaw resource publication: %w", err)
		}
		_ = json.Unmarshal(assessmentTypesRaw, &item.AssessmentTypes)
		_ = json.Unmarshal(tagsRaw, &item.Tags)
		_ = json.Unmarshal(metadataRaw, &item.Metadata)
		if item.Metadata == nil {
			item.Metadata = map[string]string{}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate maclaw resource publications: %w", err)
	}
	return items, nil
}
