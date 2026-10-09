package billing

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"evaluating_platform/internal/model"
	"evaluating_platform/internal/repository"
)

// ToolCallInfo 工具调用信息（用于计费，与 agent 包解耦）
type ToolCallInfo struct {
	ToolName   string
	TokensUsed int
}

// Service 计费服务：负责余额检查、计费记录和扣费
type Service struct {
	userRepo    *repository.UserRepository
	billingRepo *repository.BillingRepository
}

// NewService 创建计费服务
func NewService(
	userRepo *repository.UserRepository,
	billingRepo *repository.BillingRepository,
) *Service {
	return &Service{
		userRepo:    userRepo,
		billingRepo: billingRepo,
	}
}
// GetBalance 获取用户当前余额
func (s *Service) GetBalance(ctx context.Context, userID uuid.UUID) (float64, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("get user balance: %w", err)
	}
	return user.Balance, nil
}
// GetBillingRecords 分页获取用户计费记录
func (s *Service) GetBillingRecords(ctx context.Context, userID uuid.UUID, limit, offset int) ([]model.BillingRecord, int, error) {
	return s.billingRepo.ListByUser(ctx, userID, limit, offset)
}

// GetTransactionHistory 分页获取用户余额变动记录
func (s *Service) GetTransactionHistory(ctx context.Context, userID uuid.UUID, limit, offset int) ([]model.BalanceTransaction, int, error) {
	return s.billingRepo.ListTransactions(ctx, userID, limit, offset)
}
// Recharge 给用户账户充值（正向余额变动）
func (s *Service) Recharge(ctx context.Context, userID uuid.UUID, amount float64, description string) error {
	if amount <= 0 {
		return fmt.Errorf("recharge amount must be positive")
	}
	return s.userRepo.UpdateBalance(ctx, userID, amount, description)
}

// GetExpertEarnings 分页查询专家的工具调用收益明细，同时返回累计总收益
func (s *Service) GetExpertEarnings(ctx context.Context, expertID uuid.UUID, limit, offset int) ([]model.BillingRecord, int, float64, error) {
	records, total, err := s.billingRepo.ListEarnings(ctx, expertID, limit, offset)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list earnings: %w", err)
	}
	totalEarnings, err := s.billingRepo.GetTotalEarnings(ctx, expertID)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("sum earnings: %w", err)
	}
	return records, total, totalEarnings, nil
}
