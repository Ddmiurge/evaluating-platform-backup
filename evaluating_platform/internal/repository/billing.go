package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"evaluating_platform/internal/model"
)

// BillingRepository 计费数据访问层
type BillingRepository struct {
	pool *pgxpool.Pool
}

// NewBillingRepository 创建计费仓库
func NewBillingRepository(pool *pgxpool.Pool) *BillingRepository {
	return &BillingRepository{pool: pool}
}

// CreateRecord 写入单条计费记录
func (r *BillingRepository) CreateRecord(ctx context.Context, rec *model.BillingRecord) error {
	query := `
		INSERT INTO billing_records
			(id, user_id, assessment_id, tool_name, tool_category, call_count, tokens_used, amount,
			 asset_id, expert_id, expert_share)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`
	_, err := r.pool.Exec(ctx, query,
		rec.ID, rec.UserID, rec.AssessmentID, rec.ToolName, rec.ToolCategory,
		rec.CallCount, rec.TokensUsed, rec.Amount,
		rec.AssetID, rec.ExpertID, rec.ExpertShare,
	)
	if err != nil {
		return fmt.Errorf("insert billing_record: %w", err)
	}
	return nil
}

// ListByUser 分页查询用户的计费记录（按时间倒序）
func (r *BillingRepository) ListByUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]model.BillingRecord, int, error) {
	countQuery := `SELECT COUNT(*) FROM billing_records WHERE user_id = $1`
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count billing_records: %w", err)
	}

	query := `
		SELECT id, user_id, assessment_id, tool_name, tool_category, call_count, tokens_used, amount,
		       asset_id, expert_id, expert_share, created_at
		FROM billing_records
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.pool.Query(ctx, query, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query billing_records: %w", err)
	}
	defer rows.Close()

	var records []model.BillingRecord
	for rows.Next() {
		var rec model.BillingRecord
		if err := rows.Scan(
			&rec.ID, &rec.UserID, &rec.AssessmentID, &rec.ToolName, &rec.ToolCategory,
			&rec.CallCount, &rec.TokensUsed, &rec.Amount,
			&rec.AssetID, &rec.ExpertID, &rec.ExpertShare, &rec.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan billing_record: %w", err)
		}
		records = append(records, rec)
	}
	return records, total, nil
}
func (r *BillingRepository) ListTransactions(ctx context.Context, userID uuid.UUID, limit, offset int) ([]model.BalanceTransaction, int, error) {
	countQuery := `SELECT COUNT(*) FROM balance_transactions WHERE user_id = $1`
	var total int
	if err := r.pool.QueryRow(ctx, countQuery, userID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count transactions: %w", err)
	}

	query := `
		SELECT id, user_id, type, amount, balance_before, balance_after, description, created_at
		FROM balance_transactions
		WHERE user_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.pool.Query(ctx, query, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query transactions: %w", err)
	}
	defer rows.Close()

	var txns []model.BalanceTransaction
	for rows.Next() {
		var t model.BalanceTransaction
		if err := rows.Scan(
			&t.ID, &t.UserID, &t.Type, &t.Amount,
			&t.BalanceBefore, &t.BalanceAfter, &t.Description, &t.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan transaction: %w", err)
		}
		txns = append(txns, t)
	}
	return txns, total, nil
}

// ListEarnings 分页查询专家的收益明细（expert_share > 0 的计费记录，按时间倒序）
func (r *BillingRepository) ListEarnings(ctx context.Context, expertID uuid.UUID, limit, offset int) ([]model.BillingRecord, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM billing_records WHERE expert_id = $1 AND expert_share > 0`, expertID,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count earnings: %w", err)
	}

	query := `
		SELECT id, user_id, assessment_id, tool_name, tool_category, call_count, tokens_used, amount,
		       asset_id, expert_id, expert_share, created_at
		FROM billing_records
		WHERE expert_id = $1 AND expert_share > 0
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.pool.Query(ctx, query, expertID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query earnings: %w", err)
	}
	defer rows.Close()

	records := make([]model.BillingRecord, 0)
	for rows.Next() {
		var rec model.BillingRecord
		if err := rows.Scan(
			&rec.ID, &rec.UserID, &rec.AssessmentID, &rec.ToolName, &rec.ToolCategory,
			&rec.CallCount, &rec.TokensUsed, &rec.Amount,
			&rec.AssetID, &rec.ExpertID, &rec.ExpertShare, &rec.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan earning: %w", err)
		}
		records = append(records, rec)
	}
	return records, total, nil
}

// GetTotalEarnings 查询专家的累计收益总额
func (r *BillingRepository) GetTotalEarnings(ctx context.Context, expertID uuid.UUID) (float64, error) {
	var total float64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(expert_share), 0) FROM billing_records WHERE expert_id = $1`, expertID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum earnings: %w", err)
	}
	return total, nil
}

// SumTotalRevenue 统计平台总收入（所有计费记录 amount 之和）
func (r *BillingRepository) SumTotalRevenue(ctx context.Context) (float64, error) {
	var total float64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM billing_records`,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum revenue: %w", err)
	}
	return total, nil
}
