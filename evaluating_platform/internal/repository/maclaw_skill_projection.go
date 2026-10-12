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
	"evaluating_platform/pkg/logger"
)

type MaclawSkillPublicationRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawSkillPublicationRepository(pool *pgxpool.Pool) *MaclawSkillPublicationRepository {
	return &MaclawSkillPublicationRepository{pool: pool}
}

func (r *MaclawSkillPublicationRepository) UpsertSkillPublication(ctx context.Context, item *model.MaclawSkillPublication) error {
	triggers, err := json.Marshal(item.Triggers)
	if err != nil {
		return fmt.Errorf("marshal triggers: %w", err)
	}
	tags, err := json.Marshal(item.Tags)
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}
	assessmentTypes, err := json.Marshal(item.AssessmentTypes)
	if err != nil {
		return fmt.Errorf("marshal assessment types: %w", err)
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	const query = `
		INSERT INTO maclaw_skill_publications (
			source_expert_user_id, source_maclaw_tenant_id, source_skill_name, source_version,
			name, description, status, enabled, triggers, tags, assessment_types, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12::jsonb)
		ON CONFLICT (source_expert_user_id, source_skill_name, source_version) DO UPDATE SET
			source_maclaw_tenant_id = EXCLUDED.source_maclaw_tenant_id,
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			status = EXCLUDED.status,
			enabled = EXCLUDED.enabled,
			triggers = EXCLUDED.triggers,
			tags = EXCLUDED.tags,
			assessment_types = EXCLUDED.assessment_types,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
	`
	_, err = r.pool.Exec(ctx, query,
		item.SourceExpertUserID,
		item.SourceMaclawTenantID,
		strings.TrimSpace(item.SourceSkillName),
		normalizeSkillVersionForStore(item.SourceVersion),
		item.Name,
		item.Description,
		item.Status,
		item.Enabled,
		string(triggers),
		string(tags),
		string(assessmentTypes),
		string(metadata),
	)
	if err != nil {
		return fmt.Errorf("upsert maclaw skill publication: %w", err)
	}
	return nil
}

func (r *MaclawSkillPublicationRepository) MarkSkillPublicationUnavailable(ctx context.Context, sourceExpertUserID uuid.UUID, sourceSkillName string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE maclaw_skill_publications
		SET enabled = false, status = 'disabled', updated_at = NOW()
		WHERE source_expert_user_id = $1 AND source_skill_name = $2
	`, sourceExpertUserID, strings.TrimSpace(sourceSkillName))
	if err != nil {
		return fmt.Errorf("mark maclaw skill publication unavailable: %w", err)
	}
	return nil
}

func (r *MaclawSkillPublicationRepository) ListPublishedSkills(ctx context.Context, q maclaw.SkillSearchInput) ([]model.MaclawSkillPublication, error) {
	conds := []string{
		"p.enabled = true",
		"p.status IN ('active', 'published', 'enabled')",
		"u.is_active = true",
		"u.email NOT LIKE 'deleted-%@deleted.local'",
	}
	args := []any{}
	for _, token := range strings.Fields(strings.TrimSpace(q.Query)) {
		args = append(args, "%"+token+"%")
		conds = append(conds, fmt.Sprintf("(p.name ILIKE $%d OR p.source_skill_name ILIKE $%d OR p.description ILIKE $%d OR p.triggers::text ILIKE $%d OR p.tags::text ILIKE $%d)", len(args), len(args), len(args), len(args), len(args)))
	}
	limitSQL := ""
	if q.TopN > 0 {
		args = append(args, q.TopN)
		limitSQL = fmt.Sprintf(" LIMIT $%d", len(args))
	}
	query := `
		WITH ranked AS (
			SELECT p.source_expert_user_id, p.source_maclaw_tenant_id, p.source_skill_name, p.source_version,
			       p.name, p.description, p.status, p.enabled, p.triggers, p.tags, p.assessment_types, p.metadata,
			       p.created_at, p.updated_at,
			       ROW_NUMBER() OVER (
			           PARTITION BY p.source_expert_user_id, p.source_skill_name
			           ORDER BY p.updated_at DESC, p.created_at DESC
			       ) AS version_rank
			FROM maclaw_skill_publications p
			JOIN users u ON u.id = p.source_expert_user_id
			WHERE ` + strings.Join(conds, " AND ") + `
		)
		SELECT source_expert_user_id, source_maclaw_tenant_id, source_skill_name, source_version,
		       name, description, status, enabled, triggers, tags, assessment_types, metadata,
		       created_at, updated_at
		FROM ranked
		WHERE version_rank = 1
		ORDER BY updated_at DESC` + limitSQL
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list maclaw skill publications: %w", err)
	}
	defer rows.Close()
	return scanSkillPublications(rows)
}

type MaclawSkillShadowRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawSkillShadowRepository(pool *pgxpool.Pool) *MaclawSkillShadowRepository {
	return &MaclawSkillShadowRepository{pool: pool}
}

func (r *MaclawSkillShadowRepository) GetSkillShadow(ctx context.Context, enterpriseUserID uuid.UUID, sourceExpertUserID uuid.UUID, sourceSkillName, sourceVersion string) (*model.MaclawSkillShadow, error) {
	const query = `
		SELECT enterprise_user_id, enterprise_maclaw_tenant_id, source_expert_user_id, source_skill_name,
		       source_version, shadow_skill_name, sync_status, COALESCE(last_error, ''),
		       created_at, updated_at
		FROM maclaw_skill_shadows
		WHERE enterprise_user_id = $1 AND source_expert_user_id = $2 AND source_skill_name = $3 AND source_version = $4
	`
	var out model.MaclawSkillShadow
	err := r.pool.QueryRow(ctx, query, enterpriseUserID, sourceExpertUserID, strings.TrimSpace(sourceSkillName), normalizeSkillVersionForStore(sourceVersion)).Scan(
		&out.EnterpriseUserID,
		&out.EnterpriseMaclawTenantID,
		&out.SourceExpertUserID,
		&out.SourceSkillName,
		&out.SourceVersion,
		&out.ShadowSkillName,
		&out.SyncStatus,
		&out.LastError,
		&out.CreatedAt,
		&out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query maclaw skill shadow: %w", err)
	}
	return &out, nil
}

func (r *MaclawSkillShadowRepository) UpsertSkillShadow(ctx context.Context, item *model.MaclawSkillShadow) error {
	const query = `
		INSERT INTO maclaw_skill_shadows (
			enterprise_user_id, enterprise_maclaw_tenant_id, source_expert_user_id, source_skill_name,
			source_version, shadow_skill_name, sync_status, last_error
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''))
		ON CONFLICT (enterprise_user_id, source_expert_user_id, source_skill_name, source_version) DO UPDATE SET
			enterprise_maclaw_tenant_id = EXCLUDED.enterprise_maclaw_tenant_id,
			shadow_skill_name = EXCLUDED.shadow_skill_name,
			sync_status = EXCLUDED.sync_status,
			last_error = EXCLUDED.last_error,
			updated_at = NOW()
	`
	_, err := r.pool.Exec(ctx, query,
		item.EnterpriseUserID,
		item.EnterpriseMaclawTenantID,
		item.SourceExpertUserID,
		strings.TrimSpace(item.SourceSkillName),
		normalizeSkillVersionForStore(item.SourceVersion),
		item.ShadowSkillName,
		item.SyncStatus,
		item.LastError,
	)
	if err != nil {
		return fmt.Errorf("upsert maclaw skill shadow: %w", err)
	}
	return nil
}

func normalizeSkillVersionForStore(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "default"
	}
	return version
}

// scanSkillPublications 逐行扫描技能发布记录。
//
// 分级（判据见 json_columns.go）：
//   - assessment_types → CRITICAL。经 capability_catalog.go:314 变成能力
//     卡片的 RiskTypes，归零 = 向能力目录误报「不覆盖任何风险类型」。
//   - triggers → CRITICAL。经 capability_catalog.go:317 变成 UseWhen，
//     即**技能选用条件**（"什么时候该用这个技能"）。归零会让红队智能体
//     在该技能该出手时不出手，或反之——这是选择逻辑被静默改写，
//     比标签丢失严重得多。
//   - tags / metadata → degradable，但 metadata 是 risk_types /
//     target_types / languages / use_when 的回退来源（capability_catalog.go:315-317），
//     归零会静默丢掉能力标注，因此记 ERROR 而非无声放过。
//
// 结构与 scanResourcePublications 同构。
func scanSkillPublications(rows rowsScanner) ([]model.MaclawSkillPublication, error) {
	items := []model.MaclawSkillPublication{}
	var corrupt []*CorruptRowError
	for rows.Next() {
		var item model.MaclawSkillPublication
		var triggersRaw, tagsRaw, assessmentTypesRaw, metadataRaw []byte
		if err := rows.Scan(
			&item.SourceExpertUserID,
			&item.SourceMaclawTenantID,
			&item.SourceSkillName,
			&item.SourceVersion,
			&item.Name,
			&item.Description,
			&item.Status,
			&item.Enabled,
			&triggersRaw,
			&tagsRaw,
			&assessmentTypesRaw,
			&metadataRaw,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan maclaw skill publication: %w", err)
		}
		rowID := item.SourceSkillName
		if err := decodeCriticalJSONB("maclaw_skill_publications", rowID, "assessment_types", assessmentTypesRaw, &item.AssessmentTypes); err != nil {
			logger.Error("skipping corrupt maclaw skill publication", map[string]interface{}{
				"table":  "maclaw_skill_publications",
				"row":    rowID,
				"column": "assessment_types",
				"err":    err.Error(),
			})
			var bad *CorruptRowError
			if errors.As(err, &bad) {
				corrupt = append(corrupt, bad)
				continue
			}
			return nil, err
		}
		if err := decodeCriticalJSONB("maclaw_skill_publications", rowID, "triggers", triggersRaw, &item.Triggers); err != nil {
			logger.Error("skipping corrupt maclaw skill publication", map[string]interface{}{
				"table":  "maclaw_skill_publications",
				"row":    rowID,
				"column": "triggers",
				"err":    err.Error(),
			})
			var bad *CorruptRowError
			if errors.As(err, &bad) {
				corrupt = append(corrupt, bad)
				continue
			}
			return nil, err
		}
		decodeDegradableJSONB("maclaw_skill_publications", rowID, "tags", tagsRaw, &item.Tags)
		decodeDegradableJSONB("maclaw_skill_publications", rowID, "metadata", metadataRaw, &item.Metadata)
		if item.Metadata == nil {
			item.Metadata = map[string]string{}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate maclaw skill publications: %w", err)
	}
	return items, newPartialCorruptionError("maclaw_skill_publications", corrupt)
}
