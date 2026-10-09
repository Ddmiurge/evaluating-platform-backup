package billing

// ToolPrices 各工具定价（元/次调用）
var ToolPrices = map[string]float64{
	"prompt_injection":     0.50,
	"jailbreak":            0.30,
	"compliance_check":     0.40,
	"compliance_evaluator": 1.00,
	"goal_hijacking":       0.60,
	"tool_poisoning":       0.60,
	"report_generator":     2.00,
}

const (
	// MinAssessmentBalance 发起评估所需的最低余额（元）
	MinAssessmentBalance = 5.00
	// TokenCostPer1K 每 1000 tokens 费用（元）
	TokenCostPer1K = 0.01
	// ExpertShareRatio 专家工具被调用时的收益分成比例
	ExpertShareRatio = 0.30
)
