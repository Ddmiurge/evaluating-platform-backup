// Embedded-plugin build core (P28 / design §9 E-4).
//
// Two halves live here, both deliberately pure of I/O beyond the staged tree:
//
//  1. Catalog read side — the ONLY way a baked id becomes selectable. The
//     build writes testdata/embedded_plugins.json after a successful bake;
//     this file embeds it at compile time (same mechanism as the legality
//     table) and the wizard endpoint filters by CURRENT state (approved →
//     everyone; testing → testers only; anything else → invisible, because a
//     selectable id that the submit gate will refuse is exactly the dead
//     button this design refuses to ship).
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
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
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
// anything yet) — so no init panic here; a malformed document still panics,
// because that is a build-tool bug that would silently empty the catalog.
type embeddedPluginsFile struct {
	PromptfooVersion string                 `json:"promptfoo_version"`
	Plugins          []EmbeddedCatalogEntry `json:"plugins"`
}

var embeddedPluginFileData = mustParseEmbeddedPluginCatalog()

var (
	embeddedPluginCatalog        = embeddedPluginFileData.Plugins
	embeddedPluginCatalogVersion = embeddedPluginFileData.PromptfooVersion
)

func mustParseEmbeddedPluginCatalog() embeddedPluginsFile {
	var file embeddedPluginsFile
	if err := json.Unmarshal(embeddedPluginsJSON, &file); err != nil {
		panic(fmt.Sprintf("maclaw: embedded embedded_plugins.json is malformed: %v", err))
	}
	return file
}

// EmbeddedPluginCatalog returns the baked-plugin entries (empty until the
// first successful build writes the testdata file).
func EmbeddedPluginCatalog() []EmbeddedCatalogEntry {
	return embeddedPluginCatalog
}

// EmbeddedCatalogBaseMismatch reports whether the baked catalog was built
// against a different promptfoo base than the current legality table (exact
// string comparison — the same semantics as ValidateEmbeddedManifestForCatalog's
// promptfoo_base check). A stale catalog means its ids were validated against
// the OLD base: the wizard must hide them and the submit gate must refuse
// them at the request boundary, not mid-run on the engine (review finding 5).
func EmbeddedCatalogBaseMismatch() bool {
	return embeddedPluginCatalogVersion != pluginLegalityTableData.PromptfooVersion
}

// EmbeddedCatalogBaseVersions exposes (baked, current) for refusal messages.
func EmbeddedCatalogBaseVersions() (baked, current string) {
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
	if err := os.WriteFile(path.Join(stagingRoot, "index.json"), data, 0o644); err != nil {
		return nil, fmt.Errorf("write build index: %w", err)
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
