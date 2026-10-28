package maclaw

// promptfoo_custom_strategy_code.go — the BFF half of CODE strategy packages
// (`pf_strategy_form=js`).
//
// Design: docs/architecture/strategy-code-packages.md.
//
// # What this file is responsible for
//
//   1. deciding whether a package is a TEXT strategy (`strategyText`, inlined by
//      the engine) or a CODE strategy (an ESM module, loaded by promptfoo via
//      `file://`), and carrying that decision in the publication metadata;
//   2. the cheap, deterministic checks a Go process can make about JS source —
//      size, shape, and the obvious capability tokens;
//   3. mapping a CODE package to the id promptfoo will show in the report.
//
// # What it deliberately does NOT do
//
// It does not decide whether the module is *loadable*. Only a JS runtime can
// answer that, and the answer has to be produced at PUBLICATION time: a module
// that fails to load inside a run fails after the user has paid for the probes.
// The engine exposes the real check (Step 1 of the design); this file's job is
// to make sure nothing obviously unsafe is ever even offered to it.
//
// # The token list is a threshold, not a boundary
//
// `child_process`, `process.env`, `fetch(` and friends are refused because a
// strategy is supposed to be a pure function, and because these are the tokens
// that turn "transform these cases" into "reach outside the workdir". A
// determined author can evade a regex. The boundary is the tenant isolation
// described in §2 of the design, and the output validation §6.2 — never this
// list. Do not let a future change treat it as the security control.

import (
	"regexp"
	"strings"
)

const (
	// CustomStrategyFormJS is the form value that marks a CODE package.
	CustomStrategyFormJS = "js"

	// CustomStrategyCodeMaxBytes caps a code package. Algorithmic code fits well
	// inside this; a larger payload is data trying to ride along.
	CustomStrategyCodeMaxBytes = 256 * 1024
)

// Error codes. Distinct from the YAML ones so the submitter is told which
// problem they have (a text package missing strategyText is not the same failure
// as a module that reaches for process.env).
const (
	CustomStrategyErrCodeInvalid = "pf_strategy_code_invalid"
	CustomStrategyErrCodeUnsafe  = "pf_strategy_code_unsafe"
)

// customStrategyCodeForbidden are the capability tokens refused before anything
// is stored. See the file header: this raises the bar, it does not build the wall.
var customStrategyCodeForbidden = []struct {
	pattern *regexp.Regexp
	label   string
}{
	{regexp.MustCompile(`\bchild_process\b`), "child_process"},
	{regexp.MustCompile(`\bprocess\s*\.\s*env\b`), "process.env"},
	{regexp.MustCompile(`\brequire\s*\(`), "require("},
	{regexp.MustCompile("\\bimport\\s*\\(\\s*['\"`]"), "dynamic import()"},
	{regexp.MustCompile(`\bfetch\s*\(`), "fetch("},
	{regexp.MustCompile(`\bhttps?\s*\.\s*request\b`), "http.request"},
	{regexp.MustCompile(`\bnew\s+Function\b`), "new Function"},
	{regexp.MustCompile(`\beval\s*\(`), "eval("},
	{regexp.MustCompile(`\bglobalThis\b`), "globalThis"},
	{regexp.MustCompile(`(?i)\bfile://`), "file://"},
}

// IsCustomStrategyCodeForm reports whether a package declares the CODE form.
func IsCustomStrategyCodeForm(metadata map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(metadata[CustomStrategyMetadataFormKey]), CustomStrategyFormJS)
}

// ValidateAndParseCustomStrategyCodePublication is the pre-publication guard for
// a CODE package, and returns the module source it validated.
//
// Order mirrors the YAML path: form first (it produces the message the submitter
// needs), then size, then the capability tokens, then shape. It is a pure
// predicate — no I/O, no store access — so a refused package leaves nothing
// behind.
func ValidateAndParseCustomStrategyCodePublication(in EvaluationResourceInput) (string, string, error) {
	if in.Kind != EvaluationResourceKindPromptfooStrategy || !IsCustomStrategyCodeForm(in.Metadata) {
		return "", "", nil
	}
	source := in.Payload
	if strings.TrimSpace(source) == "" {
		return "", "", customStrategyValidationErrorf(CustomStrategyErrCodeInvalid,
			"代码策略的内容为空。请提交一个导出 { id, action } 的 ESM 模块。")
	}
	if len(source) > CustomStrategyCodeMaxBytes {
		return "", "", customStrategyValidationErrorf(CustomStrategyErrCodeInvalid,
			"代码策略体积 %d 字节，超过上限 %d 字节。代码包只应包含策略逻辑。", len(source), CustomStrategyCodeMaxBytes)
	}
	for _, item := range customStrategyCodeForbidden {
		if item.pattern.MatchString(source) {
			return "", "", customStrategyValidationErrorf(CustomStrategyErrCodeUnsafe,
				"代码策略命中禁止项 %q：策略必须是纯函数，不得读取环境/文件、不得发起网络请求或加载外部模块。"+
					"（这条检查提高门槛，不是隔离边界；真正的边界是运行环境的租户隔离。）", item.label)
		}
	}
	// Shape: a default export carrying `action`. A package without it would fail
	// inside the run, after the user paid for the probes.
	if !strings.Contains(source, "action") {
		return "", "", customStrategyValidationErrorf(CustomStrategyErrCodeInvalid,
			"代码策略缺少 action 函数（模块需导出 { id, action }）。")
	}
	if !regexp.MustCompile(`export\s+(default\s*\{|const\s+|async\s+function)`).MatchString(source) {
		return "", "", customStrategyValidationErrorf(CustomStrategyErrCodeInvalid,
			"代码策略必须是 ESM 模块（使用 export default { id, action } 或具名导出）。")
	}

	// A code package MUST name its variant explicitly.
	//
	// Step 0 measured that promptfoo records the file:// ref as the strategy id,
	// not the module's own exported id — so the platform's `custom:<variant>` is
	// the only thing that can distinguish two code packages. Without an explicit
	// variant every package derives the same `custom:strategy` from the handle
	// prefix, and two packages in one run would collide in strategy_stats.
	//
	// Requiring it at publication (rather than deriving one) also keeps the id
	// stable across republishes, which is what makes a report comparable to the
	// next one.
	variant := strings.TrimSpace(in.Metadata[CustomStrategyMetadataVariantKey])
	if variant == "" {
		return "", "", customStrategyValidationErrorf(CustomStrategyErrCodeInvalid,
			"代码策略必须显式声明策略标识（pf_strategy_variant，例如 my-code-probe）：promptfoo 以文件路径作为策略 id，"+
				"平台只能靠这个标识把报告里的策略区分开。")
	}
	strategyID := CustomStrategyVariantFor(in.Name, in.Metadata)
	if err := validateCustomStrategyID(strategyID); err != nil {
		return "", "", err
	}
	return source, strategyID, nil
}

// CustomStrategyCodePublicID is the id the report shows for a code package.
//
// promptfoo records the `file://` PATH as the strategy id (measured 2026-10-28,
// Step 0). The engine normalises that path back to this value before
// aggregating, so the run record and the report carry a stable, readable name —
// and never a filesystem path.
//
// The `code:` segment is deliberate: promptfoo's own `validateStrategies()`
// demands `config.strategyText` for any id starting with `custom:`, and a code
// package has none. Keeping the engine-side ref as `custom:<variant>` (its file
// name and its promptfoo strategy slot) while reporting `custom:code:<variant>`
// means neither side has to lie about what it is.
func CustomStrategyCodePublicID(strategyID string) string {
	id := strings.TrimSpace(strategyID)
	variant := strings.TrimPrefix(id, "custom:")
	if variant == id || variant == "" {
		return id
	}
	return "custom:code:" + variant
}
