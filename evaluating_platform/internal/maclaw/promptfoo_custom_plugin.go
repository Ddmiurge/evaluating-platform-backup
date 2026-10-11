package maclaw

// promptfoo_custom_plugin.go — U10 · expert-authored promptfoo plugin packages.
//
// # What this file is
//
// U6 made a silent failure visible: a plugin whose attack cases promptfoo can
// only fetch from its remote generation service used to produce a green run
// with zero coverage for that plugin. U10 opens the other end of the catalog —
// experts author their own plugin packages and publish them straight from the
// expert portal (承 `06` D-03: 取消「管理员审核」作为发布前置门, admin governs
// afterwards instead of gating beforehand) — and that makes a *new* silent
// failure shape possible, which is what this file exists to prevent:
//
//   1. **A package promptfoo cannot execute.** promptfoo's custom-plugin
//      contract (`CustomPluginDefinitionSchema`, dist/src/index.js:14985) is a
//      **strictObject** with exactly five keys. A package missing `grader`, or
//      carrying a sixth key, is not "degraded" — `loadCustomPluginDefinition`
//      throws, promptfoo catches it per-plugin
//      (`logger.error('Error generating tests for custom plugin …')`) and
//      carries on with the remaining plugins. The run finishes green with the
//      expert's capability silently absent. Verified in the dist, not assumed:
//      the `try { … } catch (e) { logger.error(…) }` around the `file://`
//      branch leaves `pluginResults[displayId].generated = 0`.
//
//   2. **A JS/script package.** `file://x.js` is not a plugin definition, it is
//      an **import**. `maybeLoadFromExternalFile` keeps a `file://….js[:fn]`
//      reference verbatim and the provider/assertion loaders
//      (`loadCallbackFromFileUrl`) `import()` it — arbitrary code execution in
//      the engine process, which holds the materialized generation API key.
//      The design doc (`14` §2 U10 ③) puts this out of scope for the first
//      release; this file is where that decision is *enforced*, and it is
//      enforced on the storage exit rather than in a UI dropdown.
//
//   3. **A package that claims a remote-gated capability.** A package whose id
//      (or declared dependency) names a plugin promptfoo can only generate
//      remotely inherits exactly the U6 failure: green run, zero coverage. So
//      publication runs the *same* U6 guardrail (`ValidateEngineRunRemoteGeneration`)
//      rather than a second, hand-written copy of the mapping.
//
// # Where the guard lives
//
// In `ResourceStoreService.SaveResource` — the single exit every write passes
// through (expert publish, admin re-publish, shadow copy). A guard placed in
// the HTTP handler alone would be defeated by the shadow-copy path, which
// re-saves a published package through the same store with no handler in the
// path at all. U8's lesson (接线写在 helper 里, 摘掉接线测试仍全绿) is the
// reason this is stated explicitly rather than left to convention.
//
// # What is deliberately NOT claimed here
//
// This file validates *shape and provenance*. It does not and cannot prove the
// sandbox: read-only root filesystem, non-root uid and egress restriction are
// container-runtime properties (see `promptfoo_engine/Dockerfile`,
// `docker-compose.yml`, and `promptfoo_engine/src/sandbox.ts`). A perfectly
// valid YAML package still runs with whatever privileges the engine process
// has, so the sandbox is a separate, separately-verified control.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// EvaluationResourceKindPromptfooPlugin is the `resource_kind` an
// expert-authored promptfoo plugin package is stored under. Reuses
// `maclaw_resources` (encrypted payload) and the publication/shadow
// distribution tables rather than introducing a parallel store.
const EvaluationResourceKindPromptfooPlugin EvaluationResourceKind = "pf_plugin"

// Error codes surfaced to the browser and grepped by operators. Part of the API
// contract: the expert portal maps each to a readable message, and none of them
// may surface as a bare 500.
const (
	// CustomPluginErrFormUnsupported means the package was submitted in a form
	// other than YAML. JS/脚本 = 任意代码执行, out of scope for the first
	// release regardless of whether the submitter is internal.
	CustomPluginErrFormUnsupported = "pf_plugin_form_unsupported"
	// CustomPluginErrDefinitionInvalid means the YAML does not satisfy
	// promptfoo's custom-plugin schema (missing/blank generator or grader,
	// wrong type for an optional field, or an undeclared extra key — promptfoo
	// parses it with a **strictObject**, so an unknown key is a hard failure,
	// not a forward-compatible extension).
	CustomPluginErrDefinitionInvalid = "pf_plugin_definition_invalid"
	// CustomPluginErrIDConflictsCatalog means the package claims the id of a
	// built-in catalog plugin. Allowed to collide it would make every report
	// line ambiguous: `metadata.pluginId` would be attributed to the built-in's
	// category, severity and rubric, so a weak expert package could borrow a
	// built-in's standing.
	CustomPluginErrIDConflictsCatalog = "pf_plugin_id_conflicts_catalog"
	// CustomPluginErrRemoteGenerationForbidden wraps U6's code. Raised when the
	// package declares a dependency on a remote-generation-gated plugin or
	// strategy, or when it claims a gated id as its own.
	CustomPluginErrRemoteGenerationForbidden = EngineErrPluginRequiresRemoteGeneration
	// CustomPluginErrExecutableReferenceForbidden means the YAML text carries a
	// `file://` / `javascript:` / `python:` reference. Defense in depth, stated
	// honestly: on promptfoo 0.123.0 the *definition values* are not themselves
	// passed through the file loader, so this is not a proven exploit path on
	// this version. It exists because the moment that changes upstream the
	// payload becomes an arbitrary-file-read primitive, and this is the only
	// place that sees the raw text before it is stored.
	CustomPluginErrExecutableReferenceForbidden = "pf_plugin_executable_reference_forbidden"
	// CustomPluginErrPayloadTooLarge means the package exceeds
	// CustomPluginMaxPayloadBytes.
	CustomPluginErrPayloadTooLarge = "pf_plugin_payload_too_large"
	// CustomPluginErrRemoteReferenceForbidden means the YAML text carries an
	// absolute http(s) URL. A plugin package has no legitimate reason to name
	// an endpoint: promptfoo's custom plugin talks only to the generation
	// provider the platform hands it.
	CustomPluginErrRemoteReferenceForbidden = "pf_plugin_remote_reference_forbidden"
)

// CustomPluginMaxPayloadBytes caps a published package. Generous for a prompt
// and a grader, far below anything that would suggest an embedded corpus.
const CustomPluginMaxPayloadBytes = 256 * 1024

// CustomPluginFormYAML is the only accepted form in the first release.
const CustomPluginFormYAML = "yaml"

// CustomPluginMetadataFormKey lets a submitter state the form explicitly so a
// wrong form produces "only YAML is accepted" rather than a schema complaint
// about their JavaScript.
const CustomPluginMetadataFormKey = "pf_plugin_form"

// CustomPluginMetadataIDKey records the package id the publication resolved to,
// so the admin governance view can show it without decrypting the payload (the
// admin resource list is metadata-only by contract — 不展示资源正文).
const CustomPluginMetadataIDKey = "pf_plugin_id"

// CustomPluginMetadataDepsKey is a comma-separated list of catalog plugin ids
// the package wraps or delegates to. Checked against U6's table: naming a
// gated plugin here is the package declaring "my attack cases come from
// promptfoo's remote service", which in this deployment means zero coverage.
const CustomPluginMetadataDepsKey = "pf_plugin_depends_on"

// CustomPluginRefPrefix marks a plugin id as a reference to a published
// package rather than a built-in catalog id. The engine resolves the prefix to
// a materialized file inside the per-run workdir.
const CustomPluginRefPrefix = "pf_plugin:"

// PromptfooCustomPluginDefinition mirrors promptfoo's
// `CustomPluginDefinitionSchema` (dist/src/index.js:14985) exactly — same five
// keys, same optionality. The YAML tags are pinned so a Go field rename cannot
// silently change the wire shape, and the parser below additionally rejects
// undeclared keys because promptfoo's schema is a strictObject.
type PromptfooCustomPluginDefinition struct {
	// Generator is the nunjucks template promptfoo renders to synthesize the
	// attack case. Required, non-blank.
	Generator string `yaml:"generator"`
	// Grader is the llm-rubric question the target's response is judged by.
	// Required, non-blank.
	Grader string `yaml:"grader"`
	// Threshold optionally overrides the pass threshold for the assertion.
	Threshold *float64 `yaml:"threshold,omitempty"`
	// Metric names the assertion's metric in the report.
	Metric string `yaml:"metric,omitempty"`
	// ID is the plugin's own display id. Optional upstream; when absent
	// promptfoo falls back to `promptfoo:redteam:custom`.
	ID string `yaml:"id,omitempty"`
}

// PromptfooCustomPluginValidationError is a publication rejection carrying a
// machine-readable code plus a message that names the actual problem. A
// validation failure must never reach the browser as a bare 500: the submitter
// is an expert trying to ship something, and "something went wrong" gives them
// nothing to act on.
type PromptfooCustomPluginValidationError struct {
	Code    string
	Message string
	// Field names the offending part of the package when one can be
	// identified (`generator`, `grader`, `id`, …). Empty when the rejection is
	// about the package as a whole.
	Field string
}

// Error implements error.
func (e *PromptfooCustomPluginValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Field != "" {
		return fmt.Sprintf("%s（字段 %s）", e.Message, e.Field)
	}
	return e.Message
}

// customPluginValidationErrorf builds a validation error without a field.
func customPluginValidationErrorf(code, format string, args ...any) *PromptfooCustomPluginValidationError {
	return &PromptfooCustomPluginValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// customPluginFieldErrorf builds a validation error naming one field.
func customPluginFieldErrorf(code, field, format string, args ...any) *PromptfooCustomPluginValidationError {
	return &PromptfooCustomPluginValidationError{Code: code, Field: field, Message: fmt.Sprintf(format, args...)}
}

// CustomPluginUnsupportedForms are the forms rejected by name. Exported so the
// expert portal can list them ("首期仅支持 YAML") instead of leaving a submitter
// to discover the limit by being refused.
var CustomPluginUnsupportedForms = []string{"javascript", "js", "mjs", "cjs", "ts", "python", "py", "sh", "shell", "bash", "exe", "binary"}

// ValidateCustomPluginForm rejects any declared or sniffed non-YAML form.
//
// Two independent checks, because they fail differently:
//   - the *declared* form (metadata `pf_plugin_form`) — authoritative when the
//     submitter filled it in, and the only signal available for a binary or a
//     Python file that happens to parse as YAML;
//   - the *sniffed* form — catches a JavaScript payload submitted while
//     claiming to be YAML, which is the case a schema check alone would report
//     as a confusing "expected generator, got string".
func ValidateCustomPluginForm(metadata map[string]string, payload string) error {
	declared := strings.ToLower(strings.TrimSpace(metadata[CustomPluginMetadataFormKey]))
	if declared != "" && declared != CustomPluginFormYAML {
		return customPluginValidationErrorf(
			CustomPluginErrFormUnsupported,
			"自定义插件首期仅支持 YAML 形态，收到的是 %q 形态。JS/脚本形态会在引擎进程内执行任意代码（引擎持有已物化的生成模型密钥），因此一律拒绝，包括内部提交。",
			declared,
		)
	}
	if sniffed, ok := sniffCustomPluginExecutableForm(payload); ok {
		return customPluginValidationErrorf(
			CustomPluginErrFormUnsupported,
			"自定义插件首期仅支持 YAML 形态，但提交内容看起来是 %s 形态。JS/脚本形态会在引擎进程内执行任意代码，因此一律拒绝，包括内部提交。",
			sniffed,
		)
	}
	return nil
}

// sniffCustomPluginExecutableForm reports the executable form a payload looks
// like, if any.
//
// Scoped deliberately to **whole-payload** signatures only: a shebang, and the
// ECMAScript module / CommonJS / Python statement forms. Reference *schemes*
// (`file://…`, `javascript:`, `python:`) are NOT sniffed here even though they
// are executable — they are caught by `assertCustomPluginTextClean`, which owns
// that vocabulary and produces the more precise message ("包含可执行引用
// "file://"；该引用在 promptfoo 中会被解析为模块导入而非数据"). Listing them in both
// places meant whichever check ran first won, and the two messages disagreed
// about what the problem was — which is the kind of ambiguity that makes a
// rejection hard to act on.
//
// A YAML document cannot begin with any of these, so there is no false-positive
// risk against a legitimate package; the check exists because the payload is
// stored before anything has looked at it.
func sniffCustomPluginExecutableForm(payload string) (string, bool) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(trimmed, "#!"):
		return "脚本（shebang）", true
	case strings.HasPrefix(trimmed, "import "),
		strings.HasPrefix(trimmed, "export "),
		strings.HasPrefix(trimmed, "module.exports"),
		strings.HasPrefix(trimmed, "require("):
		return "JavaScript", true
	case strings.HasPrefix(trimmed, "def ") && strings.Contains(lower, "python"):
		return "Python", true
	}
	return "", false
}

// ParseCustomPluginDefinition parses a published package into promptfoo's
// schema, rejecting anything promptfoo's strictObject would reject.
//
// # Why the schema is enforced here rather than left to promptfoo
//
// promptfoo's own failure mode is a per-plugin `try/catch` that logs and
// continues (see the file header). A package that gets past this function is
// therefore loadable; a package that does not get past it would degrade a run
// silently rather than fail it.
func ParseCustomPluginDefinition(payload string) (*PromptfooCustomPluginDefinition, error) {
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, "payload", "自定义插件内容为空")
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(trimmed), &node); err != nil {
		return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, "payload", "自定义插件不是合法的 YAML：%s", err.Error())
	}
	if len(node.Content) == 0 {
		return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, "payload", "自定义插件内容为空")
	}
	root := node.Content[0]
	// A YAML sequence or scalar is a valid *document* but not a valid plugin
	// definition; saying so beats zod's "expected object, received array".
	if root.Kind != yaml.MappingNode {
		return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, "payload",
			"自定义插件顶层必须是键值映射（generator / grader / threshold / metric / id），实际是 %s", yamlNodeKindName(root.Kind))
	}

	// promptfoo parses with a strictObject, so an undeclared key is a hard
	// failure at load time. Reject it at publish time with the key named —
	// otherwise the expert ships a package that loads fine locally and drops
	// out of the middle of a production run.
	declared := make(map[string]bool, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		declared[key] = true
		if !customPluginAllowedKeys[key] {
			return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, key,
				"自定义插件包含未声明字段 %q。promptfoo 以 strictObject 解析插件定义，未声明字段会在运行时被直接拒绝（且只记一条 error 日志、整次评测仍返回成功），因此必须在发布时拦下。允许字段：generator、grader、threshold、metric、id。",
				key)
		}
	}

	var def PromptfooCustomPluginDefinition
	if err := node.Decode(&def); err != nil {
		return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, "payload", "自定义插件字段类型不合法：%s", err.Error())
	}
	def.Generator = strings.TrimSpace(def.Generator)
	def.Grader = strings.TrimSpace(def.Grader)
	def.Metric = strings.TrimSpace(def.Metric)
	def.ID = strings.TrimSpace(def.ID)

	if def.Generator == "" {
		return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, "generator",
			"自定义插件缺少 generator（生成攻击用例的模板）。promptfoo 的插件定义要求 generator 与 grader 均非空。")
	}
	if def.Grader == "" {
		return nil, customPluginFieldErrorf(CustomPluginErrDefinitionInvalid, "grader",
			"自定义插件缺少 grader（判定目标响应的 rubric）。promptfoo 的插件定义要求 generator 与 grader 均非空。")
	}
	return &def, nil
}

// customPluginAllowedKeys is the exact key set promptfoo's strictObject
// declares. Kept as a literal rather than derived from the struct so a field
// added to the Go struct for Go-side convenience cannot widen what we accept —
// accepting a key promptfoo then rejects is the failure this whole file exists
// to prevent.
var customPluginAllowedKeys = map[string]bool{
	"generator": true,
	"grader":    true,
	"threshold": true,
	"metric":    true,
	"id":        true,
}

// yamlNodeKindName renders a yaml kind for an error message.
func yamlNodeKindName(kind yaml.Kind) string {
	switch kind {
	case yaml.SequenceNode:
		return "列表"
	case yaml.ScalarNode:
		return "单个标量"
	case yaml.AliasNode:
		return "锚点别名"
	case yaml.DocumentNode:
		return "文档"
	default:
		return "未知结构"
	}
}

// customPluginExecutableReferenceTokens are the reference schemes searched for
// anywhere in the raw YAML text. See CustomPluginErrExecutableReferenceForbidden
// for why this is defense in depth rather than a proven exploit.
var customPluginExecutableReferenceTokens = []string{"file://", "javascript:", "python:", "fn:", "eval:", "child_process"}

// customPluginRemoteURLPattern matches an absolute http(s) URL. A custom plugin
// has no reason to name one: promptfoo's CustomPlugin talks only to the
// generation provider the platform hands it, and any URL in the package is at
// best a copy-paste of a vendor's example.
var customPluginRemoteURLTokens = []string{"http://", "https://"}

// assertCustomPluginTextClean rejects executable references and remote URLs in
// the raw package text.
func assertCustomPluginTextClean(payload string) error {
	lower := strings.ToLower(payload)
	for _, token := range customPluginExecutableReferenceTokens {
		if strings.Contains(lower, token) {
			return customPluginValidationErrorf(CustomPluginErrExecutableReferenceForbidden,
				"自定义插件内容包含可执行引用 %q。该引用在 promptfoo 中会被解析为模块导入而非数据，等价于在引擎进程内执行任意代码，因此一律拒绝。", token)
		}
	}
	for _, token := range customPluginRemoteURLTokens {
		if strings.Contains(lower, token) {
			return customPluginValidationErrorf(CustomPluginErrRemoteReferenceForbidden,
				"自定义插件内容包含外部地址 %q。插件包不允许自行指定端点：生成与判定只走平台下发的模型端点。", token)
		}
	}
	return nil
}

// ValidateAndParseCustomPluginPublication is the U10 pre-publication guard, and
// returns the parsed definition so the caller can store the canonical
// re-serialization of exactly the document that was validated.
//
// # Order matters, and it was found by a test rather than by reading
//
// The first version of `SaveResource` parsed first and validated second. That
// made every non-YAML payload fail with a *schema* message — "自定义插件包含
// 未声明字段 \"module.exports = { generator\"" — because `module.exports = …`
// happens to parse as a YAML mapping with one weird key. The expert who
// submitted JavaScript was told their YAML had an unknown field, which is both
// wrong and useless. `TestCustomPluginJSFormIsRejectedWithReadableReason`
// caught it by asserting the *code*, not just that an error occurred.
//
// So the order is: form → size → text → shape → id → remote dependency. Form
// and text first, because they produce the message the submitter actually needs
// ("首期仅支持 YAML 形态", "不允许 file:// 引用"). Shape last among the cheap
// ones, because a valid package is the only case where it is the right message.
//
// It is a pure predicate over the request: no I/O, no store access, no
// dispatch, so a rejected package leaves nothing half-created behind it.
func ValidateAndParseCustomPluginPublication(in EvaluationResourceInput) (*PromptfooCustomPluginDefinition, error) {
	if in.Kind != EvaluationResourceKindPromptfooPlugin {
		return nil, nil
	}
	payload := in.Payload
	if len(payload) > CustomPluginMaxPayloadBytes {
		return nil, customPluginValidationErrorf(CustomPluginErrPayloadTooLarge,
			"自定义插件体积 %d 字节，超过上限 %d 字节。插件包只应包含生成模板与判定 rubric。", len(payload), CustomPluginMaxPayloadBytes)
	}
	// Form first: a non-YAML payload must be told "only YAML", not "your YAML
	// has an unknown field".
	if err := ValidateCustomPluginForm(in.Metadata, payload); err != nil {
		return nil, err
	}
	if err := assertCustomPluginTextClean(payload); err != nil {
		return nil, err
	}
	def, err := ParseCustomPluginDefinition(payload)
	if err != nil {
		return nil, err
	}
	if err := validateCustomPluginID(def.ID); err != nil {
		return nil, err
	}
	if err := validateCustomPluginDependencies(in.Metadata, def.ID); err != nil {
		return nil, err
	}
	return def, nil
}

// ValidateCustomPluginPublication is the error-only form of
// ValidateAndParseCustomPluginPublication, for callers that need the verdict
// and not the definition (the HTTP handler rejecting a request, for instance).
func ValidateCustomPluginPublication(in EvaluationResourceInput) error {
	_, err := ValidateAndParseCustomPluginPublication(in)
	return err
}

// validateCustomPluginID refuses an id that collides with a built-in catalog
// plugin.
//
// Two reasons, and the second is the one that bites:
//
//  1. promptfoo reports `metadata.pluginId = plugin.id` — but on the custom
//     path the *package's* `id` field is what the plugin instance reports
//     (`get id() { return this.definition.id || 'promptfoo:redteam:custom' }`),
//     so a colliding id is written straight into the report's plugin_stats.
//  2. The reducer classifies every plugin by looking its id up in the backend
//     catalog (U2/T2.2: an unknown id degrades to other/medium, never guessed).
//     A colliding id would therefore be credited with the built-in's category,
//     severity and — since U8 — the built-in family's rubric. An expert package
//     could ship a weak grader and have its probes reported under a strong
//     built-in's name. That is a correctness hole in the report, not a naming
//     preference, so it is refused.
func validateCustomPluginID(id string) error {
	if id == "" {
		return nil
	}
	if !PromptfooCatalogPluginExists(id) {
		return nil
	}
	return customPluginFieldErrorf(CustomPluginErrIDConflictsCatalog, "id",
		"自定义插件 id %q 与内置目录中的插件同名。同名会让报告把该插件的探针归到内置插件的风险分类/严重级别/判定标准下，等于借用内置插件的信誉。请换一个 id（建议加机构或场景前缀）。", id)
}

// validateCustomPluginDependencies runs U6's guardrail over what the package
// declares, so a package cannot reintroduce the silent-zero-coverage shape by
// proxy: naming a gated plugin in `pf_plugin_depends_on`, or claiming a gated
// plugin id as its own.
//
// The mapping table is U6's, reused rather than reimplemented — a second copy
// of "which plugins need remote generation" is exactly the kind of table that
// drifts silently.
func validateCustomPluginDependencies(metadata map[string]string, id string) error {
	plugins := splitCustomPluginDependencyList(metadata[CustomPluginMetadataDepsKey])
	if id != "" {
		plugins = append(plugins, id)
	}
	strategies := splitCustomPluginDependencyList(metadata["pf_plugin_depends_on_strategies"])
	if len(plugins) == 0 && len(strategies) == 0 {
		return nil
	}
	if err := ValidateEngineRunRemoteGeneration(
		customPluginDependencyRefs(plugins),
		customPluginDependencyRefs(strategies),
	); err != nil {
		var guard *EngineRemoteDepGuardError
		if errors.As(err, &guard) {
			return customPluginValidationErrorf(CustomPluginErrRemoteGenerationForbidden,
				"自定义插件声明了依赖远程生成的插件/策略，发布被拒绝。%s", guard.Error())
		}
		return err
	}
	return nil
}

// splitCustomPluginDependencyList parses a comma-separated dependency list.
func splitCustomPluginDependencyList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return out
}

// customPluginDependencyRefs wraps ids as capability refs for U6's validator.
func customPluginDependencyRefs(ids []string) []EngineCapabilityRef {
	refs := make([]EngineCapabilityRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, EngineCapabilityRef{ID: id})
	}
	return refs
}

// CustomPluginPublicationMetadata builds the metadata a published package
// carries: its resolved id plus the fields the publication flow needs to
// re-validate without decrypting. Returned as a fresh map — the caller may
// still be holding the request's map.
func CustomPluginPublicationMetadata(def *PromptfooCustomPluginDefinition, base map[string]string) map[string]string {
	out := make(map[string]string, len(base)+3)
	for k, v := range base {
		out[k] = v
	}
	if def != nil {
		if def.ID != "" {
			out[CustomPluginMetadataIDKey] = def.ID
		} else {
			out[CustomPluginMetadataIDKey] = PromptfooCustomPluginDefaultID
		}
	}
	out[CustomPluginMetadataFormKey] = CustomPluginFormYAML
	return out
}

// PromptfooCustomPluginDefaultID is the id promptfoo falls back to when a
// package omits its own (`this.definition.id || 'promptfoo:redteam:custom'`).
// Recorded in the publication metadata so the admin view and the run report
// agree on what an unnamed package is called, instead of one side showing
// `promptfoo:redteam:custom` and the other a blank.
const PromptfooCustomPluginDefaultID = "promptfoo:redteam:custom"

// IsCustomPluginRef reports whether a plugin id references a published package
// rather than a built-in catalog entry.
func IsCustomPluginRef(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), CustomPluginRefPrefix)
}

// CustomPluginRefTarget strips the prefix from a package reference.
func CustomPluginRefTarget(id string) string {
	return strings.TrimSpace(id[len(CustomPluginRefPrefix):])
}

// CustomPluginRef renders a package reference for a handle.
func CustomPluginRef(handle string) string {
	return CustomPluginRefPrefix + strings.TrimSpace(handle)
}

// marshalCustomPluginDefinition re-serializes a validated definition.
//
// The stored payload is this canonical form rather than the submitter's raw
// text, so the document the engine loads is byte-identical to the one
// validation inspected. Storing raw text would leave a gap between "what we
// checked" and "what runs" — the same class of bug U7 hit when promptfoo's zod
// silently stripped an undeclared `applicationDefinition` key, and the one
// discipline in `14` §5 rule 1 exists to prevent.
//
// Field order is fixed (generator, grader, then the optionals) so republishing
// an unchanged package produces an unchanged payload, which keeps the
// publication's version comparison meaningful.
func marshalCustomPluginDefinition(def *PromptfooCustomPluginDefinition) (string, error) {
	if def == nil {
		return "", customPluginValidationErrorf(CustomPluginErrDefinitionInvalid, "自定义插件内容为空")
	}
	out, err := yaml.Marshal(def)
	if err != nil {
		return "", fmt.Errorf("serialize promptfoo custom plugin definition: %w", err)
	}
	return string(out), nil
}
