package repository

// engine_run.go — DAO for engine_runs (Phase 0; §9.1 table 1).
//
// Safety: only safe progress metadata and the redacted aggregate result are
// persisted. Prompts, target responses, credentials, and engine-local paths
// never enter this table.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/maclaw"
)

type EngineRunRepository struct {
	pool *pgxpool.Pool
}

func NewEngineRunRepository(pool *pgxpool.Pool) *EngineRunRepository {
	return &EngineRunRepository{pool: pool}
}

const engineRunColumns = `
	id, platform_user_id, instance_id, session_id, source, engine, status, judge_mode,
	purpose, plugins, strategies, target_id, num_tests, planned_count, executed_count,
	current_stage, duration_ms, stage_durations, token_usage, result,
	error_code, error_brief, started_at, completed_at, created_at, updated_at
`

// marshalEngineRunBlobs 与 engineRunWriteArgs 提取自 Create/Update 的
// 重复 marshal 前奏与 QueryRow 实参块（P2-11）。
func marshalEngineRunBlobs(record maclaw.EngineRunRecord) (plugins, strategies, stageDurations, tokenUsage, result []byte) {
	plugins, _ = json.Marshal(record.Plugins)
	strategies, _ = json.Marshal(record.Strategies)
	stageDurations, _ = json.Marshal(record.StageDurations)
	tokenUsage, _ = json.Marshal(record.TokenUsage)
	result = []byte("{}")
	if record.Result != nil {
		result, _ = json.Marshal(record.Result)
	}
	return plugins, strategies, stageDurations, tokenUsage, result
}

func engineRunWriteArgs(record maclaw.EngineRunRecord, plugins, strategies, stageDurations, tokenUsage, result []byte) []any {
	return []any{
		record.ID,
		record.PlatformUserID,
		record.InstanceID,
		record.SessionID,
		record.Source,
		record.Engine,
		record.Status,
		string(record.JudgeMode),
		record.Purpose,
		plugins,
		strategies,
		record.TargetID,
		record.NumTests,
		record.PlannedCount,
		record.ExecutedCount,
		record.CurrentStage,
		record.DurationMs,
		stageDurations,
		tokenUsage,
		result,
		record.ErrorCode,
		record.ErrorBrief,
		record.StartedAt,
		record.CompletedAt,
	}
}

func (r *EngineRunRepository) Create(ctx context.Context, record maclaw.EngineRunRecord) (*maclaw.EngineRunRecord, error) {
	plugins, strategies, stageDurations, tokenUsage, result := marshalEngineRunBlobs(record)
	const query = `
		INSERT INTO engine_runs (
			id, platform_user_id, instance_id, session_id, source, engine, status, judge_mode,
			purpose, plugins, strategies, target_id, num_tests, planned_count, executed_count,
			current_stage, duration_ms, stage_durations, token_usage, result,
			error_code, error_brief, started_at, completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
		RETURNING ` + engineRunColumns
	row := r.pool.QueryRow(ctx, query,
		engineRunWriteArgs(record, plugins, strategies, stageDurations, tokenUsage, result)...,
)
	return scanEngineRun(row)
}

func (r *EngineRunRepository) Update(ctx context.Context, record maclaw.EngineRunRecord) (*maclaw.EngineRunRecord, error) {
	plugins, strategies, stageDurations, tokenUsage, result := marshalEngineRunBlobs(record)
	const query = `
		UPDATE engine_runs SET
			instance_id = $3,
			session_id = $4,
			source = $5,
			engine = $6,
			status = $7,
			judge_mode = $8,
			purpose = $9,
			plugins = $10,
			strategies = $11,
			target_id = $12,
			num_tests = $13,
			planned_count = $14,
			executed_count = $15,
			current_stage = $16,
			duration_ms = $17,
			stage_durations = $18,
			token_usage = $19,
			result = $20,
			error_code = $21,
			error_brief = $22,
			started_at = $23,
			completed_at = $24,
			updated_at = NOW()
		WHERE id = $1 AND platform_user_id = $2
		RETURNING ` + engineRunColumns
	row := r.pool.QueryRow(ctx, query,
		engineRunWriteArgs(record, plugins, strategies, stageDurations, tokenUsage, result)...,
)
	return scanEngineRun(row)
}

func (r *EngineRunRepository) Get(ctx context.Context, userID uuid.UUID, runID string) (*maclaw.EngineRunRecord, error) {
	const query = `SELECT ` + engineRunColumns + `
		FROM engine_runs
		WHERE platform_user_id = $1 AND id = $2`
	out, err := scanEngineRun(r.pool.QueryRow(ctx, query, userID, runID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

func (r *EngineRunRepository) List(ctx context.Context, userID uuid.UUID, sessionID string, limit int) ([]maclaw.EngineRunRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	const query = `SELECT ` + engineRunColumns + `
		FROM engine_runs
		WHERE platform_user_id = $1 AND ($2 = '' OR session_id = $2)
		ORDER BY created_at DESC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, query, userID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("query engine runs: %w", err)
	}
	defer rows.Close()
	items := []maclaw.EngineRunRecord{}
	for rows.Next() {
		item, err := scanEngineRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func scanEngineRun(row rowScanner) (*maclaw.EngineRunRecord, error) {
	var out maclaw.EngineRunRecord
	var judgeMode string
	var plugins, strategies, stageDurations, tokenUsage, result []byte
	if err := row.Scan(
		&out.ID,
		&out.PlatformUserID,
		&out.InstanceID,
		&out.SessionID,
		&out.Source,
		&out.Engine,
		&out.Status,
		&judgeMode,
		&out.Purpose,
		&plugins,
		&strategies,
		&out.TargetID,
		&out.NumTests,
		&out.PlannedCount,
		&out.ExecutedCount,
		&out.CurrentStage,
		&out.DurationMs,
		&stageDurations,
		&tokenUsage,
		&result,
		&out.ErrorCode,
		&out.ErrorBrief,
		&out.StartedAt,
		&out.CompletedAt,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	out.JudgeMode = maclaw.EngineJudgeMode(judgeMode)
	_ = json.Unmarshal(plugins, &out.Plugins)
	_ = json.Unmarshal(strategies, &out.Strategies)
	_ = json.Unmarshal(stageDurations, &out.StageDurations)
	_ = json.Unmarshal(tokenUsage, &out.TokenUsage)
	if len(result) > 0 && string(result) != "{}" {
		var safe maclaw.EngineSafeResult
		if err := json.Unmarshal(result, &safe); err == nil {
			out.Result = &safe
		}
	}
	return &out, nil
}
