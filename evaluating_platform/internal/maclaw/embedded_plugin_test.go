package maclaw

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// ── zip builder (tests only) ────────────────────────────────────────────────

// errFakeConflict stands in for the repository's unique-index / state-guard
// error (maclaw tests may not import repository — import cycle).
var errFakeConflict = errors.New("embedded plugin state conflict")

type zipEntry struct {
	name    string
	content string
}

func buildZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("zip create %s: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatalf("zip write %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

const validManifest = "id: probe-demo\nname: 探针演示\ncategory: injection\nseverity: high\n" +
	"promptfoo_base: \"" + "0.123.0" + "\"\nentry: src/plugin.ts\ntests: tests/run.mjs\nsummary: 测试用\n"

func validZip(t *testing.T) string {
	return buildZip(t, []zipEntry{
		{"manifest.yaml", validManifest},
		{"src/plugin.ts", "export default {}"},
		{"tests/run.mjs", "process.exit(0)"},
	})
}

// ── validation ──────────────────────────────────────────────────────────────

func TestValidateEmbeddedPluginPackageAcceptsValid(t *testing.T) {
	raw, err := DecodeEmbeddedPayload(validZip(t))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, err := ValidateEmbeddedPluginPackage(raw)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if m.ID != "probe-demo" || m.Category != "injection" || m.PromptfooBase != "0.123.0" {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestValidateEmbeddedPluginPackageRefusals(t *testing.T) {
	cases := []struct {
		name string
		zip  func(t *testing.T) string
		code string
	}{
		{"zip slip", func(t *testing.T) string {
			// Go's zip writer keeps the name as given — unlike adm-zip.
			return buildZip(t, []zipEntry{
				{"manifest.yaml", validManifest},
				{"../evil.js", "x"},
				{"tests/run.mjs", "process.exit(0)"},
			})
		}, "embedded_zip_slip"},
		{"missing manifest", func(t *testing.T) string {
			return buildZip(t, []zipEntry{{"src/plugin.ts", "x"}, {"tests/run.mjs", "process.exit(0)"}})
		}, "embedded_no_manifest"},
		{"forbidden token in source", func(t *testing.T) string {
			return buildZip(t, []zipEntry{
				{"manifest.yaml", validManifest},
				{"src/plugin.ts", "const k = process.env.SECRET;"},
				{"tests/run.mjs", "process.exit(0)"},
			})
		}, "embedded_forbidden_token"},
		{"missing test entry", func(t *testing.T) string {
			return buildZip(t, []zipEntry{{"manifest.yaml", validManifest}, {"src/plugin.ts", "x"}})
		}, "embedded_tests_contract"},
		{"wrong base version", func(t *testing.T) string {
			m := "id: probe-demo\nname: n\ncategory: injection\nseverity: high\n" +
				"promptfoo_base: \"0.99.0\"\nentry: src/plugin.ts\ntests: tests/run.mjs\n"
			return buildZip(t, []zipEntry{{"manifest.yaml", m}, {"src/plugin.ts", "x"}, {"tests/run.mjs", "process.exit(0)"}})
		}, "embedded_base_mismatch"},
		{"bad category", func(t *testing.T) string {
			m := "id: probe-demo\nname: n\ncategory: nope\nseverity: high\n" +
				"promptfoo_base: \"0.123.0\"\nentry: src/plugin.ts\ntests: tests/run.mjs\n"
			return buildZip(t, []zipEntry{{"manifest.yaml", m}, {"src/plugin.ts", "x"}, {"tests/run.mjs", "process.exit(0)"}})
		}, "embedded_category_invalid"},
		{"id collides with builtin", func(t *testing.T) string {
			m := "id: harmful\nname: n\ncategory: injection\nseverity: high\n" +
				"promptfoo_base: \"0.123.0\"\nentry: src/plugin.ts\ntests: tests/run.mjs\n"
			return buildZip(t, []zipEntry{{"manifest.yaml", m}, {"src/plugin.ts", "x"}, {"tests/run.mjs", "process.exit(0)"}})
		}, "embedded_id_conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := DecodeEmbeddedPayload(tc.zip(t))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			// Mirror the store exit's order: package checks, then catalog checks.
			m, err := ValidateEmbeddedPluginPackage(raw)
			if err == nil {
				err = ValidateEmbeddedManifestForCatalog(m, EmbeddedPluginIDReserved)
			}
			if err == nil {
				t.Fatal("expected refusal, got nil")
			}
			verr, ok := err.(*EmbeddedPluginValidationError)
			if !ok {
				t.Fatalf("error type = %T", err)
			}
			if verr.Code != tc.code {
				t.Fatalf("code = %q, want %q (%s)", verr.Code, tc.code, verr.Message)
			}
		})
	}
}

// ── in-memory store for lifecycle tests ─────────────────────────────────────

type fakeEmbeddedStore struct {
	mu     sync.Mutex
	rows   map[string]*EmbeddedPluginStateRow
	events []string
}

func newFakeEmbeddedStore() *fakeEmbeddedStore {
	return &fakeEmbeddedStore{rows: map[string]*EmbeddedPluginStateRow{}}
}

func (s *fakeEmbeddedStore) GetByPackageID(_ context.Context, id string) (*EmbeddedPluginStateRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	if !ok {
		return nil, nil
	}
	copied := *row
	return &copied, nil
}

func (s *fakeEmbeddedStore) Insert(_ context.Context, row EmbeddedPluginStateRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[row.PackageID]; exists {
		return errFakeConflict // mirrors the unique index (PK collision)
	}
	copied := row
	s.rows[row.PackageID] = &copied
	return nil
}

func (s *fakeEmbeddedStore) Transition(_ context.Context, packageID string, expected []string, to string, patch map[string]any, event string, _ *uuid.UUID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[packageID]
	if !ok {
		return errFakeConflict
	}
	match := false
	for _, e := range expected {
		if row.State == e {
			match = true
			break
		}
	}
	if !match {
		return errFakeConflict
	}
	row.State = to
	for k, v := range patch {
		switch k {
		case "resource_id":
			row.ResourceID, _ = v.(string)
		case "version":
			row.Version, _ = v.(string)
		case "auto_gate":
			row.AutoGate, _ = v.([]byte)
		case "name":
			row.Name, _ = v.(string)
		case "summary":
			row.Summary, _ = v.(string)
		case "review_reason":
			row.ReviewReason, _ = v.(string)
		case "reviewer_user_id":
			row.ReviewerUserID, _ = v.(*uuid.UUID)
		case "final_approved_by":
			row.FinalApprovedBy, _ = v.(*uuid.UUID)
		case "build_requested_by":
			row.BuildRequestedBy, _ = v.(*uuid.UUID)
		}
	}
	s.events = append(s.events, event)
	return nil
}

func (s *fakeEmbeddedStore) IsTester(_ context.Context, _ uuid.UUID) (bool, error) { return false, nil }

// ── lifecycle ───────────────────────────────────────────────────────────────

func TestEmbeddedLifecycleUploadRejectionAndRevisionLoop(t *testing.T) {
	ctx := context.Background()
	store := newFakeEmbeddedStore()
	lc := NewEmbeddedPluginLifecycle(store)
	owner := uuid.New()
	gatePass := EmbeddedAutoGateSummary{Passed: true, TestSummary: "12 passed · 40ms"}
	gateFail := EmbeddedAutoGateSummary{Passed: false, Reason: "文件 src/plugin.ts 含 process.env"}

	// First upload fails the gate: a state row exists (rejected + reason) but
	// NO resource row (the payload was never stored).
	if err := lc.RecordRejection(ctx, owner, "probe-demo", "1", "探针演示", gateFail); err != nil {
		t.Fatalf("record rejection: %v", err)
	}
	row, _ := store.GetByPackageID(ctx, "probe-demo")
	if row == nil || row.State != EmbeddedStateRejected || row.ResourceID != "" {
		t.Fatalf("rejected row = %+v", row)
	}
	if row.Name != "探针演示" {
		t.Fatalf("rejected row must keep the display name, got %q", row.Name)
	}

	// Revision: fixed package passes → back to pending, resource attached.
	if err := lc.RecordUpload(ctx, owner, "res-1", &EmbeddedManifest{ID: "probe-demo", Name: "探针演示", Summary: "s"}, gatePass, true); err != nil {
		t.Fatalf("re-upload: %v", err)
	}
	row, _ = store.GetByPackageID(ctx, "probe-demo")
	if row.State != EmbeddedStatePendingInitialReview || row.ResourceID != "res-1" || row.Version != "2" {
		t.Fatalf("after re-upload = %+v", row)
	}

	// Approve → build → testing: each step only from its own state.
	if err := lc.InitialApprove(ctx, "probe-demo", uuid.New(), "初审通过"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := lc.InitialApprove(ctx, "probe-demo", uuid.New(), "再批一次"); err == nil {
		t.Fatal("double approve must be refused (state guard)")
	}
	if err := lc.RequestBuild(ctx, "probe-demo", uuid.New()); err != nil {
		t.Fatalf("request build: %v", err)
	}
	if err := lc.MarkTesting(ctx, "probe-demo"); err != nil {
		t.Fatalf("mark testing: %v", err)
	}

	// Rule 5: NO automatic edge from testing to approved — only the manual call.
	row, _ = store.GetByPackageID(ctx, "probe-demo")
	if row.State != EmbeddedStateTesting {
		t.Fatalf("state = %s", row.State)
	}
	if err := lc.FinalApprove(ctx, "probe-demo", uuid.New(), "测试通过"); err != nil {
		t.Fatalf("final approve: %v", err)
	}
	row, _ = store.GetByPackageID(ctx, "probe-demo")
	if row.State != EmbeddedStateApproved {
		t.Fatalf("state = %s", row.State)
	}
}

func TestEmbeddedLifecycleReasonAndTerminalRules(t *testing.T) {
	ctx := context.Background()
	store := newFakeEmbeddedStore()
	lc := NewEmbeddedPluginLifecycle(store)
	owner := uuid.New()
	manifest := &EmbeddedManifest{ID: "p1", Name: "n"}
	if err := lc.RecordUpload(ctx, owner, "res-1", manifest, EmbeddedAutoGateSummary{Passed: true}, false); err != nil {
		t.Fatalf("upload: %v", err)
	}
	// Rule 2: changes without an opinion are impossible.
	if err := lc.RequestChanges(ctx, "p1", uuid.New(), "   "); err == nil {
		t.Fatal("changes without reason must be refused by the state machine itself")
	}
	// removed is terminal: re-upload refused.
	if err := lc.InitialApprove(ctx, "p1", uuid.New(), "ok"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := lc.RequestBuild(ctx, "p1", uuid.New()); err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := lc.MarkTesting(ctx, "p1"); err != nil {
		t.Fatalf("testing: %v", err)
	}
	if err := lc.MarkRemove(ctx, "p1", uuid.New(), "下线"); err == nil {
		// remove is only valid from approved/disabled, not testing — expect refusal
		t.Fatal("remove from testing must be refused")
	}
	if err := lc.FinalApprove(ctx, "p1", uuid.New(), "pass"); err != nil {
		t.Fatalf("final: %v", err)
	}
	if err := lc.MarkRemove(ctx, "p1", uuid.New(), "下线"); err != nil {
		t.Fatalf("mark remove: %v", err)
	}
	if err := lc.MarkRemoved(ctx, "p1"); err != nil {
		t.Fatalf("removed: %v", err)
	}
	if err := lc.RecordUpload(ctx, owner, "res-2", manifest, EmbeddedAutoGateSummary{Passed: true}, true); err == nil {
		t.Fatal("re-upload of a removed package must be refused (terminal)")
	}
	// Bump semantics
	if got := bumpEmbeddedVersion("1"); got != "2" {
		t.Fatalf("bump 1 = %s", got)
	}
	if got := bumpEmbeddedVersion("default"); got != "1" {
		t.Fatalf("bump default = %s", got)
	}
	if got := bumpEmbeddedVersion("41"); got != "42" {
		t.Fatalf("bump 41 = %s", got)
	}
}

// ── selection gate (both doors) ─────────────────────────────────────────────

type gateEmbeddedStore struct {
	*fakeEmbeddedStore
	tester bool
}

func (s *gateEmbeddedStore) IsTester(_ context.Context, _ uuid.UUID) (bool, error) {
	return s.tester, nil
}

func TestEmbeddedSelectionGateByState(t *testing.T) {
	ctx := context.Background()
	owner := uuid.New()
	user := uuid.New()

	newLC := func(state string) *EmbeddedPluginLifecycle {
		store := &gateEmbeddedStore{fakeEmbeddedStore: newFakeEmbeddedStore()}
		if state != "" {
			store.rows["demo"] = &EmbeddedPluginStateRow{PackageID: "demo", State: state, OwnerUserID: owner}
		}
		return NewEmbeddedPluginLifecycle(store)
	}

	// Not an embedded id at all → gate silent (builtin path continues).
	lc := newLC("")
	if err := lc.GuardEmbeddedPluginSelection(ctx, user, []string{"harmful"}); err != nil {
		t.Fatalf("builtin id must pass: %v", err)
	}

	// Pending → refused with the state machine's wording.
	lc = newLC(EmbeddedStatePendingInitialReview)
	err := lc.GuardEmbeddedPluginSelection(ctx, user, []string{"demo"})
	if err == nil {
		t.Fatal("pending package must be refused")
	}
	refusal := asRefusal(err)
	if refusal == nil || refusal.Code != "plugin_not_yet_approved" {
		t.Fatalf("refusal = %#v", err)
	}

	// Testing: tester allowed, everyone else refused.
	lc = newLC(EmbeddedStateTesting)
	if err := lc.GuardEmbeddedPluginSelection(ctx, user, []string{"demo"}); err == nil {
		t.Fatal("non-tester must not select a testing package")
	}
	testerStore := &gateEmbeddedStore{fakeEmbeddedStore: newFakeEmbeddedStore(), tester: true}
	testerStore.rows["demo"] = &EmbeddedPluginStateRow{PackageID: "demo", State: EmbeddedStateTesting, OwnerUserID: owner}
	lc = NewEmbeddedPluginLifecycle(testerStore)
	if err := lc.GuardEmbeddedPluginSelection(ctx, user, []string{"demo"}); err != nil {
		t.Fatalf("tester must select a testing package: %v", err)
	}

	// Approved → allowed for anyone.
	lc = newLC(EmbeddedStateApproved)
	if err := lc.GuardEmbeddedPluginSelection(ctx, user, []string{"demo"}); err != nil {
		t.Fatalf("approved package must pass: %v", err)
	}

	// Disabled → refused with its own code.
	lc = newLC(EmbeddedStateDisabled)
	err = lc.GuardEmbeddedPluginSelection(ctx, user, []string{"demo"})
	refusal = asRefusal(err)
	if refusal == nil || refusal.Code != "plugin_disabled" {
		t.Fatalf("disabled refusal = %#v", err)
	}
}

// Review finding 5: when the baked catalog's promptfoo_version differs from
// the current base, the shared gate refuses embedded ids with
// plugin_base_mismatch — before any state check, because a stale catalog
// makes every state's verdict untrustworthy. The refusal travels the normal
// EmbeddedSelectionRefusalError path (409 via writeEnginePreflightRejection).
func TestEmbeddedSelectionGateRefusesStaleBase(t *testing.T) {
	orig := embeddedPluginCatalogVersion
	defer func() { embeddedPluginCatalogVersion = orig }()
	embeddedPluginCatalogVersion = "0.0.0-stale"

	store := &gateEmbeddedStore{fakeEmbeddedStore: newFakeEmbeddedStore()}
	store.rows["demo"] = &EmbeddedPluginStateRow{PackageID: "demo", State: EmbeddedStateApproved}
	lc := NewEmbeddedPluginLifecycle(store)

	err := lc.GuardEmbeddedPluginSelection(context.Background(), uuid.New(), []string{"demo"})
	refusal := asRefusal(err)
	if refusal == nil || refusal.Code != "plugin_base_mismatch" {
		t.Fatalf("refusal = %#v, want plugin_base_mismatch", err)
	}
	// Non-embedded ids are unaffected (builtin path continues).
	if err := lc.GuardEmbeddedPluginSelection(context.Background(), uuid.New(), []string{"harmful"}); err != nil {
		t.Fatalf("builtin id must pass even on a stale base: %v", err)
	}
}

// asRefusal extracts the typed refusal, nil when the error is something else.
func asRefusal(err error) *EmbeddedSelectionRefusalError {
	e, ok := err.(*EmbeddedSelectionRefusalError)
	if ok {
		return e
	}
	return nil
}
