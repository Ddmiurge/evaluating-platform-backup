package repository

// json_columns.go — shared JSONB column error policy (audit 2026-10-12 P1-1/2/3/6).
//
// # Why this file exists
//
// Every JSONB column in this layer used to be decoded with a bare
// `_ = json.Unmarshal(raw, &field)`. That is a silent-corruption primitive:
//
//	corrupt JSON in DB → Unmarshal returns an error → the error is discarded →
//	the target field keeps its zero value → the function returns a normal,
//	nil-error record.
//
// The caller therefore cannot tell "this run legitimately has no plugins"
// from "this run's plugins column is corrupt and I just showed you an empty
// list". On a red-team platform that difference is the whole product: an
// evaluation that ran 12 attack plugins must never render as "0 plugins".
//
// # The policy: critical vs degradable
//
// Columns are split into two classes, and the split is a *safety* judgement,
// not a convenience one:
//
//   - CRITICAL — losing the value would change a verdict, a coverage claim, or
//     an instruction that gets sent to a target. Fail CLOSED: the row is
//     reported as corrupt and is never returned as if it were healthy.
//   - DEGRADABLE — losing the value costs display fidelity or telemetry only.
//     Degrade to the zero value, but LOG at ERROR with the row identity so the
//     corruption is still discoverable and greppable.
//
// The current classification, with the reason for each:
//
//	engine_runs.plugins / strategies → CRITICAL
//	    These are the attack surface the engine was told to exercise. Zeroing
//	    them turns "we probed 12 plugins" into "we probed none" — a false
//	    all-clear on exactly the claim the platform exists to make.
//
//	engine_runs.result → CRITICAL
//	    The redacted aggregate findings (severity counts, totals, risk
//	    categories). Zeroing it turns "3 critical findings" into "no result".
//	    Worst possible corruption in this schema.
//
//	engine_runs.stage_durations → DEGRADABLE
//	    Per-stage wall-clock breakdown. Pure progress telemetry; no verdict
//	    depends on it.
//
//	engine_runs.token_usage → DEGRADABLE
//	    Cost/telemetry counters. Verified NOT read by internal/billing (no
//	    billing computation consumes it today), so zeroing cannot misstate a
//	    charge. PROMOTION TRIGGER: if token usage ever feeds billing or quota
//	    enforcement, this column must move to CRITICAL.
//
//	maclaw_resources.assessment_types → CRITICAL
//	    Declares which evaluation suites apply to the resource. Zeroing it
//	    makes a live resource look like it carries no assessments, which
//	    misreports capability to the catalog.
//
//	maclaw_resources.tags / metadata → DEGRADABLE
//	    Labels and free-form key/value display data. Losing them costs
//	    presentation, not meaning.
//
//	templates.variables → CRITICAL
//	    The declared placeholders of a prompt template. Zeroing it renders the
//	    prompt with no substitutions, so the attack text actually sent to the
//	    target silently differs from the reviewed one — the evaluation runs but
//	    measures the wrong thing. Fails closed.
//
// # Absent vs corrupt
//
// len(raw) == 0 is treated as *absent* (zero value, no error) uniformly, not
// as corruption. These columns are all `NOT NULL DEFAULT ...` in their
// migrations, so an empty scan result is far more likely to be a NULL/legacy
// row than an actual syntax error — and failing closed on it would produce
// false positives on data that is merely empty. *Present but unparseable* is
// the corruption case this file is about.

import (
	"encoding/json"
	"strconv"
	"strings"

	"evaluating_platform/pkg/logger"
)

// CorruptRowError 标记「DB 里这一行的这个 JSONB 列解析失败」。
//
// 语义约定（重要）：它表示**该行不可信**，而不是「该字段为空」。
// 调用方拿到它时，绝不能把这一行当作零值记录继续使用 —— 那是本次审计
// 要消灭的原始 bug。
type CorruptRowError struct {
	Table string // 逻辑表名，如 "engine_runs"
	RowID string // 行标识（engine_runs.id / maclaw_resources.id / templates.id）
	Field string // 出问题的列名
	Err   error  // 底层 json.Unmarshal 错误
}

// Error 实现 error 接口。消息里必须同时带表、行、列三要素，
// 这样运维拿到一条日志就能直接定位到具体那一条脏数据。
func (e *CorruptRowError) Error() string {
	return "corrupt " + e.Table + " row " + e.RowID + " column " + e.Field + ": " + e.Err.Error()
}

// Unwrap 让 errors.Is / errors.As 能穿透到底层 json 错误。
func (e *CorruptRowError) Unwrap() error { return e.Err }

// newCorruptRowError 构造一条带完整定位信息的 CorruptRowError。
func newCorruptRowError(table, rowID, field string, err error) *CorruptRowError {
	return &CorruptRowError{Table: table, RowID: rowID, Field: field, Err: err}
}

// PartialCorruptionError 表示「结果整体可用，但有 N 行因为 JSONB 损坏被跳过」。
//
// 存在的理由：任务约束是「DB 里可能已经有损坏的历史数据，读路径不能因为
// 一条脏数据就整体崩掉」。但「跳过」如果完全静默，就等于把原来的
// 「错误被吞掉」换了个位置 —— 只不过吞的是行而不是字段。所以本类型把
// 降级事实**向上传递**：调用方可以据此打点/告警，同时决定仍然返回 200
// 与剩余的好数据，而不是把整个列表打成 500。
//
// 约定：携带本错误时返回的 items 仍然有效（非空、已跳过坏行）。
// 配套用法见 internal/api/handler 中对 IsPartialCorruption 的判定。
type PartialCorruptionError struct {
	Table   string             // 逻辑表名
	Skipped int                // 被跳过的坏行数
	Causes  []*CorruptRowError // 每条被跳过行的定位信息
}

// Error 实现 error 接口。
func (e *PartialCorruptionError) Error() string {
	parts := make([]string, 0, len(e.Causes))
	for _, c := range e.Causes {
		if c != nil {
			parts = append(parts, c.Error())
		}
	}
	return "partial corruption in " + e.Table + ": skipped " + strconv.Itoa(e.Skipped) +
		" row(s) with unparseable JSONB; detail=[" + strings.Join(parts, "; ") + "]"
}

// Unwrap 返回全部子错误，使 errors.As(err, &corrupt) 能取到每一条
// 被跳过行的具体定位（Go 1.20+ 的多错误 Unwrap 约定）。
func (e *PartialCorruptionError) Unwrap() []error {
	out := make([]error, 0, len(e.Causes))
	for _, c := range e.Causes {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

// newPartialCorruptionError 组装降级错误；causes 为空时返回 nil，
// 这样调用方可以直接 `return items, newPartialCorruptionError(...)`。
func newPartialCorruptionError(table string, causes []*CorruptRowError) error {
	if len(causes) == 0 {
		return nil
	}
	return &PartialCorruptionError{Table: table, Skipped: len(causes), Causes: causes}
}

// IsPartialCorruption 是标记方法，让本类型满足 maclaw.PartialCorruption
// 跨层接口（见 internal/maclaw/data_corruption.go）。
//
// 为什么不在本包再实现一个同名函数：判定逻辑只有一份，放在 maclaw 层，
// handler 与 service 共用同一个入口，避免两处判定漂移。
func (e *PartialCorruptionError) IsPartialCorruption() {}

// decodeCriticalJSONB 解析**关键**列：失败即返回 *CorruptRowError（fail-closed）。
//
// rowID 用于错误定位，必须由调用方提供 —— 没有它，一条脏数据在日志里就
// 无法被找到，这正是本次审计要修的「无法察觉」。
func decodeCriticalJSONB(table, rowID, field string, raw []byte, out any) error {
	if len(raw) == 0 {
		// 缺席 ≠ 损坏。见文件头「Absent vs corrupt」。
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return newCorruptRowError(table, rowID, field, err)
	}
	return nil
}

// decodeDegradableJSONB 解析**展示性**列：失败则降级为零值并记 ERROR 日志，
// 永不返回错误。
//
// 为什么用 logger.Error 而不是静默：降级是「可用性优先」的选择，不是
// 「这条数据没问题」的事实判断。不留日志就等于把审计发现的静默吞噬
// 原地保留 —— 只是从字段挪到了行。
func decodeDegradableJSONB(table, rowID, field string, raw []byte, out any) {
	if len(raw) == 0 {
		return
	}
	if err := json.Unmarshal(raw, out); err != nil {
		logger.Error("corrupt jsonb column degraded to zero value", map[string]interface{}{
			"table":  table,
			"row":    rowID,
			"column": field,
			"err":    err.Error(),
		})
	}
}

// rowsScanner 是 pgx.Rows 的最小只读子集。
//
// 为什么收窄：把逐行扫描抽成接受这个小接口的纯函数后，就可以用 fake
// 在没有 PostgreSQL 的机器上直接测「一行 JSONB 坏了会怎样」，而不是
// 只能对着真库猜。pgx.Rows 天然满足（Next/Scan/Err 三个方法同签名），
// 所以生产调用点无需任何改动。
type rowsScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}
