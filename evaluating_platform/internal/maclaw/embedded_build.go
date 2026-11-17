// Embedded-plugin build core (P28 / design §9 E-4).
//
// Two halves live here, both deliberately pure of I/O beyond the staged tree:
//
//  1. Catalog read side — the ONLY way a baked id becomes selectable. The
//     build writes testdata/embedded_plugins.json after a successful bake;
//     this file reads it at init — from `EMBEDDED_CATALOG_FILE` when that env
//     points at the mounted staged file (P28 卷挂载: stage → recreate, no image
//     rebuild), else from the compile-time embed (same mechanism as the
//     legality table, byte-for-byte pre-volume behavior) — and the wizard
//     endpoint filters by CURRENT state (approved → everyone; testing →
//     testers only; anything else → invisible, because a selectable id that the
//     submit gate will refuse is exactly the dead button this design refuses
//     to ship).
//  2. Staging — turns approved rows' stored zips into the
//     promptfoo_engine/embedded-plugins/<id>/ tree + index.json that the
//     Dockerfile COPYs. Re-validates every package through the SAME store-exit
//     rules before staging (a rule tightened after approval must not bake an
//     old package), so the image can only contain what the current guard says
//     is acceptable.
package maclaw

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// zipReader opens the decoded zip (raw bytes already validated by
// DecodeEmbeddedPayload callers).
func zipReader(raw []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, embeddedInvalid("embedded_not_zip", "不是有效的 zip 文件（%s）。", err.Error())
	}
	return zr, nil
}

//go:embed testdata/embedded_plugins.json
var embeddedPluginsJSON []byte

// EmbeddedCatalogFileEnv selects a runtime catalog file over the compiled-in
// default (P28 卷挂载). compose mounts ./internal/maclaw/testdata and points
// this at the staged file, so an embedded-plugin batch is stage →
// force-recreate with NO image rebuild: the wizard catalog follows the file at
// boot — symmetric with the engine loader's boot-time index.json read
// (Step-0: promptfoo_engine/tests/embeddedPluginsVolume.step0.test.ts: the
// engine does not re-read within a process, so both sides need the restart and
// neither needs a rebuild).
//
// It is read at package init (before config.Load), hence os.Getenv directly;
// pkg/config still binds the name so TestAllBindEnvKeysAppearInCompose forces
// compose to pass it through. Unset = byte-for-byte pre-volume behavior.
const EmbeddedCatalogFileEnv = "EMBEDDED_CATALOG_FILE"

// EmbeddedCatalogEntry is one baked plugin as the wizard consumes it.
type EmbeddedCatalogEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Fanout   int    `json:"fanout"`
	// Entry is the manifest's entry file (e.g. src/plugin.ts) — carried on
	// index.json so the engine loader knows what to import. It is NOT sent to
	// the browser (the catalog endpoint builds its response field by field).
	Entry string `json:"entry,omitempty"`
}

// embeddedPluginsFile mirrors testdata/embedded_plugins.json. Unlike the
// legality table, an EMPTY plugin list is a legitimate state (nobody has baked
// anything yet). A malformed document does NOT panic at init (record 58 §9):
// the error is recorded and surfaced through EmbeddedCatalogBootError(), which
// cmd/server treats as a startup refusal and the build tool as a warning —
// the repair path must survive the thing it repairs.
type embeddedPluginsFile struct {
	PromptfooVersion string                 `json:"promptfoo_version"`
	Plugins          []EmbeddedCatalogEntry `json:"plugins"`
}

// embeddedCatalogMu guards the snapshot vars below so a hot refresh (E-4d-3,
// SIGHUP) swaps them as one unit: a reader must never observe the new plugin
// list with the old base version (that combination is exactly what
// plugin_base_mismatch exists to catch). Readers are the accessors below;
// tests may assign the vars directly (single-goroutine).
var embeddedCatalogMu sync.RWMutex

var (
	embeddedPluginFileData       embeddedPluginsFile
	embeddedPluginCatalog        []EmbeddedCatalogEntry
	embeddedPluginCatalogVersion string
	// sha256 of the document the snapshot was loaded from — what /health
	// reports so the deploy script can compare "the backend's snapshot" with
	// "the file stage just wrote". Empty when the boot load failed.
	embeddedPluginCatalogHash string
	// Boot load failure, recorded instead of panicking (record 58 §9):
	// cmd/embeddedbuild imports this package, and a corrupt testdata file must
	// not stop the very tool that rewrites it. The refuse-to-boot contract
	// moved to the consumers — cmd/server fatals on EmbeddedCatalogBootError()
	// before serving anything — so a bad file still cannot run unnoticed.
	embeddedCatalogInitErr error
)

func init() {
	embeddedPluginFileData, embeddedCatalogInitErr = embeddedCatalogFromEnv(os.Getenv(EmbeddedCatalogFileEnv), embeddedPluginsJSON)
	embeddedPluginCatalog = embeddedPluginFileData.Plugins
	embeddedPluginCatalogVersion = embeddedPluginFileData.PromptfooVersion
	if data, err := embeddedCatalogSourceBytes(os.Getenv(EmbeddedCatalogFileEnv), embeddedPluginsJSON); err == nil {
		embeddedPluginCatalogHash = catalogSHA256(data)
	}
}

// EmbeddedCatalogBootError is the recorded boot-load failure (nil when the
// catalog parsed). cmd/server must refuse to start when it is set; the build
// tool deliberately does not — it is the repair path.
func EmbeddedCatalogBootError() error {
	return embeddedCatalogInitErr
}

// catalogSHA256 is the receipt hash: full hex so the deploy host can compute
// the same value with sha256sum over the staged file.
func catalogSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// embeddedCatalogSourceBytes resolves the raw catalog document (env path wins
// over the compiled-in embed) — the shared source for boot load and hot
// refresh, so both can never disagree about which bytes they parsed.
func embeddedCatalogSourceBytes(path string, fallback []byte) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return fallback, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s=%q is set but unreadable: %w", EmbeddedCatalogFileEnv, path, err)
	}
	return data, nil
}

// embeddedCatalogFromEnv resolves the wizard catalog source: a set path wins,
// an empty path falls back to the embed byte-for-byte (pre-volume behavior).
//
// Errors, not panics (record 58 §9): a corrupt or unreadable catalog used to
// panic at package init, which killed `cmd/embeddedbuild` too — the tool that
// is supposed to REWRITE the bad file could not start, leaving only manual
// repair. Init records the error; cmd/server turns it into a startup refusal
// (same fail-closed outcome for the service), while the build tool logs it and
// proceeds to stage.
func embeddedCatalogFromEnv(path string, fallback []byte) (embeddedPluginsFile, error) {
	data, err := embeddedCatalogSourceBytes(path, fallback)
	if err != nil {
		return embeddedPluginsFile{}, err
	}
	file, err := parseEmbeddedCatalogDocument(data)
	if err != nil {
		source := "the compiled-in testdata catalog"
		if strings.TrimSpace(path) != "" {
			source = EmbeddedCatalogFileEnv + " (" + path + ")"
		}
		return embeddedPluginsFile{}, fmt.Errorf("embedded-plugin catalog from %s: %w", source, err)
	}
	return file, nil
}

func parseEmbeddedCatalogDocument(data []byte) (embeddedPluginsFile, error) {
	var file embeddedPluginsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return embeddedPluginsFile{}, fmt.Errorf("embedded-plugin catalog is malformed: %w", err)
	}
	return file, nil
}

// EmbeddedPluginCatalog returns the baked-plugin entries (empty until the
// first successful build writes the testdata file).
func EmbeddedPluginCatalog() []EmbeddedCatalogEntry {
	embeddedCatalogMu.RLock()
	defer embeddedCatalogMu.RUnlock()
	return embeddedPluginCatalog
}

// EmbeddedCatalogStatus reports (base version, content hash) of the CURRENT
// snapshot — the receipt /health serves so the deploy script can verify the
// backend actually reloaded the file stage just wrote (design §14.3).
func EmbeddedCatalogStatus() (version, hash string) {
	embeddedCatalogMu.RLock()
	defer embeddedCatalogMu.RUnlock()
	return embeddedPluginCatalogVersion, embeddedPluginCatalogHash
}

// RefreshEmbeddedCatalog re-reads the catalog source (EMBEDDED_CATALOG_FILE
// when set, else the compiled-in embed) and swaps the snapshot atomically.
//
// E-4d-3: called from the SIGHUP handler. A read or parse failure returns an
// error and leaves the PREVIOUS snapshot serving — a bad file must never blank
// the wizard catalog (design §14.5-4: 旧目录继续服务 + 回执失败). The caller
// decides how to log/expose the receipt; this function changes nothing on
// error.
func RefreshEmbeddedCatalog() error {
	data, err := embeddedCatalogBytesForRefresh()
	if err != nil {
		return err
	}
	file, err := parseEmbeddedCatalogDocument(data)
	if err != nil {
		return err
	}
	embeddedCatalogMu.Lock()
	embeddedPluginCatalog = file.Plugins
	embeddedPluginCatalogVersion = file.PromptfooVersion
	embeddedPluginCatalogHash = catalogSHA256(data)
	embeddedCatalogMu.Unlock()
	return nil
}

func embeddedCatalogBytesForRefresh() ([]byte, error) {
	path := strings.TrimSpace(os.Getenv(EmbeddedCatalogFileEnv))
	if path == "" {
		return embeddedPluginsJSON, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read embedded catalog file: %w", err)
	}
	return data, nil
}

// EmbeddedCatalogBaseMismatch reports whether the baked catalog was built
// against a different promptfoo base than the current legality table (exact
// string comparison — the same semantics as ValidateEmbeddedManifestForCatalog's
// promptfoo_base check). A stale catalog means its ids were validated against
// the OLD base: the wizard must hide them and the submit gate must refuse
// them at the request boundary, not mid-run on the engine (review finding 5).
func EmbeddedCatalogBaseMismatch() bool {
	embeddedCatalogMu.RLock()
	defer embeddedCatalogMu.RUnlock()
	return embeddedPluginCatalogVersion != pluginLegalityTableData.PromptfooVersion
}

// EmbeddedCatalogBaseVersions exposes (baked, current) for refusal messages.
func EmbeddedCatalogBaseVersions() (baked, current string) {
	embeddedCatalogMu.RLock()
	defer embeddedCatalogMu.RUnlock()
	return embeddedPluginCatalogVersion, pluginLegalityTableData.PromptfooVersion
}

// ── staging ─────────────────────────────────────────────────────────────────

// EmbeddedBuildInput is one APPROVED package handed to the staging step: the
// stored payload is re-validated, extracted, and written under stagingRoot.
type EmbeddedBuildInput struct {
	PackageID  string
	PayloadB64 string
}

// EmbeddedBuildIndex is the index.json the harness/loader reads at engine
// boot — the single source of "what this image contains". It is also the
// batch record the finalize step consumes (review findings 1–3: one source
// for "who is in this batch").
type EmbeddedBuildIndex struct {
	BatchID  string                 `json:"batch_id"`
	BuiltAt  time.Time              `json:"built_at"`
	Packages []EmbeddedCatalogEntry `json:"packages"`
	// RemovedIDs are the remove_requested ids this bake excluded from the
	// tree; -finalize marks them removed after a successful deploy. Build-host
	// bookkeeping — the future engine loader must ignore it (JSON readers do).
	RemovedIDs []string `json:"removed_ids,omitempty"`
}

// StageEmbeddedPackages extracts every approved package into
// stagingRoot/<package_id>/ and writes stagingRoot/index.json. removedIDs are
// recorded on the index (not staged) so finalize can close exactly the
// removals this batch actually excluded.
//
// Re-validation is not paranoia: the payload was validated at upload, but the
// rules (forbidden tokens, manifest contract) may have tightened since — the
// image must only ever contain what the CURRENT guard accepts.
func StageEmbeddedPackages(inputs []EmbeddedBuildInput, stagingRoot, batchID string, removedIDs []string) (*EmbeddedBuildIndex, error) {
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create staging root: %w", err)
	}
	index := &EmbeddedBuildIndex{BatchID: batchID, BuiltAt: time.Now().UTC(), RemovedIDs: removedIDs}
	seen := map[string]bool{}
	for _, in := range inputs {
		if seen[in.PackageID] {
			return nil, embeddedInvalid("embedded_build_duplicate", "批次内包 id 重复：%s", in.PackageID)
		}
		seen[in.PackageID] = true
		raw, err := DecodeEmbeddedPayload(in.PayloadB64)
		if err != nil {
			return nil, fmt.Errorf("package %s: %w", in.PackageID, err)
		}
		manifest, _, err := ValidateEmbeddedPluginPackage(raw)
		if err != nil {
			return nil, fmt.Errorf("package %s: %w", in.PackageID, err)
		}
		if err := ValidateEmbeddedManifestForCatalog(manifest, EmbeddedPluginIDReserved); err != nil {
			return nil, fmt.Errorf("package %s: %w", in.PackageID, err)
		}
		if manifest.ID != in.PackageID {
			return nil, embeddedInvalid("embedded_build_id_mismatch",
				"状态表 id %s 与包内 manifest.id %s 不一致——拒绝入镜像。", in.PackageID, manifest.ID)
		}
		dest := path.Join(stagingRoot, manifest.ID)
		if err := extractZipTo(raw, dest); err != nil {
			return nil, fmt.Errorf("package %s: %w", in.PackageID, err)
		}
		index.Packages = append(index.Packages, EmbeddedCatalogEntry{
			ID:       manifest.ID,
			Name:     manifest.Name,
			Category: manifest.Category,
			Severity: manifest.Severity,
			Fanout:   1,
			Entry:    manifest.Entry,
		})
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal build index: %w", err)
	}
	// Atomic (tmp + rename): index.json is the persistent catalog the engine
	// boot-/reload-reads through the volume mount (E-4c) — a concurrent reader
	// must see either the previous complete file or this one, never a
	// half-written or missing document mid-stage.
	indexPath := path.Join(stagingRoot, "index.json")
	tmpPath := indexPath + ".tmp"
	if err := os.WriteFile(tmpPath, append(data, '\n'), 0o644); err != nil {
		return nil, fmt.Errorf("write build index: %w", err)
	}
	if err := os.Rename(tmpPath, indexPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("publish build index: %w", err)
	}
	return index, nil
}

// extractZipTo writes the zip's files under dest with the same path rules the
// upload guard enforced (no absolute, no .., bounded sizes).
func extractZipTo(raw []byte, dest string) error {
	// Reuse the validation pass's guarantees; here we only materialise.
	zr, err := zipReader(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("create package dir: %w", err)
	}
	for _, f := range zr.File {
		name := path.Clean(f.Name)
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return embeddedInvalid("embedded_zip_slip", "包内路径非法（%s）。", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		target := path.Join(dest, name)
		if !strings.HasPrefix(target, dest) {
			return embeddedInvalid("embedded_zip_slip", "包内路径非法（%s）。", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %s: %w", name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, EmbeddedPluginMaxUncompressed+1))
		rc.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.MkdirAll(path.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", name, err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}
