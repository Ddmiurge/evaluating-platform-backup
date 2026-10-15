package maclaw

// promptfoo_strategy_catalog.go — Phase 3 开篇 · 任务 1 · the strategy catalog,
// discovered from promptfoo at generation time instead of hand-maintained.
//
// # The gap this closes
//
// The platform used to ship a fixed list of 30 strategy ids, written by hand
// as `s("id", "名称", "摘要")` calls in promptfoo_plugin_catalog.go. promptfoo
// 0.123.0 exposes **35** native strategies. The hand-written list could not
// keep up, and did not: `morse`, `piglatin`, `camelcase`, `emoji`,
// `jailbreak-templates`, `layer`, `indirect-web-pwn`, `image`, `audio`,
// `video`, `gcg`, `custom` and `basic` were all unreachable — a user could not
// select them at all, which is the "我是不是还不能自定义设置策略" half of the
// question this work answers.
//
// # How the list is obtained (§3.4.1: runtime evaluation, never literal counting)
//
// scripts/gen_strategy_catalog.mjs imports promptfoo's **built entrypoint** and
// reads `default.redteam.Strategies` — the same array the engine asserts
// against in `promptfoo_engine/src/runner/nativeStrategyMap.ts`. Two
// alternatives were rejected on evidence:
//
//   - Counting string literals in the dist. `ALL_STRATEGIES` (the zod enum) is
//     `["default", ...DEFAULT_STRATEGIES, ...ADDITIONAL_STRATEGIES,
//     ...STRATEGY_COLLECTIONS, ...AGENTIC_STRATEGIES]` — **36 entries for 35
//     executable strategies**, because `default` is accepted by the config
//     schema but has no action. Reading the enum would have produced a
//     catalogue entry that throws at run time.
//   - Resolving the `strategies-<hash>.js` chunk by filename or by shape. Three
//     content-hashed variants of it are present on disk for a single install
//     (`strategies-4rVJCUEg.js`, `strategies-DGPYW_OT.js`,
//     `strategies-DpYzBITt.js`).
//
// This is the same discipline as U6's `gen_remote_dep.mjs`, and for the same
// reason: only a runtime `import()` reads the values the library actually uses.
//
// # Why a strategy can be *unavailable* without being remote-gated
//
// U6's guardrail answers one question — "does generating this need promptfoo's
// remote service?" — and its strategy table holds 4 entries. Discovery found
// **9** strategies that cannot run here, in four distinct ways, only one of
// which U6 covers:
//
//	availability      count   meaning
//	local                23   runs here; offered to users
//	remote                6   needs promptfoo's remote generation service (U6)
//	no_op                 2   runs, generates 0 cases — the silent-0-coverage shape
//	media                 3   needs a media channel (ffmpeg / multimodal)
//	unknown               1   probe produced 0 cases, cause not attributable
//
// The `no_op` bucket is the important new one, and it is exactly the failure
// mode U6 was built to eliminate — arrived at from the other side. `simba`
// logs "The \"simba\" strategy has been deprecated and removed. This strategy
// will be skipped" and returns `[]`; the run exits 0 and the report shows the
// strategy as applied. `basic` is promptfoo's deliberate "no strategy" control
// and is empty by design. Neither throws, neither needs remote generation, so
// **U6's table does not and cannot gate them** — and the platform's
// `nativeStrategyMap.ts` was mapping `simba → simba`, i.e. shipping a strategy
// that provably generates nothing while claiming it ran.
//
// # The platform's invented dimensions are gone, and stay gone
//
// This file used to argue the opposite: that the platform's own dimensions
// (`direct`, `role-play`, `encoding`, `multi-turn`, `assumed-knowledge`,
// `few-shot`, `many-shot`, `translate`, `otherEncodings`) described *how the
// platform wraps an attack prompt*, that they were produced by the engine's own
// prompt synthesis, and that deleting them would drop real coverage. That
// argument was true under Plan A, where a strategy id was **text** handed to the
// platform's own generation model (`generateTests.ts`), so any string "worked".
//
// Under Plan C the vocabulary belongs to promptfoo, and those nine ids are in
// neither of its slots — verified against the generated tables, not by reading
// the docs: none of them appears among the 35 native strategies
// (`testdata/promptfoo_strategy_catalog.json`) or the 119 plugins
// (`testdata/promptfoo_plugin_legality.json`). Forwarding one threw; dropping
// one did nothing, silently.
//
// P3-1 therefore withdrew all nine, and P3-3 removed the machinery that used to
// classify them. They are recorded in `retiredStrategyCatalog`
// (promptfoo_strategy_retirement.go) with a reason and a replacement path, and a
// request naming one is refused at the submit boundary. Four of them
// (`role-play` / `assumed-knowledge` / `few-shot` / `many-shot`) were once
// scheduled to come back as promptfoo **custom** strategies; that todo was
// cancelled — see that file's header for why re-adding them would reinstate the
// two-vocabulary debt P3-1 exists to remove.
//
// # Update procedure (after any promptfoo upgrade)
//
//	cd evaluating_platform && node scripts/gen_strategy_catalog.mjs
//	cd .. && go test ./internal/maclaw/ -run TestPromptfooStrategyCatalog
//
// TestPromptfooStrategyCatalogMatchesPromptfoo re-derives the native id set from
// the generated table and fails if the committed data drifted.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Strategy availability classes, as classified by the generator from a runtime
// probe of each strategy's `action()`.
const (
	// StrategyAvailabilityLocal means the strategy generates cases in this
	// deployment and may be selected.
	StrategyAvailabilityLocal = "local"
	// StrategyAvailabilityRemote means promptfoo's remote generation service is
	// required. Gated by U6's pre-flight guard.
	StrategyAvailabilityRemote = "remote"
	// StrategyAvailabilityNoOp means the strategy runs and reports success while
	// generating nothing. Never offered: it is the silent-0-coverage shape.
	StrategyAvailabilityNoOp = "no_op"
	// StrategyAvailabilityMedia means the strategy needs a media transport
	// (external binary / multimodal) this deployment has not provisioned.
	StrategyAvailabilityMedia = "media"
	// StrategyAvailabilityRequiresRuntimeInput means the strategy needs config
	// the platform does not send.
	StrategyAvailabilityRequiresRuntimeInput = "requires_runtime_input"
	// StrategyAvailabilityUnknown means the runtime probe produced no cases and
	// the cause could not be attributed. Recorded rather than assumed, so it
	// stays visible for review instead of being silently offered as working.
	StrategyAvailabilityUnknown = "unknown"
	// StrategyAvailabilityUnsupported means the strategy threw when probed.
	StrategyAvailabilityUnsupported = "unsupported"
)

// Native strategy availability values that must NOT be offered to users.
//
// `unknown` is in this list deliberately. "We could not prove it works" is not
// a licence to ship it: offering an unverified strategy reproduces the very
// silent-coverage failure the other buckets exist to prevent, just with less
// evidence attached.
var strategyUnavailableAvailabilities = map[string]bool{
	StrategyAvailabilityRemote:               true,
	StrategyAvailabilityNoOp:                 true,
	StrategyAvailabilityMedia:                true,
	StrategyAvailabilityRequiresRuntimeInput: true,
	StrategyAvailabilityUnknown:              true,
	StrategyAvailabilityUnsupported:          true,
}

// strategyCatalogTable is the on-disk shape of
// testdata/promptfoo_strategy_catalog.json. Field names mirror the JSON tags.
type strategyCatalogTable struct {
	Comment                     string                `json:"_comment"`
	PromptfooVersion            string                `json:"promptfoo_version"`
	DiscoveryMethod             string                `json:"discovery_method"`
	Totals                      strategyCatalogTotals `json:"totals"`
	NativeStrategies            []NativeStrategyRow   `json:"native_strategies"`
	PlatformInventedStrategyIDs []string              `json:"platform_invented_strategy_ids"`
	PlatformToNativeMap         map[string]string     `json:"platform_to_native_map"`
	NativeExposure              map[string]string     `json:"native_exposure"`
}

type strategyCatalogTotals struct {
	NativeStrategies     int `json:"native_strategies"`
	Local                int `json:"local"`
	Remote               int `json:"remote"`
	RemoteRequired       int `json:"remote_required"`
	NoOp                 int `json:"no_op"`
	RequiresRuntimeInput int `json:"requires_runtime_input"`
	Media                int `json:"media"`
	Unknown              int `json:"unknown"`
	Unsupported          int `json:"unsupported"`
	PlatformInvented     int `json:"platform_invented"`
}

// NativeStrategyRow is one discovered promptfoo strategy.
type NativeStrategyRow struct {
	ID                     string   `json:"id"`
	RequiresGoalExtraction bool     `json:"requires_goal_extraction"`
	Availability           string   `json:"availability"`
	Reason                 string   `json:"reason"`
	ProbeGeneratedCases    int      `json:"probe_generated_cases"`
	ProbeError             *string  `json:"probe_error"`
	ProbeLoggedDeprecation bool     `json:"probe_logged_deprecation"`
	NeedsRuntimeInput      []string `json:"needs_runtime_input"`
	// RemoteRequired is MEASURED, not listed: the generator reads promptfoo's own
	// "remote generation explicitly disabled" throw sites out of the installed
	// dist and follows deprecated-alias delegations (`jailbreak` -> the
	// iterative:meta provider). It used to be a hand-written flag fed from the
	// remote-gate table, which is why `jailbreak` and `jailbreak:meta` were
	// offered and then failed mid-run.
	RemoteRequired bool `json:"remote_required"`
	// RemoteEvidence names which measurement produced the verdict
	// ("throw-site" / "probe-helper" / "delegation:<detail>"), so a reader can
	// audit it rather than take the boolean on faith.
	RemoteEvidence string `json:"remote_evidence"`
}

//go:embed testdata/promptfoo_strategy_catalog.json
var strategyCatalogTableJSON []byte

// strategyCatalogData is the parsed table, indexed by native strategy id.
// Parsed once at init; a malformed table is a generator bug rather than a
// runtime condition, so init panics rather than degrading into an empty
// catalogue that would silently hide every native strategy.
var strategyCatalogData = mustParseStrategyCatalog()

func mustParseStrategyCatalog() *strategyCatalogTable {
	var table strategyCatalogTable
	if err := json.Unmarshal(strategyCatalogTableJSON, &table); err != nil {
		panic(fmt.Sprintf("maclaw: embedded promptfoo_strategy_catalog.json is malformed: %v", err))
	}
	if len(table.NativeStrategies) == 0 {
		panic("maclaw: embedded promptfoo_strategy_catalog.json lists zero native strategies; the catalogue would be empty")
	}
	return &table
}

// nativeStrategyRows returns every discovered native strategy, sorted by id.
func nativeStrategyRows() []NativeStrategyRow {
	rows := make([]NativeStrategyRow, len(strategyCatalogData.NativeStrategies))
	copy(rows, strategyCatalogData.NativeStrategies)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

// NativeStrategyByID returns the discovered row for a promptfoo strategy id.
func NativeStrategyByID(id string) (NativeStrategyRow, bool) {
	want := strings.TrimSpace(id)
	for _, row := range strategyCatalogData.NativeStrategies {
		if row.ID == want {
			return row, true
		}
	}
	return NativeStrategyRow{}, false
}

// StrategyIsSelectable reports whether a discovered native strategy may be
// offered to users and forwarded to promptfoo.
//
// This is the single decision point behind the catalogue's availability
// filtering, the run pre-flight, and the frontend greying, so the three cannot
// disagree about what "available" means.
func StrategyIsSelectable(id string) bool {
	row, ok := NativeStrategyByID(id)
	if !ok {
		// Unknown ids are handled by the caller: a platform-invented dimension
		// (`direct`, `role-play`, …) is legitimate and is not in this table.
		return false
	}
	return !strategyUnavailableAvailabilities[row.Availability]
}

// LocalNativeStrategyIDs returns the sorted ids of every native strategy that
// runs in this deployment. These are the strategies the platform newly gained
// by discovering promptfoo's catalogue instead of hand-listing it.
func LocalNativeStrategyIDs() []string {
	ids := make([]string, 0, len(strategyCatalogData.NativeStrategies))
	for _, row := range nativeStrategyRows() {
		if StrategyIsSelectable(row.ID) {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

// UnavailableNativeStrategyIDs returns the sorted ids of native strategies that
// must not be offered, with the reason each is withheld.
func UnavailableNativeStrategyIDs() map[string]string {
	out := make(map[string]string)
	for _, row := range nativeStrategyRows() {
		if !StrategyIsSelectable(row.ID) {
			out[row.ID] = row.Reason
		}
	}
	return out
}

// PlatformToNativeStrategyID maps a platform strategy id onto the promptfoo
// native id it corresponds to, for the ids promptfoo owns.
//
// Platform-invented dimensions are absent by construction: there is no native
// counterpart to map them to, and after P3-1 the catalogue contains none anyway.
// The map is asserted to agree with the engine's `nativeStrategyMap.ts` by
// TestPromptfooStrategyNativeMappingMatchesEngineTable.
func PlatformToNativeStrategyID(platformID string) string {
	return strategyCatalogData.PlatformToNativeMap[strings.TrimSpace(platformID)]
}

// StrategyNativeID returns the promptfoo id for a platform strategy id,
// resolving the identity case too.
//
// `PlatformToNativeStrategyID` returns "" for an id that maps to itself,
// because the generated table stores only the non-trivial mappings and the card
// layer omits the metadata when the two ids agree. A consumer that needs the id
// itself — the engine catalog, an error message — wants the resolved value, not
// the "is there a difference?" question. So this is the accessor to use, and
// `PlatformToNativeStrategyID` stays the difference query.
//
// An id in no table at all also returns its own value. That is a deliberate
// fallback, not a claim: it lets a caller distinguish "no id at all" (empty
// string) from "an id this build cannot place", and the engine refuses the
// latter explicitly (`native_generation_strategy_unknown`, P3-3). It is not a
// licence to accept invented ids — the catalogue and the submit gate are what
// keep those out (P3-1).
func StrategyNativeID(platformID string) string {
	id := strings.TrimSpace(platformID)
	if id == "" {
		return ""
	}
	if mapped := PlatformToNativeStrategyID(id); mapped != "" {
		return mapped
	}
	if gated := PromptfooRemoteDepStrategyID(id); gated != "" {
		return gated
	}
	return id
}

// StrategyCatalogStatsForDocs summarizes discovery for docs and operator-facing
// endpoints. Every number is derived from the generated table, so it cannot
// drift from what the catalogue actually offers.
type StrategyCatalogStats struct {
	PromptfooVersion string            `json:"promptfoo_version"`
	DiscoveryMethod  string            `json:"discovery_method"`
	NativeTotal      int               `json:"native_total"`
	NativeLocal      int               `json:"native_local"`
	NativeRemote     int               `json:"native_remote"`
	NativeNoOp       int               `json:"native_no_op"`
	NativeMedia      int               `json:"native_media"`
	NativeUnknown    int               `json:"native_unknown"`
	PlatformInvented int               `json:"platform_invented"`
	SelectableTotal  int               `json:"selectable_total"`
	Unavailable      map[string]string `json:"unavailable,omitempty"`
}

// PromptfooStrategyCatalogStats computes the stats implied by the compiled-in
// tables.
func PromptfooStrategyCatalogStats() StrategyCatalogStats {
	return StrategyCatalogStats{
		PromptfooVersion: strategyCatalogData.PromptfooVersion,
		DiscoveryMethod:  strategyCatalogData.DiscoveryMethod,
		NativeTotal:      len(strategyCatalogData.NativeStrategies),
		NativeLocal:      strategyCatalogData.Totals.Local,
		NativeRemote:     strategyCatalogData.Totals.Remote,
		NativeNoOp:       strategyCatalogData.Totals.NoOp,
		NativeMedia:      strategyCatalogData.Totals.Media,
		NativeUnknown:    strategyCatalogData.Totals.Unknown,
		PlatformInvented: len(strategyCatalogData.PlatformInventedStrategyIDs),
		SelectableTotal:  len(promptfooStrategyCatalog) + len(LocalNativeStrategyIDs()),
		Unavailable:      UnavailableNativeStrategyIDs(),
	}
}

// nativeStrategyDisplayNames maps the native strategies the platform newly
// exposes onto Chinese display names and summaries.
//
// Only the strategies that were NOT already in the hand-written catalogue need
// one; the pre-existing 30 keep their curated text. These are platform-authored
// descriptions of what the strategy does — deliberately **not** derived from
// promptfoo's own metadata, which this build does not expose (every entry in
// `Strategies` carries only `id`, `action` and sometimes
// `requiresGoalExtraction`; there is no `displayName` to read).
//
// A strategy missing from this map is still selectable — it simply falls back to
// its id as the label. That is a presentation gap, not a correctness one, and
// it is asserted by TestPromptfooStrategyCatalogHasDisplayCoverage rather than
// being allowed to hide a capability.
var nativeStrategyDisplayNames = map[string]struct{ Name, Summary string }{
	"morse":     {"摩斯电码", "将攻击载荷转为摩斯电码，测试编码层面对关键词过滤的绕过能力。"},
	"piglatin":  {"猪拉丁文", "将攻击载荷转为猪拉丁文，测试非标准字符变形下的安全边界。"},
	"camelcase": {"驼峰拆分", "将攻击载荷拆分为驼峰形式，测试分词/归一化前的过滤绕过。"},
	"emoji":     {"Emoji 替换", "将攻击载荷字符替换为 Emoji，测试多模态/符号归一化前的过滤绕过。"},
	"jailbreak-templates": {
		"越狱模板库", "套用静态越狱模板库改写攻击载荷，覆盖常见的框架式越狱话术。",
	},
	"layer": {
		"多层组合", "按配置把多个原生策略串联成一条攻击链，逐步叠加包装维度。",
	},
	"indirect-web-pwn": {
		"间接网页注入", "通过目标引用的外部网页内容注入攻击载荷，检验间接提示注入面。",
	},
	"custom": {
		"自定义策略", "平台自造：按专家发布的策略包驱动多轮对抗（详见自定义策略包发布链路）。",
	},
	// Deprecated upstream aliases that still generate cases. promptfoo logs a
	// deprecation warning naming the replacement but does not refuse, so they
	// work — they are named here rather than falling back to a raw id, because
	// "prompt-injection" as a label next to "越狱模板库" (which is what it
	// dispatches to) would read like two different techniques.
	"jailbreak": {
		"元提示越狱（旧别名）", "promptfoo 的旧单轮越狱入口，已废弃：等价于 jailbreak:meta，推荐直接选元提示越狱。",
	},
	"prompt-injection": {
		"提示注入模板（旧别名）", "promptfoo 的旧提示注入入口，已废弃：等价于 jailbreak-templates，推荐直接选越狱模板库。",
	},
}

// nativeStrategyDisplay returns the Chinese name and summary for a native
// strategy, falling back to the id when none is authored.
func nativeStrategyDisplay(id string) (name, summary string) {
	if entry, ok := nativeStrategyDisplayNames[id]; ok {
		return entry.Name, entry.Summary
	}
	return id, "promptfoo 原生攻击策略 " + id + "，由平台动态发现。"
}
