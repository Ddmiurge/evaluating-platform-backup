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

type MaclawResourceRecordRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawResourceRecordRepository(pool *pgxpool.Pool) *MaclawResourceRecordRepository {
	return &MaclawResourceRecordRepository{pool: pool}
}

func (r *MaclawResourceRecordRepository) UpsertResource(ctx context.Context, record model.MaclawResourceRecord) (*model.MaclawResourceRecord, error) {
	if record.ID == uuid.Nil {
		record.ID = uuid.New()
	}
	assessmentTypes, err := json.Marshal(record.AssessmentTypes)
	if err != nil {
		return nil, fmt.Errorf("marshal assessment types: %w", err)
	}
	tags, err := json.Marshal(record.Tags)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}
	metadata, err := json.Marshal(record.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	const query = `
		INSERT INTO maclaw_resources (
			id, owner_user_id, owner_maclaw_tenant_id, handle, name, description, kind, version,
			status, enabled, health_status, assessment_types, tags, summary, encrypted_payload,
			payload_key_id, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb, $14, $15, $16, $17::jsonb)
		ON CONFLICT (id) DO UPDATE SET
			owner_maclaw_tenant_id = EXCLUDED.owner_maclaw_tenant_id,
			handle = EXCLUDED.handle,
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			kind = EXCLUDED.kind,
			version = EXCLUDED.version,
			status = EXCLUDED.status,
			enabled = EXCLUDED.enabled,
			health_status = EXCLUDED.health_status,
			assessment_types = EXCLUDED.assessment_types,
			tags = EXCLUDED.tags,
			summary = EXCLUDED.summary,
			encrypted_payload = EXCLUDED.encrypted_payload,
			payload_key_id = EXCLUDED.payload_key_id,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
		RETURNING id, owner_user_id, owner_maclaw_tenant_id, handle, name, description, kind, version,
		          status, enabled, health_status, assessment_types, tags, summary, encrypted_payload,
		          payload_key_id, metadata, created_at, updated_at
	`
	rows, err := r.pool.Query(ctx, query,
		record.ID,
		record.OwnerUserID,
		record.OwnerMaclawTenantID,
		record.Handle,
		record.Name,
		record.Description,
		record.Kind,
		record.Version,
		record.Status,
		record.Enabled,
		record.HealthStatus,
		string(assessmentTypes),
		string(tags),
		record.Summary,
		record.EncryptedPayload,
		record.PayloadKeyID,
		string(metadata),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert maclaw resource: %w", err)
	}
	defer rows.Close()
	items, err := scanMaclawResourceRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

func (r *MaclawResourceRecordRepository) ListResources(ctx context.Context, userID uuid.UUID, q maclaw.EvaluationResourceQuery) ([]model.MaclawResourceRecord, error) {
	conds := []string{"owner_user_id = $1"}
	args := []any{userID}
	if q.Kind != "" {
		args = append(args, string(q.Kind))
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if !q.IncludeInactive {
		conds = append(conds, "enabled = true", "status <> 'archived'")
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
		SELECT id, owner_user_id, owner_maclaw_tenant_id, handle, name, description, kind, version,
		       status, enabled, health_status, assessment_types, tags, summary, encrypted_payload,
		       payload_key_id, metadata, created_at, updated_at
		FROM maclaw_resources
		WHERE ` + strings.Join(conds, " AND ") + `
		ORDER BY updated_at DESC` + limitSQL
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list maclaw resources: %w", err)
	}
	defer rows.Close()
	return scanMaclawResourceRecords(rows)
}

func (r *MaclawResourceRecordRepository) GetResource(ctx context.Context, userID uuid.UUID, idOrHandle string) (*model.MaclawResourceRecord, error) {
	idOrHandle = strings.TrimSpace(idOrHandle)
	if idOrHandle == "" {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, owner_user_id, owner_maclaw_tenant_id, handle, name, description, kind, version,
		       status, enabled, health_status, assessment_types, tags, summary, encrypted_payload,
		       payload_key_id, metadata, created_at, updated_at
		FROM maclaw_resources
		WHERE owner_user_id = $1 AND (id::text = $2 OR handle = $2)
	`, userID, idOrHandle)
	if err != nil {
		return nil, fmt.Errorf("query maclaw resource: %w", err)
	}
	defer rows.Close()
	items, err := scanMaclawResourceRecords(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

// scanOneMaclawResourceRecord 扫描单行。返回 *CorruptRowError 表示该行
// 的关键列（assessment_types）损坏。
//
// P1-3（审计 2026-10-12）：此函数原先对三个 JSONB 列全部丢弃 Unmarshal
// 错误，其中两处还用了 `if err == nil` 保护而第三处没有 —— 风格不统一，
// 且被保护的那两处同样在静默零值化。本批次统一走
// decodeCriticalJSONB / decodeDegradableJSONB，不再有裸 `_ =`。
func scanOneMaclawResourceRecord(row rowsScanner) (*model.MaclawResourceRecord, error) {
	var item model.MaclawResourceRecord
	var assessmentTypesRaw, tagsRaw, metadataRaw []byte
	if err := row.Scan(
		&item.ID,
		&item.OwnerUserID,
		&item.OwnerMaclawTenantID,
		&item.Handle,
		&item.Name,
		&item.Description,
		&item.Kind,
		&item.Version,
		&item.Status,
		&item.Enabled,
		&item.HealthStatus,
		&assessmentTypesRaw,
		&tagsRaw,
		&item.Summary,
		&item.EncryptedPayload,
		&item.PayloadKeyID,
		&metadataRaw,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("scan maclaw resource: %w", err)
	}

	// 关键列：声明该资源适用哪些评测套件，损坏即 fail-closed。
	// 归一化：失败时用 item.ID.String() 定位（此时 ID 已由 Scan 填好）。
	if err := decodeCriticalJSONB("maclaw_resources", item.ID.String(), "assessment_types", assessmentTypesRaw, &item.AssessmentTypes); err != nil {
		return nil, err
	}
	// 展示性列：标签与自由键值，损坏降级并记日志。
	decodeDegradableJSONB("maclaw_resources", item.ID.String(), "tags", tagsRaw, &item.Tags)
	decodeDegradableJSONB("maclaw_resources", item.ID.String(), "metadata", metadataRaw, &item.Metadata)
	if item.Metadata == nil {
		item.Metadata = map[string]string{}
	}
	return &item, nil
}

// scanMaclawResourceRecords 逐行扫描资源列表。
//
// 与 scanEngineRunRows 同构：坏行跳过 + 记日志，好行返回，并把降级事实
// 作为 PartialCorruptionError 与结果并存地上抛（理由见 json_columns.go）。
func scanMaclawResourceRecords(rows rowsScanner) ([]model.MaclawResourceRecord, error) {
	items := []model.MaclawResourceRecord{}
	var corrupt []*CorruptRowError
	for rows.Next() {
		item, err := scanOneMaclawResourceRecord(rows)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil
			}
			var bad *CorruptRowError
			if errors.As(err, &bad) {
				logger.Error("skipping corrupt maclaw resource row", map[string]interface{}{
					"table":  bad.Table,
					"row":    bad.RowID,
					"column": bad.Field,
					"err":    bad.Err.Error(),
				})
				corrupt = append(corrupt, bad)
				continue
			}
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate maclaw resources: %w", err)
	}
	return items, newPartialCorruptionError("maclaw_resources", corrupt)
}
