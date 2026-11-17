// Batch lifecycle for the embedded-plugin build pipeline (P28 / design §9
// E-4; review findings 1–4 in audits/code-review-2026-11-10-p28-embeddedbuild.md;
// records/57 §6).
//
// Two files, two roles (E-4c split — they used to be one, and finalize
// deleting the shared file bricked the engine's boot catalog):
//
//	index.json  PERSISTENT engine catalog — written by stage (union of live
//	            states: build_requested ∪ testing ∪ approved ∪ disabled, minus
//	            removals; finding 1), read by the engine at boot/volume-reload,
//	            NEVER deleted by this tool.
//	batch.json  pending marker — written by stage, consumed+deleted by a
//	            completed finalize, cleared by NO_QUEUE. Exists ⟺ a batch
//	            awaits finalize.
//
// Finalize consumes ONLY batch.json: a package that enters build_requested
// after stage waits for the next batch (finding 2), removals ride the same op
// list so a removal-only run is a legitimate batch instead of being swallowed
// by a NO_QUEUE early return (finding 3), and a corrupt index.json cannot
// block recording build_failed — the marker carries the batch identity
// (records/57 §4).
//
// Decision logic (planStage / planFinalize / applyFinalizeOps /
// buildCatalogFile) is pure so it is testable without a database; runStage /
// runFinalize are thin I/O shells around it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appcrypto "evaluating_platform/internal/crypto"
	maclawpkg "evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/repository"
)

// stageableStates is the union that must survive every bake (finding 1).
//
//   - build_requested: this batch's new work;
//   - testing / approved: baked and live — dropping them from a later bake
//     would silently remove them from the wizard catalog;
//   - disabled: D-5 keeps disabled code in the image (disable is a runtime
//     gate). Excluding it would let an unrelated build delete the code, and
//     Enable (disabled → approved) has NO edge back into build_requested —
//     an approved-but-unbaked package has no button to re-bake it.
//
// Excluded, each safe: pending/rejected/approved_for_build are hidden by the
// catalog endpoint anyway and reach build via re-upload + RequestBuild;
// build_failed was provably never live; remove_requested/removed are the
// removal track.
var stageableStates = map[string]bool{
	maclawpkg.EmbeddedStateBuildRequested: true,
	maclawpkg.EmbeddedStateTesting:        true,
	maclawpkg.EmbeddedStateApproved:       true,
	maclawpkg.EmbeddedStateDisabled:       true,
}

// stagePlan is what one -stage run will do.
type stagePlan struct {
	ToStage    []maclawpkg.EmbeddedPluginStateRow // union rows to decrypt + stage
	RemovedIDs []string                           // remove_requested ids excluded from the bake
}

// noQueue means there is genuinely nothing to do: no packages AND no
// removals. Removals alone are a real batch (finding 3).
func (p stagePlan) noQueue() bool {
	return len(p.ToStage) == 0 && len(p.RemovedIDs) == 0
}

// planStage is the single union predicate (pure; review finding 1).
func planStage(rows []maclawpkg.EmbeddedPluginStateRow) stagePlan {
	var plan stagePlan
	for _, row := range rows {
		switch {
		case row.State == maclawpkg.EmbeddedStateRemoveRequested:
			plan.RemovedIDs = append(plan.RemovedIDs, row.PackageID)
		case stageableStates[row.State]:
			plan.ToStage = append(plan.ToStage, row)
		}
	}
	return plan
}

// finalizeOp is one transition (or an explicit skip) for this batch.
type finalizeOp struct {
	PackageID string
	To        string // EmbeddedStateTesting | EmbeddedStateBuildFailed | EmbeddedStateRemoved
	Reason    string // build_failed summary only
	Skip      string // non-empty → do not transition; value says why (current state)
}

// planFinalize consumes ONLY the batch index — never the live queue — so a
// package that entered build_requested after stage stays build_requested for
// the next batch (finding 2), and removal ops come back in the SAME list as
// package ops, which makes the "removals behind an early return" bug (finding
// 3) unrepresentable.
//
// states is packageID → current state (read once by the caller).
func planFinalize(idx *maclawpkg.EmbeddedBuildIndex, states map[string]string, ok bool, reason string) []finalizeOp {
	failReason := strings.TrimSpace(reason)
	if failReason == "" {
		failReason = "构建失败（未提供原因）"
	}
	ops := make([]finalizeOp, 0, len(idx.Packages)+len(idx.RemovedIDs))
	for _, pkg := range idx.Packages {
		state, exists := states[pkg.ID]
		if !exists {
			ops = append(ops, finalizeOp{PackageID: pkg.ID, Skip: "row missing"})
			continue
		}
		if state != maclawpkg.EmbeddedStateBuildRequested {
			// Already-baked rows of THIS batch (testing/approved/disabled) and
			// mid-flight moves (re-upload → pending…) are expected skips, not
			// conflicts — they were never transition candidates.
			ops = append(ops, finalizeOp{PackageID: pkg.ID, Skip: state})
			continue
		}
		op := finalizeOp{PackageID: pkg.ID}
		if ok {
			op.To = maclawpkg.EmbeddedStateTesting
		} else {
			op.To = maclawpkg.EmbeddedStateBuildFailed
			op.Reason = failReason
		}
		ops = append(ops, op)
	}
	for _, id := range idx.RemovedIDs {
		state, exists := states[id]
		if !exists {
			ops = append(ops, finalizeOp{PackageID: id, Skip: "row missing"})
			continue
		}
		if !ok {
			// Deploy failed → the image still contains the package; the
			// removal stays open until a later successful batch.
			ops = append(ops, finalizeOp{PackageID: id, Skip: "deploy failed; removal closes on the next successful batch"})
			continue
		}
		if state != maclawpkg.EmbeddedStateRemoveRequested {
			ops = append(ops, finalizeOp{PackageID: id, Skip: state})
			continue
		}
		ops = append(ops, finalizeOp{PackageID: id, To: maclawpkg.EmbeddedStateRemoved})
	}
	return ops
}

// applyResult tallies what one finalize run did.
type applyResult struct {
	Applied         int
	SkippedConflict int
	SkippedExpected int
	Warnings        []string
}

// applyFinalizeOps executes the plan. A state conflict (the row moved between
// read and transition — another admin, or an expert re-upload) skips JUST that
// row: the deploy already happened, so aborting would strand every other
// correctly-baked row in build_requested while live. Any other error hard-fails
// immediately. This makes finalize retry-safe: a re-run re-reads the index and
// conflict/expected-skips the rows that already moved.
func applyFinalizeOps(ops []finalizeOp, exec func(finalizeOp) error) (applyResult, error) {
	var res applyResult
	for _, op := range ops {
		if op.Skip != "" {
			res.SkippedExpected++
			continue
		}
		err := exec(op)
		if err == nil {
			res.Applied++
			continue
		}
		if errors.Is(err, repository.ErrEmbeddedStateConflict) {
			res.SkippedConflict++
			if op.To == maclawpkg.EmbeddedStateRemoved {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"包 %s 的移除标记在定格前被并发变更，已跳过；若其代码已移出镜像，将在下次 stage 后随当前状态自愈", op.PackageID))
			} else {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"包 %s 在定格时被并发变更，已跳过（不改动其当前状态）", op.PackageID))
			}
			continue
		}
		return res, err
	}
	return res, nil
}

// buildCatalogFile marshals the wizard's catalog source from THIS batch's
// index, so the image, the backend embed and the picker agree by construction.
func buildCatalogFile(idx *maclawpkg.EmbeddedBuildIndex) ([]byte, error) {
	catalog := map[string]any{
		"promptfoo_version": maclawpkg.PromptfooPluginLegalityStatsForCatalog().PromptfooVersion,
		"plugins":           idx.Packages,
	}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal catalog: %w", err)
	}
	return append(data, '\n'), nil
}

// batchRows / batchTransitions are the narrow seams runFinalize needs; the
// concrete repository and lifecycle already satisfy them (fake-store tests).
type batchRows interface {
	ListAll(ctx context.Context, state string, limit int) ([]maclawpkg.EmbeddedPluginStateRow, error)
}

type batchTransitions interface {
	MarkTesting(ctx context.Context, packageID string) error
	MarkBuildFailed(ctx context.Context, packageID, reason string) error
	MarkRemoved(ctx context.Context, packageID string) error
}

// stagingIndexPath is the PERSISTENT engine catalog (E-4c): stage rewrites it
// atomically; nothing deletes it. The engine boot-reads it through the volume
// mount, so deleting it (the pre-E-4c finalize behavior) emptied the live
// catalog on the next restart (records/57 §6).
func stagingIndexPath(root string) string {
	return filepath.Join(root, "promptfoo_engine", "embedded-plugins", "index.json")
}

// stagingBatchPath is the pending marker: exists ⟺ a batch awaits finalize.
// Separate from index.json since E-4c (records/57 §6 / §4).
func stagingBatchPath(root string) string {
	return filepath.Join(root, "promptfoo_engine", "embedded-plugins", "batch.json")
}

// writeJSONAtomic writes via a temp file + rename so a concurrent engine
// boot/reload never observes a half-written marker (stage's catalog write is
// atomic the same way, in maclaw.StageEmbeddedPackages).
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// validatePlatformRoot rejects a -root that is not the platform directory
// BEFORE any DB work or staging write.
//
// Record 54 §6.3: scripts/embedded-build.sh used to pass the repository root.
// The run staged <repo>/promptfoo_engine/embedded-plugins/ (a stray tree) and
// only then failed writing <repo>/internal/maclaw/testdata/embedded_plugins.json
// — leaving debris and a confusing ENOENT. Both halves of the pipeline read
// from the platform directory, so the shape is checkable in one place.
func validatePlatformRoot(root string) error {
	for _, rel := range []string{
		filepath.Join("internal", "maclaw", "testdata"),
		"promptfoo_engine",
	} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("-root %q is not the platform directory: %s/ is missing (pass evaluating_platform/, not the repository root)", root, rel)
		}
	}
	return nil
}

// runStage: read the full state table → plan the union → decrypt every staged
// row (fail-closed: an unreadable resource/key aborts the whole batch per D-6;
// the escape hatch for a broken baked package is admin MarkRemove) → rewrite
// the staging tree and the catalog file. Rows keep their states: nothing is
// claimed as baked until -finalize says so, so a crash here is retryable.
func runStage(ctx context.Context, embeddedRepo batchRows, resourceRepo *repository.MaclawResourceRecordRepository, ks *appcrypto.KeyStore, root string) error {
	rows, err := embeddedRepo.ListAll(ctx, "", 500)
	if err != nil {
		return err
	}
	plan := planStage(rows)
	staging := filepath.Join(root, "promptfoo_engine", "embedded-plugins")
	if plan.noQueue() {
		// Invariant: batch.json exists ⟺ a batch awaits finalize. A stale
		// marker from a previous (possibly dead) batch must not be consumable.
		// index.json is NOT touched — it is the persistent engine catalog
		// (E-4c; clearing it here was the records/57 §6 failure family).
		if err := os.Remove(stagingBatchPath(root)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("clear stale batch marker: %w", err)
		}
		fmt.Println("NO_QUEUE")
		return nil
	}

	inputs := make([]maclawpkg.EmbeddedBuildInput, 0, len(plan.ToStage))
	for _, row := range plan.ToStage {
		rec, err := resourceRepo.GetResource(ctx, row.OwnerUserID, row.ResourceID)
		if err != nil || rec == nil {
			return fmt.Errorf("package %s: resource row unreadable (%v)", row.PackageID, err)
		}
		key, err := ks.GetKey(rec.PayloadKeyID)
		if err != nil {
			return fmt.Errorf("package %s: key %s: %w", row.PackageID, rec.PayloadKeyID, err)
		}
		plain, err := appcrypto.Decrypt(rec.EncryptedPayload, key)
		if err != nil {
			return fmt.Errorf("package %s: decrypt: %w", row.PackageID, err)
		}
		inputs = append(inputs, maclawpkg.EmbeddedBuildInput{PackageID: row.PackageID, PayloadB64: string(plain)})
	}

	// Clean first: leftovers from a previous batch would otherwise be COPY'd
	// into the image alongside the new index (and the old index.json would
	// claim packages this batch never staged).
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("clean staging: %w", err)
	}
	batchID := "batch-" + time.Now().UTC().Format("20060102T150405Z")
	index, err := maclawpkg.StageEmbeddedPackages(inputs, staging, batchID, plan.RemovedIDs)
	if err != nil {
		return err
	}

	catalog, err := buildCatalogFile(index)
	if err != nil {
		return err
	}
	catalogPath := filepath.Join(root, "internal", "maclaw", "testdata", "embedded_plugins.json")
	if err := os.WriteFile(catalogPath, catalog, 0o644); err != nil {
		return fmt.Errorf("write catalog: %w", err)
	}

	// The pending marker (E-4c): same batch identity, separate file. Written
	// AFTER the catalog so a crash in between leaves no marker → finalize
	// refuses → the batch stays build_requested and a re-stage is the repair.
	if err := writeJSONAtomic(stagingBatchPath(root), index); err != nil {
		return fmt.Errorf("write batch marker: %w", err)
	}

	fmt.Printf("STAGED %s packages=%d removed=%d\n", batchID, len(index.Packages), len(index.RemovedIDs))
	for _, pkg := range index.Packages {
		fmt.Printf("  - %s (%s)\n", pkg.ID, pkg.Name)
	}
	for _, id := range index.RemovedIDs {
		fmt.Printf("  - %s (remove_requested, excluded)\n", id)
	}
	return nil
}

// runFinalize: the state-machine half. Only reached after the deploy step, so
// `testing` means "baked AND live" — never "baked but not deployed". It
// consumes this batch's batch.json (never index.json, never the live queue)
// and always processes removals, including for a removal-only batch.
func runFinalize(ctx context.Context, rows batchRows, transitions batchTransitions, root string, intent finalizeIntent) error {
	raw, err := os.ReadFile(stagingBatchPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no batch marker at %s — nothing to finalize (skip finalize after NO_QUEUE; batch.json exists only while a batch awaits finalize)", stagingBatchPath(root))
		}
		return fmt.Errorf("read batch marker: %w", err)
	}
	var idx maclawpkg.EmbeddedBuildIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		return fmt.Errorf("parse batch marker: %w", err)
	}

	all, err := rows.ListAll(ctx, "", 500)
	if err != nil {
		return err
	}
	states := make(map[string]string, len(all))
	for _, row := range all {
		states[row.PackageID] = row.State
	}

	ops := planFinalize(&idx, states, intent.Ok, intent.Reason)
	res, err := applyFinalizeOps(ops, func(op finalizeOp) error {
		switch op.To {
		case maclawpkg.EmbeddedStateTesting:
			return transitions.MarkTesting(ctx, op.PackageID)
		case maclawpkg.EmbeddedStateBuildFailed:
			return transitions.MarkBuildFailed(ctx, op.PackageID, op.Reason)
		case maclawpkg.EmbeddedStateRemoved:
			return transitions.MarkRemoved(ctx, op.PackageID)
		default:
			return fmt.Errorf("unknown finalize target %q for package %s", op.To, op.PackageID)
		}
	})
	for _, w := range res.Warnings {
		fmt.Println("WARNING:", w)
	}
	if err != nil {
		return err
	}

	// Completion clears the batch MARKER (both outcomes — the batch is done,
	// rows are testing or build_failed). An apply error above returned already
	// and keeps it so the orchestrator can retry. index.json is untouched: it
	// is the persistent engine catalog (E-4c; records/57 §6).
	if err := os.Remove(stagingBatchPath(root)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear batch marker: %w", err)
	}
	fmt.Printf("FINALIZED ok=%v applied=%d expected_skips=%d conflicts=%d\n",
		intent.Ok, res.Applied, res.SkippedExpected, res.SkippedConflict)
	return nil
}
