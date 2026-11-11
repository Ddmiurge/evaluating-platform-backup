package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	maclawpkg "evaluating_platform/internal/maclaw"
	"evaluating_platform/internal/repository"
)

// row is a shorthand for a state-table row in plan tests.
func row(id, state string) maclawpkg.EmbeddedPluginStateRow {
	return maclawpkg.EmbeddedPluginStateRow{PackageID: id, State: state}
}

func ids(rows []maclawpkg.EmbeddedPluginStateRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.PackageID)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Review finding 1: the union is build_requested ∪ testing ∪ approved ∪
// disabled; remove_requested rides as RemovedIDs; every other state is out.
// This test is the executable spec for the disabled-in-union decision (D-5:
// disable is a runtime gate, the image keeps the code).
func TestPlanStageUnionIncludesBakedStates(t *testing.T) {
	all := []maclawpkg.EmbeddedPluginStateRow{
		row("a-pending", maclawpkg.EmbeddedStatePendingInitialReview),
		row("a-rejected", maclawpkg.EmbeddedStateRejected),
		row("a-forbuild", maclawpkg.EmbeddedStateApprovedForBuild),
		row("a-requested", maclawpkg.EmbeddedStateBuildRequested),
		row("a-failed", maclawpkg.EmbeddedStateBuildFailed),
		row("a-testing", maclawpkg.EmbeddedStateTesting),
		row("a-approved", maclawpkg.EmbeddedStateApproved),
		row("a-disabled", maclawpkg.EmbeddedStateDisabled),
		row("a-removing", maclawpkg.EmbeddedStateRemoveRequested),
		row("a-removed", maclawpkg.EmbeddedStateRemoved),
	}
	plan := planStage(all)

	wantStage := []string{"a-requested", "a-testing", "a-approved", "a-disabled"}
	if got := ids(plan.ToStage); !sameStrings(got, wantStage) {
		t.Fatalf("ToStage = %v, want %v", got, wantStage)
	}
	if want := []string{"a-removing"}; !sameStrings(plan.RemovedIDs, want) {
		t.Fatalf("RemovedIDs = %v, want %v", plan.RemovedIDs, want)
	}
}

// Review finding 3 (stage half): removals alone are a real batch — only a
// genuinely empty plan is NO_QUEUE.
func TestPlanStageNoQueueOnlyWhenNothingToDo(t *testing.T) {
	cases := []struct {
		name    string
		rows    []maclawpkg.EmbeddedPluginStateRow
		noQueue bool
	}{
		{"empty", nil, true},
		{"only removals", []maclawpkg.EmbeddedPluginStateRow{row("r", maclawpkg.EmbeddedStateRemoveRequested)}, false},
		{"only disabled", []maclawpkg.EmbeddedPluginStateRow{row("d", maclawpkg.EmbeddedStateDisabled)}, false},
		{"only pending", []maclawpkg.EmbeddedPluginStateRow{row("p", maclawpkg.EmbeddedStatePendingInitialReview)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := planStage(tc.rows).noQueue(); got != tc.noQueue {
				t.Fatalf("noQueue() = %v, want %v", got, tc.noQueue)
			}
		})
	}
}

// Review finding 2: finalize consumes ONLY the batch index. D entered the
// queue after stage and R is a removal not recorded in this index — neither
// may be touched, even though both are "pending work" by a live-queue reading.
func TestPlanFinalizeConsumesBatchIndexNotQueue(t *testing.T) {
	idx := &maclawpkg.EmbeddedBuildIndex{
		BatchID:  "batch-1",
		Packages: []maclawpkg.EmbeddedCatalogEntry{{ID: "A"}, {ID: "B"}},
	}
	states := map[string]string{
		"A": maclawpkg.EmbeddedStateBuildRequested, // still waiting for this batch
		"B": maclawpkg.EmbeddedStateTesting,        // already moved (expected skip)
		"D": maclawpkg.EmbeddedStateBuildRequested, // queued AFTER stage — next batch
		"R": maclawpkg.EmbeddedStateRemoveRequested,
	}
	ops := planFinalize(idx, states, true, "")

	var transitioned []string
	for _, op := range ops {
		if op.Skip == "" {
			transitioned = append(transitioned, op.PackageID)
		}
	}
	if want := []string{"A"}; !sameStrings(transitioned, want) {
		t.Fatalf("transitioned = %v, want %v (D and R must get zero ops)", transitioned, want)
	}
	// R must not appear in any form: it is not in this index at all.
	for _, op := range ops {
		if op.PackageID == "R" || op.PackageID == "D" {
			t.Fatalf("op for non-batch row: %+v", op)
		}
	}
}

// Review finding 3 (finalize half): a removal-only batch (zero packages)
// still produces the removal transition — the old NO_QUEUE early return sat
// in front of the removal loop and left rows stuck in remove_requested.
func TestPlanFinalizeRemovalsWithoutPackages(t *testing.T) {
	idx := &maclawpkg.EmbeddedBuildIndex{BatchID: "batch-r", RemovedIDs: []string{"R"}}
	states := map[string]string{"R": maclawpkg.EmbeddedStateRemoveRequested}
	ops := planFinalize(idx, states, true, "")
	if len(ops) != 1 {
		t.Fatalf("ops = %+v, want exactly one removal op", ops)
	}
	if ops[0].PackageID != "R" || ops[0].To != maclawpkg.EmbeddedStateRemoved || ops[0].Skip != "" {
		t.Fatalf("op = %+v", ops[0])
	}
}

// A failed deploy must not close removals (the image still contains them)
// and must send this batch's still-queued rows to build_failed with the
// supplied reason.
func TestPlanFinalizeFailurePath(t *testing.T) {
	idx := &maclawpkg.EmbeddedBuildIndex{
		Packages:   []maclawpkg.EmbeddedCatalogEntry{{ID: "A"}},
		RemovedIDs: []string{"R"},
	}
	states := map[string]string{
		"A": maclawpkg.EmbeddedStateBuildRequested,
		"R": maclawpkg.EmbeddedStateRemoveRequested,
	}
	ops := planFinalize(idx, states, false, "guard red")
	if len(ops) != 2 {
		t.Fatalf("ops = %+v", ops)
	}
	if ops[0].To != maclawpkg.EmbeddedStateBuildFailed || ops[0].Reason != "guard red" {
		t.Fatalf("package op = %+v", ops[0])
	}
	if ops[1].Skip == "" {
		t.Fatalf("removal must stay open on a failed deploy, got %+v", ops[1])
	}
}

// Two-tier skips: a row that moved between read and plan is an expected skip
// (named by its current state), not a conflict — only rows that were still
// candidates at read time can produce a real conflict at exec time.
func TestPlanFinalizeMidFlightMoves(t *testing.T) {
	idx := &maclawpkg.EmbeddedBuildIndex{Packages: []maclawpkg.EmbeddedCatalogEntry{
		{ID: "reuploaded"}, {ID: "already-testing"}, {ID: "waiting"}, {ID: "gone"},
	}}
	states := map[string]string{
		"reuploaded":      maclawpkg.EmbeddedStatePendingInitialReview,
		"already-testing": maclawpkg.EmbeddedStateTesting,
		"waiting":         maclawpkg.EmbeddedStateBuildRequested,
		// "gone" absent: row missing
	}
	ops := planFinalize(idx, states, true, "")
	if len(ops) != 4 {
		t.Fatalf("ops = %+v", ops)
	}
	byID := map[string]finalizeOp{}
	for _, op := range ops {
		byID[op.PackageID] = op
	}
	if byID["reuploaded"].Skip != maclawpkg.EmbeddedStatePendingInitialReview {
		t.Fatalf("reuploaded = %+v", byID["reuploaded"])
	}
	if byID["already-testing"].Skip != maclawpkg.EmbeddedStateTesting {
		t.Fatalf("already-testing = %+v", byID["already-testing"])
	}
	if byID["waiting"].Skip != "" || byID["waiting"].To != maclawpkg.EmbeddedStateTesting {
		t.Fatalf("waiting = %+v", byID["waiting"])
	}
	if byID["gone"].Skip != "row missing" {
		t.Fatalf("gone = %+v", byID["gone"])
	}
}

// Exec-time semantics: a state conflict skips just that row (the deploy
// already happened — aborting would strand the rest); any other error
// hard-fails immediately.
func TestApplyFinalizeOpsConflictSkip(t *testing.T) {
	conflict := repository.ErrEmbeddedStateConflict
	ops := []finalizeOp{
		{PackageID: "A", To: maclawpkg.EmbeddedStateTesting},
		{PackageID: "B", To: maclawpkg.EmbeddedStateTesting},
		{PackageID: "C", To: maclawpkg.EmbeddedStateTesting},
	}

	// Conflict in the middle → continue, count, warn.
	var calls []string
	res, err := applyFinalizeOps(ops, func(op finalizeOp) error {
		calls = append(calls, op.PackageID)
		if op.PackageID == "B" {
			return conflict
		}
		return nil
	})
	if err != nil {
		t.Fatalf("conflict must not fail the run: %v", err)
	}
	if res.Applied != 2 || res.SkippedConflict != 1 || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !sameStrings(calls, []string{"A", "B", "C"}) {
		t.Fatalf("calls = %v", calls)
	}

	// Plain error → stop immediately (Applied counts only what landed).
	calls = nil
	res, err = applyFinalizeOps(ops, func(op finalizeOp) error {
		calls = append(calls, op.PackageID)
		if op.PackageID == "B" {
			return errors.New("connection reset")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected hard failure")
	}
	if res.Applied != 1 || !sameStrings(calls, []string{"A", "B"}) {
		t.Fatalf("result = %+v calls = %v", res, calls)
	}
}

// Review finding 4: the documented invocations resolve; every half-specified
// or contradictory combination is rejected (bare -finalize used to silently
// mean "fail the whole batch").
func TestParseFinalizeIntent(t *testing.T) {
	cases := []struct {
		name    string
		ok      bool
		fail    bool
		reason  string
		wantErr bool
		wantOk  bool
	}{
		{"ok", true, false, "", false, true},
		{"fail with reason", false, true, "guard red", false, false},
		{"bare finalize", false, false, "", true, false},
		{"mutually exclusive", true, true, "x", true, false},
		{"fail without reason", false, true, "  ", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent, err := parseFinalizeIntent(tc.ok, tc.fail, tc.reason)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if intent.Ok != tc.wantOk {
				t.Fatalf("Ok = %v, want %v", intent.Ok, tc.wantOk)
			}
		})
	}
}

// Review finding 4: the file-header command `-finalize -fail -reason=…` must
// parse — the flag set used to not define -fail at all (exit 2 on the
// documented failure path).
func TestRegisterFlagsDefinesFail(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	flags := registerFlags(fs)
	if fs.Lookup("fail") == nil || flags.fail == nil {
		t.Fatal("-fail flag not defined")
	}
	if fs.Lookup("ok") == nil || fs.Lookup("reason") == nil {
		t.Fatal("-ok/-reason flags missing")
	}
	// The documented failure command parses end to end.
	if err := fs.Parse([]string{"-finalize", "-fail", "-reason=guard red"}); err != nil {
		t.Fatalf("documented failure command does not parse: %v", err)
	}
	if _, err := parseFinalizeIntent(*flags.ok, *flags.fail, *flags.reason); err != nil {
		t.Fatalf("documented failure command does not resolve: %v", err)
	}
}

// The catalog file is derived from the SAME batch index — union content and
// base version land in the file the wizard embeds.
func TestBuildCatalogFileUnionContent(t *testing.T) {
	idx := &maclawpkg.EmbeddedBuildIndex{
		BatchID: "batch-9",
		Packages: []maclawpkg.EmbeddedCatalogEntry{
			{ID: "pkg-a", Name: "A"},
			{ID: "pkg-b", Name: "B"},
		},
		RemovedIDs: []string{"gone"},
	}
	raw, err := buildCatalogFile(idx)
	if err != nil {
		t.Fatalf("buildCatalogFile: %v", err)
	}
	var file struct {
		PromptfooVersion string                           `json:"promptfoo_version"`
		Plugins          []maclawpkg.EmbeddedCatalogEntry `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("catalog parse: %v", err)
	}
	if file.PromptfooVersion == "" {
		t.Fatal("promptfoo_version missing from catalog")
	}
	if len(file.Plugins) != 2 || file.Plugins[0].ID != "pkg-a" || file.Plugins[1].ID != "pkg-b" {
		t.Fatalf("plugins = %+v", file.Plugins)
	}
}

// fakeRows / fakeTransitions: in-memory stand-ins for the repository and the
// lifecycle so the finalize shell runs end to end without a database.
type fakeRows struct {
	states map[string]string
	calls  int
}

func (f *fakeRows) ListAll(_ context.Context, _ string, _ int) ([]maclawpkg.EmbeddedPluginStateRow, error) {
	f.calls++
	var out []maclawpkg.EmbeddedPluginStateRow
	for id, state := range f.states {
		out = append(out, row(id, state))
	}
	return out, nil
}

type fakeTransitions struct {
	testing []string
	failed  []string
	removed []string
}

func (f *fakeTransitions) MarkTesting(_ context.Context, id string) error {
	f.testing = append(f.testing, id)
	return nil
}

func (f *fakeTransitions) MarkBuildFailed(_ context.Context, id, _ string) error {
	f.failed = append(f.failed, id)
	return nil
}

func (f *fakeTransitions) MarkRemoved(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

// No index.json → hard error naming the NO_QUEUE contract; no transitions
// are attempted (finding 2: finalize must never fall back to the live queue).
func TestRunFinalizeMissingIndexErrors(t *testing.T) {
	rows := &fakeRows{states: map[string]string{"X": maclawpkg.EmbeddedStateBuildRequested}}
	trans := &fakeTransitions{}
	err := runFinalize(context.Background(), rows, trans, t.TempDir(), finalizeIntent{Ok: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "index.json") && !strings.Contains(err.Error(), "batch index") {
		t.Fatalf("error should name the missing index: %v", err)
	}
	if len(trans.testing)+len(trans.failed)+len(trans.removed) != 0 {
		t.Fatalf("no transitions may run: %+v", trans)
	}
}

// End-to-end (no DB): a removal-only index marks the removal and never calls
// MarkTesting — finding 3's full shell path.
func TestRunFinalizeRemovalOnlyBatch(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "promptfoo_engine", "embedded-plugins")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := maclawpkg.EmbeddedBuildIndex{BatchID: "batch-r", RemovedIDs: []string{"R"}}
	raw, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "index.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	rows := &fakeRows{states: map[string]string{"R": maclawpkg.EmbeddedStateRemoveRequested}}
	trans := &fakeTransitions{}
	if err := runFinalize(context.Background(), rows, trans, root, finalizeIntent{Ok: true}); err != nil {
		t.Fatalf("runFinalize: %v", err)
	}
	if len(trans.removed) != 1 || trans.removed[0] != "R" {
		t.Fatalf("removed = %v", trans.removed)
	}
	if len(trans.testing) != 0 || len(trans.failed) != 0 {
		t.Fatalf("unexpected transitions: %+v", trans)
	}
	// Success clears the batch record (index exists ⟺ awaiting finalize).
	if _, err := os.Stat(filepath.Join(staging, "index.json")); !os.IsNotExist(err) {
		t.Fatalf("index.json should be cleared after a successful finalize: %v", err)
	}
}
