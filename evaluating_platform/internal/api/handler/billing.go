package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"evaluating_platform/internal/billing"
	"evaluating_platform/pkg/logger"
)

// BillingHandler 计费相关 API 处理器
type BillingHandler struct {
	billingService *billing.Service
}

// NewBillingHandler 创建计费处理器
func NewBillingHandler(billingService *billing.Service) *BillingHandler {
	return &BillingHandler{billingService: billingService}
}

// GetBalance 查询当前用户余额
// GET /api/v1/billing/balance
func (h *BillingHandler) GetBalance(c *gin.Context) {
	userID, _ := uuid.Parse(c.GetString("user_id"))

	balance, err := h.billingService.GetBalance(c.Request.Context(), userID)
	if err != nil {
		logger.Error("get balance failed", map[string]interface{}{
			"user_id": userID,
			"error":   err.Error(),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询余额失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user_id": userID,
		"balance": balance,
		"unit":    "CNY",
	})
}

// GetBillingRecords 分页查询计费记录
// GET /api/v1/billing/records?limit=20&offset=0
func (h *BillingHandler) GetBillingRecords(c *gin.Context) {
	userID, _ := uuid.Parse(c.GetString("user_id"))

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit > 100 {
		limit = 100
	}

	records, total, err := h.billingService.GetBillingRecords(c.Request.Context(), userID, limit, offset)
	if err != nil {
		logger.Error("get billing records failed", map[string]interface{}{
			"user_id": userID,
			"error":   err.Error(),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询计费记录失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items":  records,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GetTransactions 分页查询余额变动记录
// GET /api/v1/billing/transactions?limit=20&offset=0
func (h *BillingHandler) GetTransactions(c *gin.Context) {
	userID, _ := uuid.Parse(c.GetString("user_id"))

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit > 100 {
		limit = 100
	}

	txns, total, err := h.billingService.GetTransactionHistory(c.Request.Context(), userID, limit, offset)
	if err != nil {
		logger.Error("get transactions failed", map[string]interface{}{
			"user_id": userID,
			"error":   err.Error(),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询变动记录失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items":  txns,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// RechargeRequest 充值请求体
type RechargeRequest struct {
	Amount      float64 `json:"amount" binding:"required,gt=0"`
	Description string  `json:"description"`
}

// Recharge 给用户账户充值（生产环境应接入支付网关；当前仅作演示）
// POST /api/v1/billing/recharge
func (h *BillingHandler) Recharge(c *gin.Context) {
	userID, _ := uuid.Parse(c.GetString("user_id"))

	var req RechargeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Amount > 10000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "单次充值不超过 10000 元"})
		return
	}

	description := req.Description
	if description == "" {
		description = "账户充值"
	}

	// 直接调用 UserRepository.UpdateBalance (通过 billingService 暴露)
	if err := h.billingService.Recharge(c.Request.Context(), userID, req.Amount, description); err != nil {
		logger.Error("recharge failed", map[string]interface{}{
			"user_id": userID,
			"amount":  req.Amount,
			"error":   err.Error(),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "充值失败: " + err.Error()})
		return
	}

	balance, _ := h.billingService.GetBalance(c.Request.Context(), userID)
	logger.Info("recharge success", map[string]interface{}{
		"user_id":     userID,
		"amount":      req.Amount,
		"new_balance": balance,
	})

	c.JSON(http.StatusOK, gin.H{
		"message":     "充值成功",
		"recharged":   req.Amount,
		"new_balance": balance,
	})
}

// GetToolPrices 查询工具定价表（公开接口，无需鉴权）
// GET /api/v1/billing/prices
func (h *BillingHandler) GetToolPrices(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"prices":                 billing.ToolPrices,
		"token_cost_per_1k":      billing.TokenCostPer1K,
		"min_assessment_balance": billing.MinAssessmentBalance,
		"expert_share_ratio":     billing.ExpertShareRatio,
		"currency":               "CNY",
	})
}

// GetExpertEarnings 专家查询自己的工具调用收益明细
// GET /api/v1/billing/earnings?limit=20&offset=0
func (h *BillingHandler) GetExpertEarnings(c *gin.Context) {
	expertID, _ := uuid.Parse(c.GetString("user_id"))

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit > 100 {
		limit = 100
	}

	records, total, totalEarnings, err := h.billingService.GetExpertEarnings(c.Request.Context(), expertID, limit, offset)
	if err != nil {
		logger.Error("get expert earnings failed", map[string]interface{}{
			"expert_id": expertID,
			"error":     err.Error(),
		})
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询收益失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"items":          records,
		"total":          total,
		"total_earnings": totalEarnings,
		"limit":          limit,
		"offset":         offset,
	})
}
