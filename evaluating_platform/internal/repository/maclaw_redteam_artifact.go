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
	"evaluating_platform/pkg/logger"
)

type MaclawRedteamArtifactRepository struct {
	pool *pgxpool.Pool
}

func NewMaclawRedteamArtifactRepository(pool *pgxpool.Pool) *MaclawRedteamArtifactRepository {
	return &MaclawRedteamArtifactRepository{pool: pool}
}

func (r *MaclawRedteamArtifactRepository) SaveEvidence(ctx context.Context, record maclaw.RedteamEvidenceRecord) (*maclaw.RedteamEvidenceRecord, error) {
	metadata, err := json.Marshal(record.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal redteam evidence metadata: %w", err)
	}
	const query = `
		INSERT INTO maclaw_redteam_evidence (
			id, handle, platform_user_id, instance_id, session_id, run_id, kind, title, summary, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE SET
			handle = EXCLUDED.handle,
			instance_id = EXCLUDED.instance_id,
			session_id = EXCLUDED.session_id,
			run_id = EXCLUDED.run_id,
			kind = EXCLUDED.kind,
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
		RETURNING id, handle, platform_user_id, instance_id, session_id, run_id, kind, title, summary, metadata, created_at, updated_at
	`
	row := r.pool.QueryRow(ctx, query,
		record.ID,
		record.Handle,
		record.PlatformUserID,
		record.InstanceID,
		record.SessionID,
		record.RunID,
		string(record.Kind),
		record.Title,
		record.Summary,
		metadata,
	)
	return scanRedteamEvidence(row)
}

func (r *MaclawRedteamArtifactRepository) ListEvidence(ctx context.Context, userID uuid.UUID, q maclaw.EvaluationEvidenceQuery) ([]maclaw.RedteamEvidenceRecord, error) {
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	const query = `
		SELECT id, handle, platform_user_id, instance_id, session_id, run_id, kind, title, summary, metadata, created_at, updated_at
		FROM maclaw_redteam_evidence
		WHERE platform_user_id = $1
		  AND ($2 = '' OR instance_id = $2)
		  AND ($3 = '' OR session_id = $3)
		  AND ($4 = '' OR run_id = $4)
		  AND ($5 = '' OR kind = $5)
		ORDER BY created_at DESC
		LIMIT $6
	`
	rows, err := r.pool.Query(ctx, query, userID, q.InstanceID, q.SessionID, q.RunID, string(q.Kind), limit)
	if err != nil {
		return nil, fmt.Errorf("query maclaw redteam evidence: %w", err)
	}
	defer rows.Close()
	// 列表读：坏行跳过 + 上抛可观测的降级错误（不因一条脏数据整体 500）。
	return scanRedteamRows(rows, "maclaw_redteam_evidence", scanRedteamEvidence)
}

func (r *MaclawRedteamArtifactRepository) GetEvidence(ctx context.Context, userID uuid.UUID, evidenceID string) (*maclaw.RedteamEvidenceRecord, error) {
	const query = `
		SELECT id, handle, platform_user_id, instance_id, session_id, run_id, kind, title, summary, metadata, created_at, updated_at
		FROM maclaw_redteam_evidence
		WHERE platform_user_id = $1 AND (id = $2 OR handle = $2)
	`
	out, err := scanRedteamEvidence(r.pool.QueryRow(ctx, query, userID, evidenceID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

func (r *MaclawRedteamArtifactRepository) SaveReport(ctx context.Context, record maclaw.RedteamReportRecord) (*maclaw.RedteamReportRecord, error) {
	// 写路径 fail-closed：报告的 findings 是用户最终看到的结论，
	// 编码失败时绝不能落库（沿用 json_columns.go 的写路径策略）。
	findings, err := json.Marshal(record.Findings)
	if err != nil {
		return nil, fmt.Errorf("marshal redteam report findings: %w", err)
	}
	evidenceHandles, err := json.Marshal(record.EvidenceHandles)
	if err != nil {
		return nil, fmt.Errorf("marshal redteam report evidence handles: %w", err)
	}
	metadata, err := json.Marshal(record.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal redteam report metadata: %w", err)
	}
	const query = `
		INSERT INTO maclaw_redteam_reports (
			id, handle, platform_user_id, instance_id, session_id, run_id, engine_run_id, title, summary, risk_level,
			safety_score, findings, evidence_handles, metadata
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT (id) DO UPDATE SET
			handle = EXCLUDED.handle,
			instance_id = EXCLUDED.instance_id,
			session_id = EXCLUDED.session_id,
			run_id = EXCLUDED.run_id,
			engine_run_id = EXCLUDED.engine_run_id,
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			risk_level = EXCLUDED.risk_level,
			safety_score = EXCLUDED.safety_score,
			findings = EXCLUDED.findings,
			evidence_handles = EXCLUDED.evidence_handles,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()
		RETURNING id, handle, platform_user_id, instance_id, session_id, run_id, engine_run_id, title, summary, risk_level,
			safety_score, findings, evidence_handles, metadata, created_at, updated_at
	`
	row := r.pool.QueryRow(ctx, query,
		record.ID,
		record.Handle,
		record.PlatformUserID,
		record.InstanceID,
		record.SessionID,
		record.RunID,
		record.EngineRunID,
		record.Title,
		record.Summary,
		record.RiskLevel,
		record.SafetyScore,
		findings,
		evidenceHandles,
		metadata,
	)
	return scanRedteamReport(row)
}

// GetReportByRunID returns the newest report for one run, owner-scoped.
//
// Ownership is part of the WHERE clause rather than a post-check: the caller is a
// run id from the browser, and a report belonging to another account must be
// indistinguishable from one that does not exist.
func (r *MaclawRedteamArtifactRepository) GetReportByRunID(ctx context.Context, userID uuid.UUID, runID string) (*maclaw.RedteamReportRecord, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil
	}
	// The engine run id is NOT in `run_id`: that column holds the planner's run
	// id (`msg_…` on the confirm fast path, `run_…` on the pre-P11 platform path).
	// Measured on ai02 2026-10-27: 0 of 72 rows had an `er-…` run_id, while 43/43
	// reports carried `metadata.engine_run_id` and all 43 joined a real
	// engine_runs row — the first version of this lookup matched on `run_id` and
	// therefore 404ed for every run.
	//
	// Both shapes are accepted while rows written before the dedicated column
	// existed are still in the table; new rows are written to `engine_run_id`.
	// The metadata expression is cost-bounded by the owner predicate and the
	// index created with the column.
	const query = `
		SELECT id, handle, platform_user_id, instance_id, session_id, run_id, engine_run_id, title, summary, risk_level,
			safety_score, findings, evidence_handles, metadata, created_at, updated_at
		FROM maclaw_redteam_reports
		WHERE platform_user_id = $1
		  AND (engine_run_id = $2 OR metadata->>'engine_run_id' = $2)
		ORDER BY created_at DESC
		LIMIT 1
	`
	out, err := scanRedteamReport(r.pool.QueryRow(ctx, query, userID, runID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query maclaw redteam report by run: %w", err)
	}
	return out, nil
}

func (r *MaclawRedteamArtifactRepository) GetReport(ctx context.Context, userID uuid.UUID, reportID string) (*maclaw.RedteamReportRecord, error) {
	const query = `
		SELECT id, handle, platform_user_id, instance_id, session_id, run_id, engine_run_id, title, summary, risk_level,
			safety_score, findings, evidence_handles, metadata, created_at, updated_at
		FROM maclaw_redteam_reports
		WHERE platform_user_id = $1 AND (id = $2 OR handle = $2)
	`
	out, err := scanRedteamReport(r.pool.QueryRow(ctx, query, userID, reportID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

// scanRedteamEvidence 扫描单条证据。metadata 为展示性列，损坏降级并记日志。
func scanRedteamEvidence(row rowScanner) (*maclaw.RedteamEvidenceRecord, error) {
	var out maclaw.RedteamEvidenceRecord
	var kind string
	var metadata []byte
	if err := row.Scan(
		&out.ID,
		&out.Handle,
		&out.PlatformUserID,
		&out.InstanceID,
		&out.SessionID,
		&out.RunID,
		&kind,
		&out.Title,
		&out.Summary,
		&metadata,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	out.Kind = maclaw.EvaluationEvidenceKind(kind)
	decodeDegradableJSONB("maclaw_redteam_evidence", out.Handle, "metadata", metadata, &out.Metadata)
	return &out, nil
}

// scanRedteamReport 扫描单份报告。
//
// 分级（判据见 json_columns.go；此处补充本层特有的理由）：
//   - findings → CRITICAL。这是用户最终读到的发现列表，且直接参与
//     redteam_artifact_service 的成功/失败计数（见其 summary 逻辑：
//     `success += findingLooksSuccessful(finding)`）。归零等于把
//     「3 个 critical 发现」变成「0 个发现、0 次成功」——用户读到的是
//     一份干净报告。这是全库最坏的一类静默腐败。
//   - evidence_handles → CRITICAL。证据句柄是结论到原始证据的链路；
//     归零后报告仍显示findings，但每条结论都无法追溯，用户无法验证。
//   - metadata → CRITICAL（**从 degradable 升级**）。这一列不是展示字段：
//     DD-5=A 把 judge_track 的**唯一真源**放在 report.metadata 里
//     （见 RedteamReportRecord.Metadata 注释与 redteam_judge_track.go）。
//     ResolveRedteamJudgeTrack 的判定顺序是 explicit → engine_metadata →
//     legacy_default，且 metadata 为空时**直接**返回 legacy_default+Platform。
//     也就是说 metadata 损坏归零，会让「引擎链路判定的报告」被静默降级成
//     「U4 之前的旧报告 / 平台判定」——判定口径的来源被改写，而报告照样
//     打得开。这比 findings 归零更隐蔽：findings 归零用户会看到空列表，
//     judge_track 归零用户只看到一个措辞不同但看起来完全正常的标签。
//
// ⚠️⚠️ 下一个维护者一定会在这里撞上一个看似矛盾的地方，请先读完这段：
//
//	本列**整列 JSON 解析失败** ≠ U4（redteam_judge_track.go）处理的
//	「judge_track 单个值非法」。**这是两个不同层面的问题，不是同一个冲突。**
//
//	U4 的容错设计处理的是：拿到一个**能解析的** metadata map，
//	其中 judge_track 的值非法 —— 此时记录 Invalid、退回 platform、
//	由渲染层 caption 明说「元数据非法」。U4 明确写了
//	「打不开比口径不明更糟」，所以那一层**不该动，本改动也没动它**。
//	（前提是 metadata 能解析 —— 那条路径的前提正是本函数守的东西。）
//
//	本函数面对的是另一件事：**metadata 这一列根本不是合法 JSON**，
//	整个 map 解不出来。此时 ResolveRedteamJudgeTrack 拿到的是**空 map**，
//	走它的第 4 条兜底：
//	    if len(metadata) == 0 {
//	        return {Track: RedteamJudgeTrackPlatform, Source: LegacyDefault}
//	    }
//	而 U4 之前引擎链路的报告是**真实存在**的（U4 是增量落地，不是从零起的），
//	所以一份 2026-10-10 之后的引擎报告会被静默标成 legacy_default、
//	标签变成「平台 Judge」—— **U4 的容错路径压根覆盖不到这里**，
//	因为它假设输入是可解析的。
//
//	结论：此处 fail-closed 是**补U4 的盲区**，不是违反 U4 的设计。
//	U4 的读路径容错（记录 Invalid + caption 提示）在 metadata 能正常解析时
//	仍然完全有效，未被本改动触碰。
func scanRedteamReport(row rowScanner) (*maclaw.RedteamReportRecord, error) {
	var out maclaw.RedteamReportRecord
	var findings, evidenceHandles, metadata []byte
	if err := row.Scan(
		&out.ID,
		&out.Handle,
		&out.PlatformUserID,
		&out.InstanceID,
		&out.SessionID,
		&out.RunID,
		&out.EngineRunID,
		&out.Title,
		&out.Summary,
		&out.RiskLevel,
		&out.SafetyScore,
		&findings,
		&evidenceHandles,
		&metadata,
		&out.CreatedAt,
		&out.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := decodeCriticalJSONB("maclaw_redteam_reports", out.Handle, "findings", findings, &out.Findings); err != nil {
		return nil, err
	}
	if err := decodeCriticalJSONB("maclaw_redteam_reports", out.Handle, "evidence_handles", evidenceHandles, &out.EvidenceHandles); err != nil {
		return nil, err
	}
	// judge_track 的唯一真源在此列，故与 findings 同为 CRITICAL。
	// 注意：此处拦的是「整列 JSON 损坏」，不是 U4 处理的「单值非法」——
	// 两者区别见上方那段⚠️ 注释，改动前请先读完，否则容易误以为这里
	// 覆盖了 U4 的容错路径（它没有），或反过来误把它降级回 degradable。
	if err := decodeCriticalJSONB("maclaw_redteam_reports", out.Handle, "metadata", metadata, &out.Metadata); err != nil {
		return nil, err
	}
	return &out, nil
}

// scanRedteamRows 是列表读共用的逐行扫描器。
//
// 用泛型而非各写一份：evidence 与 report 两条列表路径的降级语义必须
// **完全一致**，写两份就等于给自己留一个「下次只改了一半」的口子。
func scanRedteamRows[T any](rows rowsScanner, table string, scan func(rowScanner) (*T, error)) ([]T, error) {
	items := []T{}
	var corrupt []*CorruptRowError
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			var bad *CorruptRowError
			if errors.As(err, &bad) {
				logger.Error("skipping corrupt redteam row", map[string]interface{}{
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
		return nil, fmt.Errorf("iterate %s: %w", table, err)
	}
	return items, newPartialCorruptionError(table, corrupt)
}
