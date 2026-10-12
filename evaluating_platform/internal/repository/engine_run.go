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
	"evaluating_platform/pkg/logger"
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

// engineRunBlobs 是 engine_runs 五个 JSONB 列的写入形态。
type engineRunBlobs struct {
	plugins        []byte
	strategies     []byte
	stageDurations []byte
	tokenUsage     []byte
	result         []byte
}

// marshalEngineRunBlobs 把记录的可变字段编码成五个 JSONB 列。
//
// P1-1（审计 2026-10-12）：此函数原先对五次json.Marshal 全部丢弃错误
// （`plugins, _ = json.Marshal(...)`）。marshal 失败时会把 nil 或截断的
// 字节写进 DB，而调用处**不知情** —— 写出一行脏数据，却拿到一个
// 成功的返回。写路径因此改为 fail-closed：任一列编码失败即返回错误，
// 绝不落库。
//
// 为什么写路径必须 fail-closed 而读路径不是：写路径是我们**唯一能阻止**
// 脏数据产生的地方（脏数据一旦落库，约束就转移到所有未来读者身上）。
// 读路径面对的是**已经存在**的历史脏数据（见 json_columns.go），在那里
// 拒绝服务比丢一个字段危害更大。
func marshalEngineRunBlobs(record maclaw.EngineRunRecord) (engineRunBlobs, error) {
	var blobs engineRunBlobs

	plugins, err := json.Marshal(record.Plugins)
	if err != nil {
		return engineRunBlobs{}, fmt.Errorf("marshal engine run plugins: %w", err)
	}
	blobs.plugins = plugins

	strategies, err := json.Marshal(record.Strategies)
	if err != nil {
		return engineRunBlobs{}, fmt.Errorf("marshal engine run strategies: %w", err)
	}
	blobs.strategies = strategies

	stageDurations, err := json.Marshal(record.StageDurations)
	if err != nil {
		return engineRunBlobs{}, fmt.Errorf("marshal engine run stage durations: %w", err)
	}
	blobs.stageDurations = stageDurations

	tokenUsage, err := json.Marshal(record.TokenUsage)
	if err != nil {
		return engineRunBlobs{}, fmt.Errorf("marshal engine run token usage: %w", err)
	}
	blobs.tokenUsage = tokenUsage

	// result列是 NOT NULL DEFAULT '{}'，所以 nil 必须显式写成空对象，
	// 否则会触发 NOT NULL 违约。保持既有语义，只是把错误交了出来。
	blobs.result = []byte("{}")
	if record.Result != nil {
		result, err := json.Marshal(record.Result)
		if err != nil {
			return engineRunBlobs{}, fmt.Errorf("marshal engine run result: %w", err)
		}
		blobs.result = result
	}

	return blobs, nil
}

// engineRunWriteArgs builds the positional argument list shared by Create and
// Update. Column order must stay in lockstep with the INSERT/UPDATE statements
// below and with engineRunWriteColumns.
//
// Safety: every value here is safe progress metadata. job_id is a BFF-minted
// `pfj-<uuid>` identifier (format-constrained by both the service layer and
// the 027 CHECK constraint) — never user content.
func engineRunWriteArgs(record maclaw.EngineRunRecord, blobs engineRunBlobs) []any {
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
		blobs.plugins,
		blobs.strategies,
		record.TargetID,
		record.NumTests,
		record.PlannedCount,
		record.ExecutedCount,
		record.CurrentStage,
		record.DurationMs,
		blobs.stageDurations,
		blobs.tokenUsage,
		blobs.result,
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
	// Fail-closed：编码失败时不发SQL，避免把一行坏JSONB 写进 DB。
	blobs, err := marshalEngineRunBlobs(record)
	if err != nil {
		return nil, fmt.Errorf("create engine run: %w", err)
	}
	query := `
		INSERT INTO engine_runs (` + engineRunWriteColumns + `)
		VALUES (` + engineRunWritePlaceholders + `)
		RETURNING ` + engineRunColumns
	row := r.pool.QueryRow(ctx, query, engineRunWriteArgs(record, blobs)...)
	return scanEngineRun(row)
}

func (r *EngineRunRepository) Update(ctx context.Context, record maclaw.EngineRunRecord) (*maclaw.EngineRunRecord, error) {
	// Fail-closed：同 Create，编码失败时不发 SQL。
	blobs, err := marshalEngineRunBlobs(record)
	if err != nil {
		return nil, fmt.Errorf("update engine run: %w", err)
	}
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
	row := r.pool.QueryRow(ctx, query, engineRunWriteArgs(record, blobs)...)
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

// List 返回调用者的 run 列表（仅安全字段）。
//
// P1-2 读路径设计判断（本批次的核心权衡）：DB 里可能**已经有**损坏的历史
// 数据（本项目当前就跑着实例）。所以这里刻意**不**因为一行脏数据就让整个
// 列表 500 —— 一次坏数据会让用户的整个 run 历史页打不开，代价远大于
// 少显示一行。
//
// 具体做法：
//   - 逐行扫描，坏行被跳过并记录定位信息（表/行/列）；
//   - 好行照常返回；
//   - 若确实跳过了行，返回 PartialCorruptionError **与好行并存**。
//     调用方用 repository.IsPartialCorruption 判定：记日志后仍返回 200，
//     从而「向上传递可观测的错误」与「不让一条脏数据整体崩掉」两个目标
//     同时成立。
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
	return scanEngineRunRows(rows)
}

// scanEngineRunRows 是 List 的逐行实现，抽出成独立函数以便用 fake 在无 PG
// 的环境下测试「一行坏了会怎样」（护栏要求打生产出口，List 通过它到达
// scanEngineRun，与真库路径完全一致）。
func scanEngineRunRows(rows rowsScanner) ([]maclaw.EngineRunRecord, error) {
	items := []maclaw.EngineRunRecord{}
	var corrupt []*CorruptRowError
	for rows.Next() {
		item, err := scanEngineRun(rows)
		if err != nil {
			var bad *CorruptRowError
			if errors.As(err, &bad) {
				// 坏行：跳过 + 记日志，不中断整批。
				logger.Error("skipping corrupt engine run row", map[string]interface{}{
					"table":  bad.Table,
					"row":    bad.RowID,
					"column": bad.Field,
					"err":    bad.Err.Error(),
				})
				corrupt = append(corrupt, bad)
				continue
			}
			// 非损坏类错误（连接中断、Scan 失败等）：整体失败。
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate engine runs: %w", err)
	}
	return items, newPartialCorruptionError("engine_runs", corrupt)
}

// scanEngineRun 把一行 engine_runs 映射成领域记录。
//
// P1-2（审计 2026-10-12）：此函数原先对四个 JSONB 列全部丢弃 Unmarshal
// 错误。逻辑推演：非法 JSON → Unmarshal 返回 error → 被丢弃 → 目标字段
// 保持零值，函数照常返回 nil error。**调用方拿到的是「字段为空的正常
// 记录」，完全无法察觉。**
//
// 现在按 json_columns.go 的分类处理：
//   - plugins / strategies / result（关键）：失败即返回 *CorruptRowError。
//     这三个字段任一为空都会把「我们测过什么/测出了什么」变成一句假话。
//   - stage_durations / token_usage（展示性）：降级为零值并记 ERROR 日志。
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

	// 关键列：fail-closed。out.ID 此时已由 Scan 填好，可直接用于定位。
	if err := decodeCriticalJSONB("engine_runs", out.ID, "plugins", plugins, &out.Plugins); err != nil {
		return nil, err
	}
	if err := decodeCriticalJSONB("engine_runs", out.ID, "strategies", strategies, &out.Strategies); err != nil {
		return nil, err
	}
	// result 列是 NOT NULL DEFAULT '{}'；'{}' 与空都表示「还没有结果」。
	if len(result) > 0 && string(result) != "{}" {
		var safe maclaw.EngineSafeResult
		if err := decodeCriticalJSONB("engine_runs", out.ID, "result", result, &safe); err != nil {
			return nil, err
		}
		out.Result = &safe
	}

	// 展示性列：降级 + 记日志。
	decodeDegradableJSONB("engine_runs", out.ID, "stage_durations", stageDurations, &out.StageDurations)
	decodeDegradableJSONB("engine_runs", out.ID, "token_usage", tokenUsage, &out.TokenUsage)

	return &out, nil
}
