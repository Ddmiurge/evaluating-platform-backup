package maclaw

// promptfoo_custom_strategy.go — Phase 3 开篇 · 任务 2 · expert-authored
// promptfoo strategy packages.
//
// # How this differs from promptfoo_custom_plugin.go, and why that difference
// is the whole task
//
// Both features let an expert publish YAML to the platform, and both end at the
// engine. They are not the same shape, and building this as "the plugin path
// again" would have produced a feature that loads and does nothing.
//
// A **plugin** is an attack *generator*: `generator` is an nunjucks template
// rendered into attack cases, `grader` is the rubric judged by. promptfoo loads
// it from a **file** — `CustomPlugin` takes a `filePath` and calls
// `maybeLoadFromExternalFile` — so the engine writes the package into its
// per-run workdir and references it as `file://<name>.yaml`.
//
// A **strategy** is an attack *wrapping dimension*. It has no generator and no
// grader: it reshapes a payload the plugin already produced. And it is loaded
// **entirely differently** — measured on promptfoo 0.123.0:
//
//  1. `file://x.js` strategies exist, but `loadStrategy()` requires
//     `isJavascriptFile(filePath)` and throws *"Custom strategy file must be a
//     JavaScript file"* otherwise (`dist/src/fileExtensions-*.js`). A file-based
//     strategy is therefore **only ever JS/TS** — an `import()` of a module
//     exporting `id` and `action`, i.e. arbitrary code execution inside the
//     engine process, which holds the materialized generation API key. D-03
//     rules that out for the first release.
//  2. There is a **yaml-native** hook instead: a strategy whose id is `custom` or
//     `custom:<variant>` and whose `config.strategyText` is the attacker's
//     instruction. `isCustomStrategy(id)` is literally
//     `id === 'custom' || id.startsWith('custom:')`, and
//     `validateStrategies()` requires `config.strategyText` to be a non-empty
//     string. `addCustom()` re-points each test case at
//     `promptfoo:redteam:custom[:<variant>]` and spreads the config through.
//
// So this file validates a `{ strategyText, … }` document that the engine
// **inlines** into the redteam config. No materialization, no workdir file, no
// import — which also means a package's bytes have no path to an `import()`.
// That is a stronger property than the plugin path can offer, and it is why the
// JS refusal here is enforced by construction rather than by a check that could
// be forgotten downstream.
//
// # Why the fields are allow-listed when promptfoo does not need them to be
//
// promptfoo's strategy `config` is `z.record(z.string(), z.unknown())`: unknown
// keys survive parsing and are spread into the provider config. So there is no
// U10-style "silently stripped, run stays green" hazard here — but there is a
// worse one: a typo'd `maxTurn` (for `maxTurns`) is *accepted and inert*.
// Failing at publication is the only way the expert learns.
//
// The engine re-checks the same list at load time (`customStrategy.ts`). That
// duplication is deliberate and is the two-gate argument U6 makes: this engine
// is separately deployable with its own REST surface, so a request that reaches
// it directly — older BFF, replayed capture, future caller — must not be able
// to smuggle a field past the platform's check.
//
// # Where the guard lives
//
// In `ResourceStoreService.SaveResource`, the single exit every write passes
// through, for the same reason as U10: the shadow-copy path re-saves a published
// package through the same store with no handler in the path, so a guard in the
// HTTP handler alone would be defeated.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// EvaluationResourceKindPromptfooStrategy is the `resource_kind` an
// expert-authored strategy package is stored under. Reuses `maclaw_resources`
// and the publication/shadow distribution tables rather than introducing a
// parallel store.
const EvaluationResourceKindPromptfooStrategy EvaluationResourceKind = "pf_strategy"

// Error codes surfaced to the browser and grepped by operators. Part of the API
// contract: the expert portal maps each to a readable message, and none of them
// may surface as a bare 500.
const (
	// CustomStrategyErrFormUnsupported means the package was submitted in a form
	// other than YAML. Same reasoning as U10 and, here, a sharper one: on
	// promptfoo 0.123.0 a file-based strategy is *only* JS/TS, so "publish your
	// strategy as a file" is not a restricted version of this feature — it is a
	// different and much more dangerous one.
	CustomStrategyErrFormUnsupported = "pf_strategy_form_unsupported"
	// CustomStrategyErrDefinitionInvalid means the YAML is not a usable custom
	// strategy: missing/blank `strategyText`, an undeclared field, or a field
	// of the wrong type.
	CustomStrategyErrDefinitionInvalid = "pf_strategy_definition_invalid"
	// CustomStrategyErrIDConflictsCatalog means the package's derived strategy
	// id collides with a built-in catalog strategy.
	CustomStrategyErrIDConflictsCatalog = "pf_strategy_id_conflicts_catalog"
	// CustomStrategyErrIDInvalid means the handle does not yield a legal
	// `custom:<variant>` id.
	CustomStrategyErrIDInvalid = "pf_strategy_id_invalid"
	// CustomStrategyErrRemoteGenerationForbidden wraps U6's code. A strategy
	// package cannot itself be remote-gated (it wraps plugins, and those are
	// gated at run time by the plugin ref), but it may *declare* that it wraps
	// gated plugins, and that declaration must be refused for the same reason
	// U10 refuses it: a package whose cases come from promptfoo's remote
	// service produces a green run with zero coverage.
	CustomStrategyErrRemoteGenerationForbidden = EngineErrPluginRequiresRemoteGeneration
	// CustomStrategyErrPayloadTooLarge means the package exceeds
	// CustomStrategyMaxPayloadBytes.
	CustomStrategyErrPayloadTooLarge = "pf_strategy_payload_too_large"
	// CustomStrategyErrRemoteReferenceForbidden means the YAML text carries an
	// absolute http(s) URL. A strategy is a set of instructions fed to the
	// generation model; it has no legitimate reason to name an endpoint, and
	// `redteamProvider` — which *does* select one — is engine-owned and cannot
	// be set from the package body.
	CustomStrategyErrRemoteReferenceForbidden = "pf_strategy_remote_reference_forbidden"
)

// CustomStrategyMaxPayloadBytes caps a published package. A strategy text is
// prose fed to an attack model; anything approaching corpus size is a mistake or
// an attempt to smuggle data out through the prompt.
const CustomStrategyMaxPayloadBytes = 128 * 1024

// CustomStrategyFormYAML is the only accepted form in the first release.
const CustomStrategyFormYAML = "yaml"

// Metadata keys, mirroring U10's plugin keys so the publication flow and the
// admin governance view can treat both kinds uniformly.
const (
	// CustomStrategyMetadataFormKey lets a submitter state the form explicitly,
	// so a wrong form produces "only YAML is accepted" rather than a schema
	// complaint about their JavaScript.
	CustomStrategyMetadataFormKey = "pf_strategy_form"
	// CustomStrategyMetadataDepsKey is a comma-separated list of catalog
	// plugin ids the strategy is designed to wrap.
	CustomStrategyMetadataDepsKey = "pf_strategy_depends_on"
	// CustomStrategyMetadataVariantKey optionally overrides the strategy variant
	// suffix. Empty means "derive it from the publication handle".
	CustomStrategyMetadataVariantKey = "pf_strategy_variant"
)

// CustomStrategyRefPrefix marks a strategy id as a reference to a published
// package rather than a built-in catalog id. Mirrors `CustomPluginRefPrefix`
// but is a distinct string on purpose: both travel in the same request, and a
// shared prefix would let any future pattern-match confuse the two.
const CustomStrategyRefPrefix = "pf_strategy:"

// PromptfooCustomStrategyDefinition is the package schema.
//
// `strategyText` is the only required field, and it is the one promptfoo
// actually consumes. The remaining fields were measured as read by
// `CustomProvider`'s constructor (`dist/src/custom-*.js:100-112`); each has a
// documented upstream default, which is why they are all optional.
//
// `redteamProvider` and `targetId` are intentionally NOT fields here even though
// promptfoo reads them from the same config object: they choose the endpoint the
// attack model is called on, so they are engine-owned. Accepting them from an
// expert package would let a published package redirect the platform's
// generation credential somewhere else. The engine sets them.
type PromptfooCustomStrategyDefinition struct {
	// StrategyText is the attacker's instruction, interpolated into promptfoo's
	// `CUSTOM_PARENT_TEMPLATE` as `{{customStrategyText}}` and sent to the
	// attack model. Required, non-blank.
	//
	// Verified by capture, not by reading a schema: driving the real
	// CustomProvider against a local OpenAI-compatible stub with a sentinel in
	// this field produced requests whose system message carried the sentinel
	// verbatim under `###Custom Strategy###`. A field that were accepted and
	// ignored would silently fall back to promptfoo's default
	// ("Follow the conversation naturally to achieve the objective.") and every
	// package would behave identically to every other.
	StrategyText string `yaml:"strategyText"`
	// MaxTurns bounds the attack conversation. Upstream default 10, and clamped
	// to 10 by promptfoo when not logged into cloud. Must be >= 1.
	MaxTurns *int `yaml:"maxTurns,omitempty"`
	// MaxBacktracks bounds how many times the attack model may retry a turn.
	// Upstream default 10. Must be >= 1.
	MaxBacktracks *int `yaml:"maxBacktracks,omitempty"`
	// Stateful makes the attack conversation server-side stateful, so the target
	// can thread a conversation id across turns. Interacts with U9's
	// `assertAgentConversationIsExecutable` guard.
	Stateful *bool `yaml:"stateful,omitempty"`
	// ContinueAfterSuccess keeps attacking after a successful turn instead of
	// stopping.
	ContinueAfterSuccess *bool `yaml:"continueAfterSuccess,omitempty"`
	// ExcludeTargetOutputFromAgenticAttackGeneration hides the target's replies
	// from the attack model.
	ExcludeTargetOutputFromAgenticAttackGeneration *bool `yaml:"excludeTargetOutputFromAgenticAttackGeneration,omitempty"`
}

// customStrategyAllowedKeys is the exact key set the engine also enforces
// (`ALLOWED_STRATEGY_KEYS` in promptfoo_engine/src/runner/customStrategy.ts).
//
// Kept as a literal rather than derived from the struct for U10's reason: a
// field added to the Go struct for Go-side convenience must not widen what the
// engine will accept, and the two lists are asserted equal by
// TestCustomStrategyAllowedKeysMatchEngine so they cannot drift.
var customStrategyAllowedKeys = map[string]bool{
	"strategyText":         true,
	"maxTurns":             true,
	"maxBacktracks":        true,
	"stateful":             true,
	"continueAfterSuccess": true,
	"excludeTargetOutputFromAgenticAttackGeneration": true,
}

// customStrategyIDPattern constrains the `custom:<variant>` id.
//
// promptfoo derives two things from the variant, both of which reach the report:
// `addCustom()` splits the id on `:` and uses the tail as `provider.config.variant`,
// and it appends the variant to every assertion metric (`Harmful/Custom:<variant>`).
// A variant containing `:`, a path separator or whitespace would put junk into a
// provider id and a metric label, so it is refused rather than sanitized.
var customStrategyIDPattern = regexp.MustCompile(`^custom:([A-Za-z0-9._-]{1,64})$`)

// customStrategyExecutableTokens are reference schemes searched for anywhere in
// the raw YAML. On this promptfoo version a strategy package's *values* are not
// passed through any file loader, so this is defence in depth rather than a
// proven exploit path — stated honestly rather than overclaimed. It exists
// because the moment that changes upstream, a `file://` in a strategy package
// becomes a module import, and this is the only place that sees the raw text
// before it is stored.
var customStrategyExecutableTokens = []string{"file://", "javascript:", "python:", "eval:", "child_process"}

var customStrategyRemoteURLTokens = []string{"http://", "https://"}

// CustomStrategyVariantFor derives the `custom:<variant>` id for a publication
// handle.
//
// The handle is platform-issued and already constrained, so it is used as the
// identity: it is what admin governance acts on, and it is unique per
// publication. An expert-supplied override is honoured only when it produces a
// legal id, so a bad override degrades to the handle-derived id instead of
// producing an id promptfoo would put into a metric label.
func CustomStrategyVariantFor(handle string, metadata map[string]string) string {
	variant := strings.TrimSpace(metadata[CustomStrategyMetadataVariantKey])
	if !customStrategyIDPattern.MatchString("custom:" + variant) {
		variant = sanitizeStrategyVariant(handle)
	}
	return "custom:" + variant
}

// sanitizeStrategyVariant reduces a handle to the characters a variant may
// contain. Anything else becomes a dash, so a handle can never inject a `:` or a
// path separator into the derived id.
func sanitizeStrategyVariant(handle string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(handle)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "strategy"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// IsCustomStrategyRef reports whether a strategy id references a published
// package rather than a built-in catalog entry.
func IsCustomStrategyRef(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), CustomStrategyRefPrefix)
}

// CustomStrategyRefTarget strips the prefix from a package reference.
func CustomStrategyRefTarget(id string) string {
	return strings.TrimSpace(id[len(CustomStrategyRefPrefix):])
}

// CustomStrategyRef renders a package reference for a handle.
func CustomStrategyRef(handle string) string {
	return CustomStrategyRefPrefix + strings.TrimSpace(handle)
}

// ParseCustomStrategyDefinition parses a published package, rejecting anything
// the engine would reject — or, worse, silently accept and ignore.
func ParseCustomStrategyDefinition(payload string) (*PromptfooCustomStrategyDefinition, error) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "payload", "自定义策略内容为空")
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(trimmed), &node); err != nil {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "payload", "自定义策略不是合法的 YAML：%s", err.Error())
	}
	if len(node.Content) == 0 {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "payload", "自定义策略内容为空")
	}
	root := node.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "payload",
			"自定义策略顶层必须是键值映射（strategyText 等），实际是 %s", yamlNodeKindName(root.Kind))
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if !customStrategyAllowedKeys[key] {
			return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, key,
				"自定义策略包含未声明字段 %q。promptfoo 不校验策略 config 的字段名（z.record 会放行任意键并原样转发给 provider），"+
					"拼错的字段不会报错、也不会生效，因此在发布时拦下。允许字段：strategyText、maxTurns、maxBacktracks、stateful、continueAfterSuccess、excludeTargetOutputFromAgenticAttackGeneration。",
				key)
		}
	}
	var def PromptfooCustomStrategyDefinition
	if err := node.Decode(&def); err != nil {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "payload", "自定义策略字段类型不合法：%s", err.Error())
	}
	def.StrategyText = strings.TrimSpace(def.StrategyText)
	if def.StrategyText == "" {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "strategyText",
			"自定义策略缺少 strategyText。这是 promptfoo custom 策略唯一必填的字段：缺失时会在评测进行中抛 "+
				"\"Custom strategy requires strategyText in config\"，评测记录已经落库才发现。")
	}
	// A zero/negative bound would end the attack loop immediately: a zero-turn
	// run that still reports success.
	if def.MaxTurns != nil && *def.MaxTurns < 1 {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "maxTurns",
			"maxTurns 必须是 ≥1 的整数（收到 %d）。设为 0 会让攻击对话立即结束，产生 0 轮交互却仍然报成功。", *def.MaxTurns)
	}
	if def.MaxBacktracks != nil && *def.MaxBacktracks < 1 {
		return nil, customStrategyFieldErrorf(CustomStrategyErrDefinitionInvalid, "maxBacktracks",
			"maxBacktracks 必须是 ≥1 的整数（收到 %d）。", *def.MaxBacktracks)
	}
	return &def, nil
}

// ValidateAndParseCustomStrategyPublication is the pre-publication guard, and
// returns the parsed definition so the caller can store the canonical
// re-serialization of exactly the document that was validated.
//
// Order mirrors U10's, for the same reason found by a test: form and text first,
// because they produce the message the submitter actually needs; shape last among
// the cheap ones, because a valid package is the only case where it is the right
// message.
//
// It is a pure predicate over the request: no I/O, no store access, no dispatch,
// so a rejected package leaves nothing half-created behind it.
func ValidateAndParseCustomStrategyPublication(in EvaluationResourceInput) (*PromptfooCustomStrategyDefinition, string, error) {
	if in.Kind != EvaluationResourceKindPromptfooStrategy {
		return nil, "", nil
	}
	// CODE packages take the other branch entirely: their payload is an ESM
	// module, not the YAML the rest of this function parses.
	if IsCustomStrategyCodeForm(in.Metadata) {
		return nil, "", customStrategyValidationErrorf(CustomStrategyErrCodeInvalid,
			"代码策略必须走 ValidateAndParseCustomStrategyCodePublication。")
	}
	payload := in.Payload
	if len(payload) > CustomStrategyMaxPayloadBytes {
		return nil, "", customStrategyValidationErrorf(CustomStrategyErrPayloadTooLarge,
			"自定义策略体积 %d 字节，超过上限 %d 字节。策略包只应包含攻击方法说明。", len(payload), CustomStrategyMaxPayloadBytes)
	}
	if err := ValidateCustomStrategyForm(in.Metadata, payload); err != nil {
		return nil, "", err
	}
	if err := assertCustomStrategyTextClean(payload); err != nil {
		return nil, "", err
	}
	def, err := ParseCustomStrategyDefinition(payload)
	if err != nil {
		return nil, "", err
	}
	strategyID := CustomStrategyVariantFor(in.Name, in.Metadata)
	if err := validateCustomStrategyID(strategyID); err != nil {
		return nil, "", err
	}
	if err := validateCustomStrategyDependencies(in.Metadata, strategyID); err != nil {
		return nil, "", err
	}
	return def, strategyID, nil
}

// ValidateCustomStrategyPublication is the error-only form, for callers that
// need the verdict and not the definition.
func ValidateCustomStrategyPublication(in EvaluationResourceInput) error {
	_, _, err := ValidateAndParseCustomStrategyPublication(in)
	return err
}

// ValidateCustomStrategyForm rejects any declared or sniffed non-YAML form.
//
// Two independent checks, because they fail differently: the *declared* form is
// authoritative when filled in and is the only signal for a binary; the *sniffed*
// form catches a JavaScript payload submitted while claiming to be YAML, which a
// schema check alone would report as a confusing "expected strategyText, got
// string".
func ValidateCustomStrategyForm(metadata map[string]string, payload string) error {
	declared := strings.ToLower(strings.TrimSpace(metadata[CustomStrategyMetadataFormKey]))
	if declared != "" && declared != CustomStrategyFormYAML {
		return customStrategyValidationErrorf(CustomStrategyErrFormUnsupported,
			"自定义策略首期仅支持 YAML 形态，收到的是 %q 形态。promptfoo 的 file:// 策略只接受 .js/.ts（loadStrategy 会校验 isJavascriptFile），"+
				"即在引擎进程内执行任意代码（引擎持有已物化的生成模型密钥），因此一律拒绝，包括内部提交。", declared)
	}
	if sniffed, ok := sniffCustomPluginExecutableForm(payload); ok {
		return customStrategyValidationErrorf(CustomStrategyErrFormUnsupported,
			"自定义策略首期仅支持 YAML 形态，但提交内容看起来是 %s 形态。file:// 策略在 promptfoo 中只接受 JS/TS，等价于在引擎进程内执行任意代码，因此一律拒绝。", sniffed)
	}
	return nil
}

// assertCustomStrategyTextClean rejects executable references and remote URLs in
// the raw package text.
func assertCustomStrategyTextClean(payload string) error {
	lower := strings.ToLower(payload)
	for _, token := range customStrategyExecutableTokens {
		if strings.Contains(lower, token) {
			return customStrategyValidationErrorf(CustomStrategyErrExecutableReferenceForbiddenForStrategy(),
				"自定义策略内容包含可执行引用 %q。策略包中的可执行引用会被解析为模块导入而非数据，因此一律拒绝。", token)
		}
	}
	for _, token := range customStrategyRemoteURLTokens {
		if strings.Contains(lower, token) {
			return customStrategyValidationErrorf(CustomStrategyErrRemoteReferenceForbidden,
				"自定义策略内容包含外部地址 %q。策略包不允许自行指定端点：生成与判定只走平台下发的模型端点（redteamProvider 由引擎侧注入，不接受包内指定）。", token)
		}
	}
	return nil
}

// CustomStrategyErrExecutableReferenceForbidden is the strategy-side code for a
// `file://`-style reference in a package body. Separate from the plugin code
// because the two are grepped independently by operators.
const CustomStrategyErrExecutableReferenceForbidden = "pf_strategy_executable_reference_forbidden"

// CustomStrategyErrExecutableReferenceForbiddenForStrategy returns that code.
// A function rather than a bare constant so the call site reads as a decision.
func CustomStrategyErrExecutableReferenceForbiddenForStrategy() string {
	return CustomStrategyErrExecutableReferenceForbidden
}

// validateCustomStrategyID refuses an id that collides with a built-in catalog
// strategy.
//
// The report resolves every strategy through the catalog, and
// `metadata.strategyId` is written verbatim by `addCustom()` — so a collision
// would file an expert package's probes under a built-in's name, exactly the
// borrowing U10 refuses for plugins.
func validateCustomStrategyID(strategyID string) error {
	if !PromptfooCatalogStrategyExists(strategyID) {
		return nil
	}
	return customStrategyFieldErrorf(CustomStrategyErrIDConflictsCatalog, "id",
		"自定义策略 id %q 与内置目录中的策略同名。同名会让报告把该策略的探针归到内置策略名下。请换一个变体名（可在元数据 pf_strategy_variant 中指定）。", strategyID)
}

// validateCustomStrategyDependencies runs U6's guardrail over what the package
// declares, reusing U6's table rather than reimplementing it: a second copy of
// "which capabilities need remote generation" is exactly the kind of table that
// drifts silently.
func validateCustomStrategyDependencies(metadata map[string]string, strategyID string) error {
	plugins := splitCustomPluginDependencyList(metadata[CustomStrategyMetadataDepsKey])
	if len(plugins) == 0 {
		return nil
	}
	// The strategy id itself is included so a package cannot smuggle a gated
	// native strategy id in as its own name.
	refs := customPluginDependencyRefs(append(plugins, strategyID))
	if err := ValidateEngineRunRemoteGeneration(refs, nil); err != nil {
		var guard *EngineRemoteDepGuardError
		if errors.As(err, &guard) {
			return customStrategyValidationErrorf(CustomStrategyErrRemoteGenerationForbidden,
				"自定义策略声明了依赖远程生成的插件，发布被拒绝。%s", guard.Error())
		}
		return err
	}
	return nil
}

// CustomStrategyPublicationMetadata builds the metadata a published package
// carries, so the admin governance view and the run report can be reconciled
// without decrypting the payload (不展示资源正文).
func CustomStrategyPublicationMetadata(strategyID string, base map[string]string) map[string]string {
	out := make(map[string]string, len(base)+3)
	for k, v := range base {
		out[k] = v
	}
	// The form is CARRIED, not forced. It used to be written as yaml
	// unconditionally; with CODE packages (`pf_strategy_form=js`) that would
	// relabel a module as YAML at the storage exit, and the resolver would then
	// hand an ESM source to the `custom:<variant>` inliner (design §3.1).
	form := strings.ToLower(strings.TrimSpace(base[CustomStrategyMetadataFormKey]))
	if form == "" {
		form = CustomStrategyFormYAML
	}
	out[CustomStrategyMetadataFormKey] = form
	out["pf_strategy_id"] = strategyID
	return out
}

// marshalCustomStrategyDefinition re-serializes a validated definition.
//
// The stored payload is this canonical form rather than the submitter's raw
// text, so the document the engine loads is byte-identical to the one validation
// inspected — the same gap U7 hit with `applicationDefinition`, and the reason
// `14` §5 rule 1 exists. Field order is fixed by the struct so republishing an
// unchanged package produces an unchanged payload, keeping version comparison
// meaningful.
func marshalCustomStrategyDefinition(def *PromptfooCustomStrategyDefinition) (string, error) {
	if def == nil {
		return "", customStrategyValidationErrorf(CustomStrategyErrDefinitionInvalid, "自定义策略内容为空")
	}
	out, err := yaml.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("serialize promptfoo custom strategy definition: %w", err)
	}
	return string(out), nil
}

// customStrategyValidationErrorf builds a validation error without a field.
func customStrategyValidationErrorf(code, format string, args ...any) *PromptfooCustomPluginValidationError {
	return &PromptfooCustomPluginValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// customStrategyFieldErrorf builds a validation error naming one field.
//
// The error type is shared with U10's plugin path deliberately: both carry a
// machine-readable `Code`, a `Message` and an optional `Field`, and the HTTP
// layer already knows how to render one. The codes are distinct strings, so
// nothing downstream conflates a plugin rejection with a strategy rejection.
func customStrategyFieldErrorf(code, field, format string, args ...any) *PromptfooCustomPluginValidationError {
	return &PromptfooCustomPluginValidationError{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}
