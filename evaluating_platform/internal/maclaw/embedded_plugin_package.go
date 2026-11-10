// Embedded-plugin package validation (P28, docs/architecture/embedded-plugin-track.md).
//
// This is a PURE predicate over the uploaded zip: no store access, no engine
// probe — so a refused package leaves no row and no file, and the same
// function can run at the storage exit (authoritative, U10 placement) and
// wherever a cheap pre-check saves a round trip. The engine probe that runs
// the package's own tests lives in SaveResource, next to the code-strategy
// probe, for the same reason: only the engine can execute `node tests/run.mjs`.
package maclaw

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"

	appcrypto "evaluating_platform/internal/crypto"

	yaml "gopkg.in/yaml.v3"
)

const (
	// EmbeddedPluginMaxZipBytes: zip on the wire (base64 expands 4/3, the
	// handler's read cap accounts for that separately).
	EmbeddedPluginMaxZipBytes = 512 * 1024
	// EmbeddedPluginMaxUncompressed: decompression-bomb guard.
	EmbeddedPluginMaxUncompressed = 4 * 1024 * 1024
	// EmbeddedPluginMaxFiles: archive breadth guard.
	EmbeddedPluginMaxFiles = 64
	// EmbeddedPluginTestEntry: the ONE agreed test command contract — zero
	// dependency, offline, seconds. Packages needing npm install or network are
	// refused at upload (2026-11-10 decision: friction-loop feedback must be
	// seconds, not a build batch).
	EmbeddedPluginTestEntry = "tests/run.mjs"
)

// EvaluationResourceKindPromptfooEmbeddedPlugin: a NEW kind on purpose —
// pf_plugin flows into the enterprise catalogue / shadow projection / expert
// cross-browse; an embedded package pre-bake must flow NOWHERE (only the
// approved-and-baked id enters the plugin catalog, as a builtin-style id).
const EvaluationResourceKindPromptfooEmbeddedPlugin EvaluationResourceKind = "pf_plugin_embedded"

var embeddedPluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)

// EmbeddedManifest mirrors the doc §3.2 contract.
type EmbeddedManifest struct {
	ID            string `yaml:"id" json:"id"`
	Name          string `yaml:"name" json:"name"`
	Category      string `yaml:"category" json:"category"`
	Severity      string `yaml:"severity" json:"severity"`
	PromptfooBase string `yaml:"promptfoo_base" json:"promptfoo_base"`
	Entry         string `yaml:"entry" json:"entry"`
	Tests         string `yaml:"tests" json:"tests"`
	Summary       string `yaml:"summary" json:"summary"`
}

// EmbeddedPluginValidationError is the refusal type surfaced through
// writeMaclawError / writeEnginePreflightRejection style handling: a code the
// UI maps to an actionable hint, never a bare 500.
type EmbeddedPluginValidationError struct {
	Code    string
	Message string
}

func (e *EmbeddedPluginValidationError) Error() string { return e.Message }

func embeddedInvalid(code, format string, args ...any) *EmbeddedPluginValidationError {
	return &EmbeddedPluginValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ValidateEmbeddedPluginPackage: zip safety → manifest → capability tokens →
// id rules. Returns the parsed manifest so callers reuse one parse.
func ValidateEmbeddedPluginPackage(data []byte) (*EmbeddedManifest, error) {
	if len(data) == 0 {
		return nil, embeddedInvalid("embedded_empty", "上传的包为空。")
	}
	if len(data) > EmbeddedPluginMaxZipBytes {
		return nil, embeddedInvalid("embedded_too_large", "压缩包 %d 字节超过上限 %d 字节。", len(data), EmbeddedPluginMaxZipBytes)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, embeddedInvalid("embedded_not_zip", "不是有效的 zip 文件（%s）。", err.Error())
	}
	if len(zr.File) == 0 || len(zr.File) > EmbeddedPluginMaxFiles {
		return nil, embeddedInvalid("embedded_file_count", "包内文件数必须在 1..%d 之间，实际 %d。", EmbeddedPluginMaxFiles, len(zr.File))
	}

	var manifestRaw []byte
	var totalUncompressed int64
	var textFiles []struct{ name, content string }
	for _, f := range zr.File {
		name := f.Name
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return nil, embeddedInvalid("embedded_zip_slip", "包内路径非法（%s）：不允许绝对路径与 ..。", name)
		}
		if f.Mode()&0o120000 != 0 { // symlink / device bits
			return nil, embeddedInvalid("embedded_zip_link", "包内不允许符号链接或特殊文件（%s）。", name)
		}
		if f.UncompressedSize64 > EmbeddedPluginMaxUncompressed {
			return nil, embeddedInvalid("embedded_zip_bomb", "解压后体积超过 %d 字节上限（%s）。", EmbeddedPluginMaxUncompressed, name)
		}
		totalUncompressed += int64(f.UncompressedSize64)
		if totalUncompressed > EmbeddedPluginMaxUncompressed {
			return nil, embeddedInvalid("embedded_zip_bomb", "解压总大小超过 %d 字节上限。", EmbeddedPluginMaxUncompressed)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, embeddedInvalid("embedded_zip_read", "无法读取包内文件（%s）。", name)
		}
		content, err := io.ReadAll(io.LimitReader(rc, EmbeddedPluginMaxUncompressed+1))
		rc.Close()
		if err != nil {
			return nil, embeddedInvalid("embedded_zip_read", "无法读取包内文件（%s）。", name)
		}
		clean := path.Clean(name)
		if clean == "manifest.yaml" {
			manifestRaw = content
		}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".mjs") {
			textFiles = append(textFiles, struct{ name, content string }{clean, string(content)})
		}
	}
	if manifestRaw == nil {
		return nil, embeddedInvalid("embedded_no_manifest", "缺少根目录 manifest.yaml。")
	}

	manifest, err := parseEmbeddedManifest(manifestRaw)
	if err != nil {
		return nil, err
	}

	// Capability tokens: the SAME refused set as code strategies — this code
	// runs inside the key-holding engine process, so the bar is identical.
	for _, tf := range textFiles {
		if forbidden := capabilityTokenHit(tf.content); forbidden != "" {
			return nil, embeddedInvalid("embedded_forbidden_token",
				"文件 %s 含禁止的能力令牌（%s）：内嵌插件与代码策略同禁 env/文件/进程/网络。", tf.name, forbidden)
		}
	}

	// entry + tests contract
	if manifest.Entry == "" || !hasFile(zr, manifest.Entry) {
		return nil, embeddedInvalid("embedded_entry_missing", "manifest.entry=%q 在包内不存在。", manifest.Entry)
	}
	if manifest.Tests != EmbeddedPluginTestEntry || !hasFile(zr, EmbeddedPluginTestEntry) {
		return nil, embeddedInvalid("embedded_tests_contract",
			"包内测试必须是 %s（零依赖、离线可跑，node 子进程秒级执行）。", EmbeddedPluginTestEntry)
	}
	return manifest, nil
}

func hasFile(zr *zip.Reader, want string) bool {
	for _, f := range zr.File {
		if path.Clean(f.Name) == path.Clean(want) && !f.FileInfo().IsDir() {
			return true
		}
	}
	return false
}

// ValidateEmbeddedManifestForCatalog: id/category/severity/base rules —
// separated because id-conflict checks need the store (done at the exit with
// the catalog in hand), while the rest is pure.
func ValidateEmbeddedManifestForCatalog(m *EmbeddedManifest, builtinOrReserved func(id string) bool) error {
	if !embeddedPluginIDPattern.MatchString(m.ID) {
		return embeddedInvalid("embedded_id_invalid",
			"id %q 不合法：小写字母/数字/._- 开头，2-64 字符。", m.ID)
	}
	if builtinOrReserved != nil && builtinOrReserved(m.ID) {
		return embeddedInvalid("embedded_id_conflict",
			"id %q 与现有插件目录冲突（内置/已下线/已登记）。", m.ID)
	}
	if m.Name == "" {
		return embeddedInvalid("embedded_name_missing", "manifest.name 不能为空。")
	}
	validCategory := false
	for _, c := range []string{"harmful", "privacy", "injection", "bias", "security-exploit",
		"hallucination", "off-topic", "brand", "sensitive", "industry", "dataset"} {
		if m.Category == c {
			validCategory = true
			break
		}
	}
	if !validCategory {
		return embeddedInvalid("embedded_category_invalid",
			"category=%q 不在现有 11 类内（harmful/privacy/injection/bias/security-exploit/hallucination/off-topic/brand/sensitive/industry/dataset）。", m.Category)
	}
	switch m.Severity {
	case "critical", "high", "medium", "low":
	default:
		return embeddedInvalid("embedded_severity_invalid", "severity 必须是 critical/high/medium/low。")
	}
	if m.PromptfooBase != pluginLegalityTableData.PromptfooVersion {
		return embeddedInvalid("embedded_base_mismatch",
			"promptfoo_base=%q 与当前引擎基座 %s 不一致——包必须对着本基座开发。", m.PromptfooBase, pluginLegalityTableData.PromptfooVersion)
	}
	return nil
}

// DecodeEmbeddedPayload: the stored payload is base64(zip) (metadata carries
// payload_encoding so the read side never guesses).
func DecodeEmbeddedPayload(payload string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
	if err != nil {
		return nil, embeddedInvalid("embedded_payload_encoding", "包正文不是合法 base64（应由发布入口写入）。")
	}
	return raw, nil
}

// EmbeddedAutoGateSummary is the SAFE subset stored in auto_gate (no file
// contents, no paths beyond basenames): what the gate checked and what it
// found, so the portals can show 「自动关卡：✓/✗ 原因」 without the payload.
type EmbeddedAutoGateSummary struct {
	Passed      bool   `json:"passed"`
	Reason      string `json:"reason,omitempty"`
	Files       int    `json:"files"`
	SourceBytes int    `json:"source_bytes"`
	TestCommand string `json:"test_command,omitempty"`
	TestSummary string `json:"test_summary,omitempty"` // e.g. "12 passed · 0.8s" — engine-provided, bounded
}

// parseEmbeddedManifest: strict — an unknown key is usually a typo that would
// otherwise silently vanish (the same reasoning as the YAML plugin schema).
func parseEmbeddedManifest(raw []byte) (*EmbeddedManifest, error) {
	var m EmbeddedManifest
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // unknown key = typo that would otherwise silently vanish
	if err := dec.Decode(&m); err != nil {
		return nil, embeddedInvalid("embedded_manifest_invalid", "manifest.yaml 解析失败：%s", err.Error())
	}
	return &m, nil
}

// embeddedPluginIDReserved: an embedded id may not collide with anything the
// platform already offers — builtin catalog, retired/withdrawn ids. Checked at
// the store exit AND re-checked at initial approval (rule 4: the catalog can
// gain an id between upload and approval; first approver wins, the unique
// index on package_id is the concurrency backstop).
func EmbeddedPluginIDReserved(id string) bool {
	if PromptfooCatalogPluginExists(id) {
		return true
	}
	for _, retired := range IllegalPluginIDs() {
		if retired == id {
			return true
		}
	}
	return false
}

// stampEmbeddedGateMetadata writes the auto-gate summary the handler reads
// back to build the state row. Keys avoid the payload/secret blacklist
// substrings (redteam.SanitizeMetadata) — they must survive the write path.
func stampEmbeddedGateMetadata(metadata map[string]string, m *EmbeddedManifest, verdict *EmbeddedPluginProbeResult) {
	metadata["embedded_package_id"] = m.ID
	metadata["embedded_gate"] = "passed"
	if verdict != nil {
		metadata["embedded_test_summary"] = verdict.TestSummary
	}
}

// PeekEmbeddedManifest extracts id/name/summary from the uploaded zip WITHOUT
// validating it — used by the upload handler to record a rejection state for a
// package that failed the gates (the invalid payload itself is never stored).
// Returns nil when even the shape is unreadable: nothing to list, the error
// response alone carries the reason.
func PeekEmbeddedManifest(payload string) *EmbeddedManifest {
	raw, err := DecodeEmbeddedPayload(payload)
	if err != nil {
		return nil
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil
	}
	for _, f := range zr.File {
		if path.Clean(f.Name) != "manifest.yaml" || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil
		}
		content, err := io.ReadAll(io.LimitReader(rc, 64*1024))
		rc.Close()
		if err != nil {
			return nil
		}
		var m EmbeddedManifest
		if yaml.Unmarshal(content, &m) != nil || strings.TrimSpace(m.ID) == "" {
			return nil
		}
		return &m
	}
	return nil
}

// EmbeddedSourceFile is one file returned by the admin review drawer (R-5).
type EmbeddedSourceFile struct {
	Name      string `json:"name"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

const (
	embeddedSourceMaxFileBytes = 200 * 1024
	embeddedSourceMaxTotal     = 1024 * 1024
)

// AdminReadEmbeddedPackage decrypts and extracts the stored zip for the admin
// review drawer — the R-5 narrow exception (reading the source IS the
// governance act). Owner-scoped by passing the package OWNER's user id (the
// state row knows who uploaded it); bounds prevent a bomb reaching the
// browser, and only text-ish extensions are returned (a binary blob has
// nothing to review and everything to hide).
func (s *ResourceStoreService) AdminReadEmbeddedPackage(ctx context.Context, ownerUserID uuid.UUID, resourceID string) ([]EmbeddedSourceFile, error) {
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	record, err := s.store.GetResource(ctx, ownerUserID, resourceID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errors.New("embedded package resource not found")
	}
	key, err := s.keyStore.GetKey(record.PayloadKeyID)
	if err != nil {
		return nil, fmt.Errorf("load embedded package key: %w", err)
	}
	plain, err := appcrypto.Decrypt(record.EncryptedPayload, key)
	if err != nil {
		return nil, fmt.Errorf("decrypt embedded package: %w", err)
	}
	raw, err := DecodeEmbeddedPayload(string(plain))
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, embeddedInvalid("embedded_not_zip", "存储的包不是有效的 zip（%s）。", err.Error())
	}
	textish := map[string]bool{
		".ts": true, ".js": true, ".mjs": true, ".yaml": true, ".yml": true,
		".json": true, ".md": true, ".txt": true, "": true, // manifest.yaml etc.
	}
	out := make([]EmbeddedSourceFile, 0, len(zr.File))
	total := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(f.Name)
		ext := strings.ToLower(path.Ext(name))
		if !textish[ext] {
			continue // binary: nothing to review
		}
		if total >= embeddedSourceMaxTotal {
			break
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		limit := int64(embeddedSourceMaxFileBytes)
		if remaining := int64(embeddedSourceMaxTotal - total); limit > remaining {
			limit = remaining
		}
		content, err := io.ReadAll(io.LimitReader(rc, limit+1))
		rc.Close()
		if err != nil {
			continue
		}
		truncated := int64(len(content)) > limit
		if truncated {
			content = content[:limit]
		}
		if bytes.Contains(content[:min(len(content), 4096)], []byte{0}) {
			continue // binary with a text extension — skip
		}
		total += len(content)
		out = append(out, EmbeddedSourceFile{Name: name, Content: string(content), Truncated: truncated})
	}
	return out, nil
}
