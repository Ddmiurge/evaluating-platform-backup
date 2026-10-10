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
	id, platform_user_id, instance_id, session_id, source, job_id, engine, status, judge_mode,
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

// engineRunWriteArgs builds the positional argument list shared by Create and
// Update. Column order must stay in lockstep with the INSERT/UPDATE statements
// below and with engineRunWriteColumns.
//
// Safety: every value here is safe progress metadata. job_id is a BFF-minted
// `pfj-<uuid>` identifier (format-constrained by both the service layer and
// the 027 CHECK constraint) — never user content.
func engineRunWriteArgs(record maclaw.EngineRunRecord, plugins, strategies, stageDurations, tokenUsage, result []byte) []any {
	return []any{
		record.ID,
		record.PlatformUserID,
		record.InstanceID,
		record.SessionID,
		record.Source,
		record.JobID,
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

// engineRunWriteColumns is the INSERT column list matching
// engineRunWriteArgs order exactly.
const engineRunWriteColumns = `
	id, platform_user_id, instance_id, session_id, source, job_id, engine, status, judge_mode,
	purpose, plugins, strategies, target_id, num_tests, planned_count, executed_count,
	current_stage, duration_ms, stage_durations, token_usage, result,
	error_code, error_brief, started_at, completed_at
`

// engineRunWritePlaceholders is the $n list matching engineRunWriteColumns.
const engineRunWritePlaceholders = `$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25`

func (r *EngineRunRepository) Create(ctx context.Context, record maclaw.EngineRunRecord) (*maclaw.EngineRunRecord, error) {
	plugins, strategies, stageDurations, tokenUsage, result := marshalEngineRunBlobs(record)
	query := `
		INSERT INTO engine_runs (` + engineRunWriteColumns + `)
		VALUES (` + engineRunWritePlaceholders + `)
		RETURNING ` + engineRunColumns
	row := r.pool.QueryRow(ctx, query,
		engineRunWriteArgs(record, plugins, strategies, stageDurations, tokenUsage, result)...,
	)
	return scanEngineRun(row)
}

func (r *EngineRunRepository) Update(ctx context.Context, record maclaw.EngineRunRecord) (*maclaw.EngineRunRecord, error) {
	plugins, strategies, stageDurations, tokenUsage, result := marshalEngineRunBlobs(record)
	// job_id is written "sticky": a progress update must never erase the
	// pfj- ↔ run binding.
	//
	// Why this is load-bearing (U5): the background wait loop keeps its own
	// copy of the record from Prepare time, whose JobID is still "". Saving
	// that copy with a plain `job_id = $6` would blank the link on the very
	// first progress tick, so a restart one minute into a run could no longer
	// recover it — exactly the silent loss U5 exists to prevent. The link is
	// therefore only ever established by LinkJob and never cleared here.
	//
	// `source` is kept consistent with the link so the
	// engine_runs_job_source_check invariant (job_id = '' OR source =
	// 'chat_confirm') holds for every write path, not just LinkJob.
	query := `
		UPDATE engine_runs SET
			instance_id = $3,
			session_id = $4,
			source = CASE
				WHEN COALESCE(NULLIF($6, ''), engine_runs.job_id) <> '' THEN 'chat_confirm'
				ELSE $5
			END,
			job_id = COALESCE(NULLIF($6, ''), engine_runs.job_id),
			engine = $7,
			status = $8,
			judge_mode = $9,
			purpose = $10,
			plugins = $11,
			strategies = $12,
			target_id = $13,
			num_tests = $14,
			planned_count = $15,
			executed_count = $16,
			current_stage = $17,
			duration_ms = $18,
			stage_durations = $19,
			token_usage = $20,
			result = $21,
			error_code = $22,
			error_brief = $23,
			started_at = $24,
			completed_at = $25,
			updated_at = NOW()
		WHERE id = $1 AND platform_user_id = $2
		RETURNING ` + engineRunColumns
	row := r.pool.QueryRow(ctx, query,
		engineRunWriteArgs(record, plugins, strategies, stageDurations, tokenUsage, result)...,
	)
	return scanEngineRun(row)
}

// LinkJob binds a `pfj-` job id to a run, idempotently (U5).
//
// The guard `(job_id = ” OR job_id = $4)` is the whole point:
//   - First link writes the value.
//   - Every later link of the same (run, job) pair matches the guard but
//     writes the identical value → net no-op, row count stays 1.
//   - A job id already bound to a *different* run fails the guard and the
//     unique index (idx_engine_runs_job_id) rejects it too, so one browser
//     job can never come to point at two runs.
//
// Because the write is an UPDATE and never an INSERT, a replayed link after a
// restart cannot create a second engine_runs row.
func (r *EngineRunRepository) LinkJob(ctx context.Context, userID uuid.UUID, runID, jobID string) (*maclaw.EngineRunRecord, error) {
	const query = `
		UPDATE engine_runs SET
			job_id = $4,
			source = 'chat_confirm',
			updated_at = NOW()
		WHERE id = $1
		  AND platform_user_id = $2
		  AND (job_id = '' OR job_id = $4)
		RETURNING ` + engineRunColumns
	record, err := scanEngineRun(r.pool.QueryRow(ctx, query, runID, userID, jobID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the run does not exist, does not belong to this user, or
			// the job id is bound elsewhere. All three are caller-visible
			// failures, never silent successes.
			return nil, fmt.Errorf("link engine job: run %s not found or job %s already bound to another run", runID, jobID)
		}
		return nil, fmt.Errorf("link engine job: %w", err)
	}
	return record, nil
}

// GetByJobID resolves the run behind a `pfj-` job id. This is the restart
// recovery path: after a BFF restart the in-memory job store is empty, but the
// browser still polls with the job id it received, and PostgreSQL still holds
// the binding written by LinkJob.
func (r *EngineRunRepository) GetByJobID(ctx context.Context, userID uuid.UUID, jobID string) (*maclaw.EngineRunRecord, error) {
	const query = `SELECT ` + engineRunColumns + `
		FROM engine_runs
		WHERE platform_user_id = $1 AND job_id = $2
		LIMIT 1`
	out, err := scanEngineRun(r.pool.QueryRow(ctx, query, userID, jobID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query engine run by job id: %w", err)
	}
	return out, nil
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
		&out.JobID,
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
