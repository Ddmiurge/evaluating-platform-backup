package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"evaluating_platform/internal/maclaw"
)

// planMessageFixture is a plan_confirm card the user confirmed.
func planMessageFixture(id string) maclaw.RuntimeMessage {
	return maclaw.RuntimeMessage{
		ID:   id,
		Role: "assistant",
		Metadata: map[string]string{
			"card_type":       "plan_confirm",
			"response_source": "plan_confirm",
		},
	}
}

func succeededEngineJobFixture(jobID, sessionID, planMessageID, reportID string) *maclaw.PlatformEngineJob {
	completed := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	return &maclaw.PlatformEngineJob{
		ID:            jobID,
		EngineRunID:   "er-" + jobID,
		PlanMessageID: planMessageID,
		SessionID:     sessionID,
		Status:        maclaw.EvaluationJobStatusSucceeded,
		PlannedCount:  6,
		ExecutedCount: 6,
		ReportID:      reportID,
		StartedAt:     completed.Add(-time.Minute),
		CompletedAt:   &completed,
	}
}

// TestGetSessionSynthesizesTheEngineReportCard covers the WIRING, not just the
// helper.
//
// The other tests in this file call `appendCompletedEngineReportMessages`
// directly, so they stay green if its call is removed from GetSession — exactly
// the call-site-wiring trap U8 hit, where a counter-proof deleted the wiring and
// the suite still passed. This one drives the HTTP handler, so removing that call
// turns it red.
func TestGetSessionSynthesizesTheEngineReportCard(t *testing.T) {
	gateway := &fakeMaclawRuntimeGateway{
		enabled: true,
		messages: []maclaw.RuntimeMessage{
			{Role: "user", Content: "对当前被测模型做合规安全评测"},
			planMessageFixture("plan-wire"),
		},
	}
	handler := NewMaclawRuntimeHandler(gateway, "inst_1")
	jobs := maclaw.NewPlatformEngineJobStore()
	jobs.Create(succeededEngineJobFixture("pfj-wire", "sess_1", "plan-wire", "redteam_report_wire"))
	handler.engineJobs = jobs

	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/sessions/:id", handler.GetSession)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/sessions/sess_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"report_id":"redteam_report_wire"`,
		`"downloadable":"true"`,
		`"download_format":"pdf"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("session snapshot is missing %s:\n%s", want, body)
		}
	}
	// And it sits directly after its plan card.
	planIdx := strings.Index(body, `"plan-wire"`)
	reportIdx := strings.Index(body, `"engine-report:redteam_report_wire"`)
	if planIdx < 0 || reportIdx < 0 || reportIdx < planIdx {
		t.Fatalf("report card is not after its plan card (plan@%d report@%d):\n%s", planIdx, reportIdx, body)
	}
}

// TestAppendCompletedEngineReportMessagesAddsDownloadableCard is the 2026-10-16
// gap being closed: the engine fast path never wrote a report message, so a
// finished evaluation produced a stored, exportable PDF that the chat never
// showed.
func TestAppendCompletedEngineReportMessagesAddsDownloadableCard(t *testing.T) {
	const sessionID = "sess_report_card"
	messages := []maclaw.RuntimeMessage{
		{Role: "user", Content: "对当前被测模型做合规安全评测"},
		planMessageFixture("plan-1"),
	}

	out := appendCompletedEngineReportMessages(context.Background(), nil, uuid.Nil, messages,
		sessionID, []*maclaw.PlatformEngineJob{
			succeededEngineJobFixture("pfj-1", sessionID, "plan-1", "redteam_report_abc"),
		})

	if len(out) != 3 {
		t.Fatalf("messages = %d, want the report card appended (3)", len(out))
	}
	card := out[2]
	if card.Metadata["report_id"] != "redteam_report_abc" {
		t.Errorf("report_id = %q", card.Metadata["report_id"])
	}
	if card.Metadata["card_type"] != "report" {
		t.Errorf("card_type = %q, want report", card.Metadata["card_type"])
	}
	// The download button gates on these three.
	if card.Metadata["downloadable"] != "true" {
		t.Errorf("downloadable = %q, want true", card.Metadata["downloadable"])
	}
	if card.Metadata["download_format"] != "pdf" {
		t.Errorf("download_format = %q, want pdf", card.Metadata["download_format"])
	}
	if card.Role != "assistant" {
		t.Errorf("role = %q, want assistant", card.Role)
	}
	if card.CreatedAt.IsZero() {
		t.Error("created_at is zero; the card would sort at the epoch")
	}
}

// TestAppendCompletedEngineReportMessagesPlacesCardAfterItsPlan pins the
// placement rule.
//
// The snapshot is rebuilt on every session load, so an appended card would sink
// below every later message and re-appear at the bottom each time. It belongs
// right after the plan the user approved.
func TestAppendCompletedEngineReportMessagesPlacesCardAfterItsPlan(t *testing.T) {
	const sessionID = "sess_placement"
	messages := []maclaw.RuntimeMessage{
		{Role: "user", Content: "第一条"},
		planMessageFixture("plan-1"),
		{Role: "user", Content: "评测跑完之后我又问了一句"},
	}

	out := appendCompletedEngineReportMessages(context.Background(), nil, uuid.Nil, messages,
		sessionID, []*maclaw.PlatformEngineJob{
			succeededEngineJobFixture("pfj-1", sessionID, "plan-1", "redteam_report_place"),
		})

	if len(out) != 4 {
		t.Fatalf("messages = %d, want 4", len(out))
	}
	if out[1].ID != "plan-1" {
		t.Fatalf("message 1 = %q, want the plan card", out[1].ID)
	}
	if out[2].Metadata["report_id"] != "redteam_report_place" {
		t.Fatalf("message 2 = %+v, want the report card directly after its plan", out[2])
	}
	if out[3].Content != "评测跑完之后我又问了一句" {
		t.Fatalf("message 3 = %+v, want the later user message", out[3])
	}
}

// TestAppendCompletedEngineReportMessagesFallsBackToAppend pins that a missing
// anchor costs the position, never the card.
func TestAppendCompletedEngineReportMessagesFallsBackToAppend(t *testing.T) {
	messages := []maclaw.RuntimeMessage{{Role: "user", Content: "hi"}}
	out := appendCompletedEngineReportMessages(context.Background(), nil, uuid.Nil, messages,
		"sess_x", []*maclaw.PlatformEngineJob{
			succeededEngineJobFixture("pfj-1", "sess_x", "plan-missing", "redteam_report_late"),
		})
	if len(out) != 2 || out[1].Metadata["report_id"] != "redteam_report_late" {
		t.Fatalf("out = %+v, want the card appended", out)
	}
}

// TestAppendCompletedEngineReportMessagesIgnoresUnfinishedAndReportless pins the
// two states that must not produce a card: a run that has not succeeded, and a
// succeeded run whose report id is empty (which would render a download button
// pointing at nothing).
func TestAppendCompletedEngineReportMessagesIgnoresUnfinishedAndReportless(t *testing.T) {
	running := succeededEngineJobFixture("pfj-run", "sess_y", "plan-1", "redteam_report_r")
	running.Status = maclaw.EvaluationJobStatusRunning
	running.ReportID = ""

	noReport := succeededEngineJobFixture("pfj-noreport", "sess_y", "plan-1", "")
	noReport.ReportID = ""

	failed := succeededEngineJobFixture("pfj-failed", "sess_y", "plan-1", "redteam_report_f")
	failed.Status = maclaw.EvaluationJobStatusFailed

	out := appendCompletedEngineReportMessages(context.Background(), nil, uuid.Nil,
		[]maclaw.RuntimeMessage{planMessageFixture("plan-1")},
		"sess_y", []*maclaw.PlatformEngineJob{running, noReport, failed})

	if len(out) != 1 {
		t.Fatalf("out = %+v, want no card for a running, reportless, or failed job", out)
	}
}

// TestAppendCompletedEngineReportMessagesDoesNotDuplicate pins the dedupe.
//
// The snapshot is rebuilt on every load, so without this the card would multiply
// once per poll — and on the agent-loop path MaClaw writes the report message
// itself, so the guard has to be per-report rather than per-path.
func TestAppendCompletedEngineReportMessagesDoesNotDuplicate(t *testing.T) {
	already := maclaw.RuntimeMessage{
		ID:       "engine-report:redteam_report_dup",
		Role:     "assistant",
		Metadata: map[string]string{"card_type": "report", "report_id": "redteam_report_dup"},
	}
	messages := []maclaw.RuntimeMessage{planMessageFixture("plan-1"), already}

	out := appendCompletedEngineReportMessages(context.Background(), nil, uuid.Nil, messages,
		"sess_dup", []*maclaw.PlatformEngineJob{
			succeededEngineJobFixture("pfj-1", "sess_dup", "plan-1", "redteam_report_dup"),
		})

	if len(out) != 2 {
		t.Fatalf("out = %+v, want the existing card left alone", out)
	}
}

// TestPlatformEngineJobStoreListBySession pins the session lookup the card
// depends on: wrong-session jobs must not leak a card into someone else's
// conversation.
func TestPlatformEngineJobStoreListBySession(t *testing.T) {
	store := maclaw.NewPlatformEngineJobStore()
	store.Create(succeededEngineJobFixture("pfj-a", "sess-1", "plan-1", "report-a"))
	store.Create(succeededEngineJobFixture("pfj-b", "sess-2", "plan-2", "report-b"))
	store.Create(succeededEngineJobFixture("pfj-c", "sess-1", "plan-3", "report-c"))

	got := store.ListBySession("sess-1")
	if len(got) != 2 {
		t.Fatalf("jobs = %d, want 2", len(got))
	}
	for _, job := range got {
		if job.SessionID != "sess-1" {
			t.Errorf("job %s belongs to %s", job.ID, job.SessionID)
		}
	}
	if len(store.ListBySession("sess-none")) != 0 {
		t.Error("unknown session returned jobs")
	}
	if len(store.ListBySession("")) != 0 {
		t.Error("empty session id returned jobs")
	}
}

func runningEngineJobFixture(jobID, sessionID, planMessageID string) *maclaw.PlatformEngineJob {
	return &maclaw.PlatformEngineJob{
		ID:            jobID,
		EngineRunID:   "er-" + jobID,
		PlanMessageID: planMessageID,
		SessionID:     sessionID,
		Status:        maclaw.EvaluationJobStatusRunning,
		PlannedCount:  6,
		ExecutedCount: 2,
		CurrentStage:  "generating_tests",
		StatusText:    "promptfoo 原生生成攻击用例",
		DurationMs:    42000,
		StartedAt:     time.Date(2026, time.October, 26, 15, 20, 0, 0, time.UTC),
	}
}

// A running evaluation must be visible in the session snapshot, not only in the
// browser's local state. Before 2026-10-26 it vanished on refresh, and a user
// who saw it disappear re-submitted the plan (record 37: two jobs, two reports).
func TestAppendRunningEngineProgressMessagesAddsASafeCard(t *testing.T) {
	const sessionID = "sess_progress"
	messages := []maclaw.RuntimeMessage{
		{Role: "user", Content: "做一次合规评测"},
		planMessageFixture("plan-progress"),
	}
	out := appendRunningEngineProgressMessages(messages, sessionID, []*maclaw.PlatformEngineJob{
		runningEngineJobFixture("pfj-run", sessionID, "plan-progress"),
	})
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3 (user + plan + progress)", len(out))
	}
	card := out[2]
	if card.ID != "engine-progress:pfj-run" {
		t.Errorf("card id = %q", card.ID)
	}
	if out[1].ID != "plan-progress" || card.ID == out[1].ID {
		t.Fatal("progress card does not follow its plan card")
	}
	for key, want := range map[string]string{
		"card_type":      "progress",
		"job_id":         "pfj-run",
		"phase":          "running",
		"planned_count":  "6",
		"executed_count": "2",
		"current_stage":  "generating_tests",
		"duration_ms":    "42000",
	} {
		if got := card.Metadata[key]; got != want {
			t.Errorf("metadata[%s] = %q, want %q", key, got, want)
		}
	}
	// Safe fields only: nothing that could carry content, credentials or paths.
	for key := range card.Metadata {
		for _, banned := range []string{"prompt", "payload", "response", "content", "token", "secret", "path"} {
			if strings.Contains(strings.ToLower(key), banned) {
				t.Errorf("progress metadata carries a banned key %q", key)
			}
		}
	}

	// A terminal job belongs to the report path; no progress card.
	terminal := runningEngineJobFixture("pfj-done", sessionID, "plan-progress")
	terminal.Status = maclaw.EvaluationJobStatusSucceeded
	if got := appendRunningEngineProgressMessages(messages, sessionID, []*maclaw.PlatformEngineJob{terminal}); len(got) != 2 {
		t.Errorf("messages = %d after a terminal job, want 2", len(got))
	}

	// And it must not duplicate a message that already carries the job id.
	withExisting := append(out, maclaw.RuntimeMessage{Metadata: map[string]string{"job_id": "pfj-run"}})
	if got := appendRunningEngineProgressMessages(withExisting, sessionID, []*maclaw.PlatformEngineJob{runningEngineJobFixture("pfj-run", sessionID, "plan-progress")}); len(got) != 4 {
		t.Errorf("messages = %d, want no duplicate progress card", len(got))
	}
}

// Wiring: GetSession must call the helper. Without this, the helper can be
// perfect while a refresh still shows nothing — the call-site trap this project
// has hit before (U8).
func TestGetSessionSynthesizesRunningEngineProgressCard(t *testing.T) {
	gateway := &fakeMaclawRuntimeGateway{
		enabled: true,
		messages: []maclaw.RuntimeMessage{
			{Role: "user", Content: "对当前被测模型做合规安全评测"},
			planMessageFixture("plan-run"),
		},
	}
	handler := NewMaclawRuntimeHandler(gateway, "inst_1")
	jobs := maclaw.NewPlatformEngineJobStore()
	jobs.Create(runningEngineJobFixture("pfj-wire", "sess_1", "plan-run"))
	handler.engineJobs = jobs

	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/sessions/:id", handler.GetSession)
	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/sessions/sess_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"engine-progress:pfj-wire"`, `"card_type":"progress"`, `"current_stage":"generating_tests"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("session snapshot is missing %s:\n%s", want, body)
		}
	}
	planIdx := strings.Index(body, `"plan-run"`)
	cardIdx := strings.Index(body, `"engine-progress:pfj-wire"`)
	if planIdx < 0 || cardIdx < 0 || cardIdx < planIdx {
		t.Fatalf("progress card is not after its plan card (plan@%d card@%d)", planIdx, cardIdx)
	}
}
