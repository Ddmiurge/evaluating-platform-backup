package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"evaluating_platform/internal/maclaw"
)

type fakeMaclawGateway struct {
	enabled           bool
	targetErr         error
	evidenceInstance  string
	lastQuery         maclaw.EvaluationResourceQuery
	lastCreated       maclaw.EvaluationResourceInput
	archived          bool
	lastTargetQuery   maclaw.EvaluationTargetQuery
	lastTargetCreated maclaw.EvaluationTargetInput
	lastProbeTargetID string
	lastRunCreated    maclaw.EvaluationRunInput
	lastReportID      string
	lastExportFormat  string
	lastEvidenceQuery maclaw.EvaluationEvidenceQuery
	lastEvidenceID    string
	lastJobQuery      maclaw.EvaluationJobQuery
	lastJobID         string
	lastCancelJobID   string
	lastRetryJobID    string
	lastResumeJobID   string
	lastRecoveryJobID string
}

func (f *fakeMaclawGateway) Enabled() bool { return f.enabled }

func (f *fakeMaclawGateway) SearchEvaluationResources(ctx context.Context, q maclaw.EvaluationResourceQuery) ([]maclaw.EvaluationResourceSummary, error) {
	_ = ctx
	f.lastQuery = q
	return []maclaw.EvaluationResourceSummary{
		{ID: "res_1", Handle: "evalres_handle", Name: "Prompt sample", Kind: maclaw.EvaluationResourceKindSample, Summary: "safe summary"},
	}, nil
}

func (f *fakeMaclawGateway) ArchiveResource(context.Context, string) (*maclaw.EvaluationResourceSummary, error) {
	f.archived = true
	return &maclaw.EvaluationResourceSummary{ID: "res_1", Handle: "evalres_handle", Status: maclaw.EvaluationResourceStatusArchived, Enabled: false}, nil
}

func (f *fakeMaclawGateway) SaveEvaluationResource(ctx context.Context, in maclaw.EvaluationResourceInput) (*maclaw.EvaluationResourceSummary, error) {
	_ = ctx
	f.lastCreated = in
	return &maclaw.EvaluationResourceSummary{ID: "res_1", Handle: "evalres_handle", Name: in.Name, Kind: in.Kind, Summary: in.Summary}, nil
}

func (f *fakeMaclawGateway) PreviewEvaluationResource(ctx context.Context, resourceID string) (*maclaw.EvaluationResourcePreview, error) {
	_ = ctx
	return &maclaw.EvaluationResourcePreview{
		Resource:         maclaw.EvaluationResourceSummary{ID: resourceID, Handle: "evalres_handle", Name: "Prompt sample", Kind: maclaw.EvaluationResourceKindSample},
		PayloadBytes:     15,
		PayloadLineCount: 2,
		PayloadSHA256:    "digest",
	}, nil
}

func (f *fakeMaclawGateway) SearchEvaluationTargets(ctx context.Context, q maclaw.EvaluationTargetQuery) ([]maclaw.EvaluationTargetSummary, error) {
	_ = ctx
	f.lastTargetQuery = q
	return []maclaw.EvaluationTargetSummary{
		{ID: "target_1", Name: "OpenAI target", Kind: maclaw.EvaluationTargetKindLLM, Provider: "openai", BaseURL: "https://api.example/v1", Model: "gpt-test", CredentialSecretSet: true},
	}, nil
}

func (f *fakeMaclawGateway) SaveEvaluationTarget(ctx context.Context, in maclaw.EvaluationTargetInput) (*maclaw.EvaluationTargetSummary, error) {
	_ = ctx
	f.lastTargetCreated = in
	return &maclaw.EvaluationTargetSummary{ID: "target_1", Name: in.Name, Kind: in.Kind, Provider: in.Provider, BaseURL: in.BaseURL, Model: in.Model, CredentialSecretSet: in.CredentialSecret != ""}, nil
}

func (f *fakeMaclawGateway) GetEvaluationTarget(ctx context.Context, targetID string) (*maclaw.EvaluationTargetSummary, error) {
	_ = ctx
	if f.targetErr != nil {
		return nil, f.targetErr
	}
	return &maclaw.EvaluationTargetSummary{ID: targetID, Name: "OpenAI target", Kind: maclaw.EvaluationTargetKindLLM, Provider: "openai", BaseURL: "https://api.example/v1", Model: "gpt-test", CredentialSecretSet: true}, nil
}

func (f *fakeMaclawGateway) ProbeEvaluationTarget(ctx context.Context, targetID string) (*maclaw.EvaluationTargetProbeResult, error) {
	_ = ctx
	f.lastProbeTargetID = targetID
	return &maclaw.EvaluationTargetProbeResult{
		Target:     maclaw.EvaluationTargetSummary{ID: targetID, Name: "OpenAI target", Kind: maclaw.EvaluationTargetKindLLM, Provider: "openai", BaseURL: "https://api.example/v1", Model: "gpt-test", CredentialSecretSet: true, HealthStatus: maclaw.EvaluationTargetHealthHealthy},
		Status:     maclaw.EvaluationTargetHealthHealthy,
		Message:    "target responded",
		StatusCode: 204,
	}, nil
}

func (f *fakeMaclawGateway) StartEvaluationRun(ctx context.Context, in maclaw.EvaluationRunInput) (*maclaw.EvaluationRunResult, error) {
	_ = ctx
	f.lastRunCreated = in
	score := 100.0
	return &maclaw.EvaluationRunResult{
		Run:      &maclaw.RuntimeRun{ID: "run_1", SessionID: in.SessionID, Status: "succeeded", Metadata: map[string]string{"report_id": "report_1"}},
		Evidence: &maclaw.EvaluationEvidenceSummary{ID: "evidence_1", InstanceID: in.InstanceID, SessionID: in.SessionID, RunID: "run_1", Kind: maclaw.EvaluationEvidenceKindResult, Title: "Target response", Summary: "status 200", Handle: "evalevh_1"},
		Report:   &maclaw.EvaluationReport{ID: "report_1", InstanceID: in.InstanceID, SessionID: in.SessionID, RunID: "run_1", Title: "Evaluation report", Summary: "ok", SafetyScore: &score},
	}, nil
}

func (f *fakeMaclawGateway) GetEvaluationReport(ctx context.Context, reportID string) (*maclaw.EvaluationReport, error) {
	_ = ctx
	f.lastReportID = reportID
	score := 88.0
	return &maclaw.EvaluationReport{ID: reportID, Title: "Safety report", Summary: "Summary text", RiskLevel: "low", SafetyScore: &score}, nil
}

func (f *fakeMaclawGateway) ExportEvaluationReport(ctx context.Context, reportID, format string) (*maclaw.EvaluationReportExport, error) {
	_ = ctx
	f.lastReportID = reportID
	f.lastExportFormat = format
	return &maclaw.EvaluationReportExport{
		ReportID:    reportID,
		Format:      "markdown",
		Filename:    "report_1.md",
		ContentType: "text/markdown; charset=utf-8",
		Content:     []byte("# Safety report\n"),
	}, nil
}

func (f *fakeMaclawGateway) ListEvaluationEvidence(ctx context.Context, q maclaw.EvaluationEvidenceQuery) ([]maclaw.EvaluationEvidenceSummary, error) {
	_ = ctx
	f.lastEvidenceQuery = q
	return []maclaw.EvaluationEvidenceSummary{
		{ID: "evidence_1", InstanceID: "inst_1", SessionID: "session_1", RunID: "run_1", Kind: maclaw.EvaluationEvidenceKindToolCall, Title: "Tool call", Summary: "status 200", Handle: "evalevh_1"},
	}, nil
}

func (f *fakeMaclawGateway) GetEvaluationEvidence(ctx context.Context, evidenceID string) (*maclaw.EvaluationEvidenceSummary, error) {
	_ = ctx
	f.lastEvidenceID = evidenceID
	instanceID := f.evidenceInstance
	if instanceID == "" {
		instanceID = "inst_1"
	}
	return &maclaw.EvaluationEvidenceSummary{ID: evidenceID, InstanceID: instanceID, SessionID: "session_1", RunID: "run_1", Kind: maclaw.EvaluationEvidenceKindToolCall, Title: "Tool call", Summary: "status 200", Handle: "evalevh_1"}, nil
}

func (f *fakeMaclawGateway) ListEvaluationJobs(ctx context.Context, q maclaw.EvaluationJobQuery) ([]maclaw.EvaluationJob, error) {
	_ = ctx
	f.lastJobQuery = q
	return []maclaw.EvaluationJob{{
		ID:     "job_1",
		Kind:   maclaw.EvaluationJobKindRun,
		Status: maclaw.EvaluationJobStatusSucceeded,
		Result: &maclaw.EvaluationRunResult{
			Run:      &maclaw.RuntimeRun{ID: "run_1", SessionID: "sess_1", Status: "succeeded"},
			Evidence: &maclaw.EvaluationEvidenceSummary{ID: "evidence_1", RunID: "run_1", Kind: maclaw.EvaluationEvidenceKindResult, Summary: "status 200", Handle: "evalevh_1"},
			Report:   &maclaw.EvaluationReport{ID: "report_1", RunID: "run_1", Title: "Evaluation report", Summary: "ok"},
		},
	}}, nil
}

func (f *fakeMaclawGateway) GetEvaluationJob(ctx context.Context, jobID string) (*maclaw.EvaluationJob, error) {
	_ = ctx
	f.lastJobID = jobID
	return &maclaw.EvaluationJob{
		ID:     jobID,
		Kind:   maclaw.EvaluationJobKindRun,
		Status: maclaw.EvaluationJobStatusSucceeded,
		Result: &maclaw.EvaluationRunResult{
			Run:      &maclaw.RuntimeRun{ID: "run_1", SessionID: "sess_1", Status: "succeeded"},
			Evidence: &maclaw.EvaluationEvidenceSummary{ID: "evidence_1", RunID: "run_1", Kind: maclaw.EvaluationEvidenceKindResult, Summary: "status 200", Handle: "evalevh_1"},
			Report:   &maclaw.EvaluationReport{ID: "report_1", RunID: "run_1", Title: "Evaluation report", Summary: "ok"},
		},
	}, nil
}

func (f *fakeMaclawGateway) CancelEvaluationJob(ctx context.Context, jobID string) (*maclaw.EvaluationJob, error) {
	_ = ctx
	f.lastCancelJobID = jobID
	return &maclaw.EvaluationJob{ID: jobID, Kind: maclaw.EvaluationJobKindRun, Status: maclaw.EvaluationJobStatusCanceled}, nil
}

func (f *fakeMaclawGateway) RetryEvaluationJob(ctx context.Context, jobID string) (*maclaw.EvaluationJob, error) {
	_ = ctx
	f.lastRetryJobID = jobID
	return &maclaw.EvaluationJob{ID: "job_retry", Kind: maclaw.EvaluationJobKindRun, Status: maclaw.EvaluationJobStatusPending}, nil
}

func (f *fakeMaclawGateway) ResumeEvaluationJob(ctx context.Context, jobID string) (*maclaw.EvaluationJob, error) {
	_ = ctx
	f.lastResumeJobID = jobID
	return &maclaw.EvaluationJob{ID: "job_resume", Kind: maclaw.EvaluationJobKindRun, Status: maclaw.EvaluationJobStatusPending}, nil
}

func (f *fakeMaclawGateway) GetEvaluationJobRecovery(ctx context.Context, jobID string) (*maclaw.EvaluationJobRecovery, error) {
	_ = ctx
	f.lastRecoveryJobID = jobID
	return &maclaw.EvaluationJobRecovery{
		JobID:                jobID,
		RunID:                "run_1",
		Action:               "manual_review",
		Strategy:             "manual_review",
		Step:                 "calling_target",
		CompletedStepIDs:     []string{"materializing_resources"},
		CanRetry:             false,
		ManualReviewRequired: true,
		Reason:               "target may have been called before interruption",
		RecoveryIndexed:      true,
	}, nil
}

func TestMaclawEvaluationHandlerStartsRunWithoutPayloadOrSecretLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandlerWithResolver(gateway, maclaw.NewInstanceResolver("inst_mapped", nil))
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/runs", handler.StartRun)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/runs", strings.NewReader(`{"instance_id":"inst_from_browser_must_be_ignored","session_id":"sess_1","target_id":"target_1","resource_handles":["evalres_handle"],"goal":"Run one check"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastRunCreated.InstanceID != "inst_mapped" || gateway.lastRunCreated.SessionID != "sess_1" || gateway.lastRunCreated.TargetID != "target_1" || len(gateway.lastRunCreated.ResourceHandles) != 1 || gateway.lastRunCreated.ResourceHandles[0] != "evalres_handle" {
		t.Fatalf("created run = %#v", gateway.lastRunCreated)
	}
	if strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), `"content"`) || strings.Contains(w.Body.String(), `"credential_secret"`) {
		t.Fatalf("start run response leaked sensitive data: %s", w.Body.String())
	}
	var resp maclaw.EvaluationRunResult
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Run == nil || resp.Run.ID != "run_1" || resp.Evidence == nil || resp.Evidence.Handle != "evalevh_1" || resp.Report == nil || resp.Report.ID != "report_1" {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestMaclawEvaluationHandlerRejectsExpertExecutionSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandlerWithResolver(gateway, maclaw.NewInstanceResolver("inst_mapped", nil))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_role", "expert")
		c.Next()
	})
	router.POST("/maclaw/evaluation/runs", handler.StartRun)
	router.POST("/maclaw/evaluation/targets", handler.CreateTarget)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/runs", strings.NewReader(`{"session_id":"sess_1","target_id":"target_1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("run status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastRunCreated.SessionID != "" {
		t.Fatalf("expert run should not reach gateway: %#v", gateway.lastRunCreated)
	}

	req = httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/targets", strings.NewReader(`{"name":"target","kind":"llm","provider":"openai","base_url":"https://target.example/v1","model":"gpt-test"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("target status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastTargetCreated.Name != "" {
		t.Fatalf("expert target should not reach gateway: %#v", gateway.lastTargetCreated)
	}
}

func TestMaclawEvaluationHandlerRejectsMissingRoleExecutionSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandlerWithResolver(gateway, maclaw.NewInstanceResolver("inst_mapped", nil))
	router := gin.New()
	router.POST("/maclaw/evaluation/runs", handler.StartRun)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/runs", strings.NewReader(`{"session_id":"sess_1","target_id":"target_1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastRunCreated.SessionID != "" {
		t.Fatalf("missing role request should not reach gateway: %#v", gateway.lastRunCreated)
	}
}

func TestMaclawEvaluationHandlerListsSummaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/resources", handler.ListResources)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/resources?kind=sample&query=prompt&assessment_type=llm_safety&assessment_type=jailbreak&include_inactive=true&limit=7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastQuery.Kind != maclaw.EvaluationResourceKindSample ||
		gateway.lastQuery.Query != "prompt" ||
		!gateway.lastQuery.IncludeInactive ||
		gateway.lastQuery.Limit != 7 {
		t.Fatalf("query = %#v", gateway.lastQuery)
	}
	if len(gateway.lastQuery.AssessmentTypes) != 2 {
		t.Fatalf("assessment types = %#v", gateway.lastQuery.AssessmentTypes)
	}
	if strings.Contains(w.Body.String(), "payload") {
		t.Fatalf("list response should not expose payload: %s", w.Body.String())
	}
	var resp struct {
		Items []maclaw.EvaluationResourceSummary `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Handle != "evalres_handle" {
		t.Fatalf("items = %#v", resp.Items)
	}
}

func TestMaclawEvaluationHandlerListsEvaluationJobsWithoutSensitiveResultFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/jobs", handler.ListJobs)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/jobs?kind=mcp.start&status=succeeded&limit=4", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastJobQuery.Kind != maclaw.EvaluationJobKindRun || gateway.lastJobQuery.Status != maclaw.EvaluationJobStatusSucceeded || gateway.lastJobQuery.Limit != 4 {
		t.Fatalf("job query = %#v", gateway.lastJobQuery)
	}
	if strings.Contains(w.Body.String(), `"content"`) || strings.Contains(w.Body.String(), `"credential_secret"`) || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatalf("job list leaked sensitive result fields: %s", w.Body.String())
	}
	var resp struct {
		Items []maclaw.EvaluationJob `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != "job_1" || resp.Items[0].Result == nil || resp.Items[0].Result.Report == nil {
		t.Fatalf("items = %#v", resp.Items)
	}
}

func TestMaclawEvaluationHandlerGetsAndCancelsEvaluationJob(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/jobs/:id", handler.GetJob)
	router.GET("/maclaw/evaluation/jobs/:id/recovery", handler.GetJobRecovery)
	router.POST("/maclaw/evaluation/jobs/:id/cancel", handler.CancelJob)
	router.POST("/maclaw/evaluation/jobs/:id/retry", handler.RetryJob)
	router.POST("/maclaw/evaluation/jobs/:id/resume", handler.ResumeJob)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/jobs/job_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastJobID != "job_1" || strings.Contains(w.Body.String(), `"content"`) || strings.Contains(w.Body.String(), `"credential_secret"`) {
		t.Fatalf("get job response = %s last id = %q", w.Body.String(), gateway.lastJobID)
	}

	req = httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/jobs/job_1/recovery", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("recovery status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastRecoveryJobID != "job_1" || !strings.Contains(w.Body.String(), `"manual_review_required":true`) || strings.Contains(w.Body.String(), `"content"`) || strings.Contains(w.Body.String(), `"credential_secret"`) {
		t.Fatalf("recovery response = %s last id = %q", w.Body.String(), gateway.lastRecoveryJobID)
	}

	req = httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/jobs/job_1/cancel", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastCancelJobID != "job_1" || !strings.Contains(w.Body.String(), `"status":"canceled"`) {
		t.Fatalf("cancel job response = %s last id = %q", w.Body.String(), gateway.lastCancelJobID)
	}

	req = httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/jobs/job_1/retry", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastRetryJobID != "job_1" || !strings.Contains(w.Body.String(), `"id":"job_retry"`) {
		t.Fatalf("retry job response = %s last id = %q", w.Body.String(), gateway.lastRetryJobID)
	}

	req = httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/jobs/job_1/resume", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastResumeJobID != "job_1" || !strings.Contains(w.Body.String(), `"id":"job_resume"`) {
		t.Fatalf("resume job response = %s last id = %q", w.Body.String(), gateway.lastResumeJobID)
	}
}

func TestMaclawEvaluationHandlerListsTargetSummaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/targets", handler.ListTargets)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/targets?kind=llm&provider=openai&query=safety&include_inactive=true&limit=5", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastTargetQuery.Kind != maclaw.EvaluationTargetKindLLM ||
		gateway.lastTargetQuery.Provider != "openai" ||
		gateway.lastTargetQuery.Query != "safety" ||
		!gateway.lastTargetQuery.IncludeInactive ||
		gateway.lastTargetQuery.Limit != 5 {
		t.Fatalf("query = %#v", gateway.lastTargetQuery)
	}
	if strings.Contains(w.Body.String(), `"credential_secret":`) || strings.Contains(w.Body.String(), "sk-") {
		t.Fatalf("list response leaked credential secret: %s", w.Body.String())
	}
	var resp struct {
		Items []maclaw.EvaluationTargetSummary `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ID != "target_1" || !resp.Items[0].CredentialSecretSet {
		t.Fatalf("items = %#v", resp.Items)
	}
}

func TestMaclawEvaluationHandlerListsEvidenceSummariesWithoutContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/evidence", handler.ListEvidence)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/evidence?instance_id=inst_1&session_id=session_1&run_id=run_1&kind=tool_call&limit=3", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastEvidenceQuery.InstanceID != "inst_1" ||
		gateway.lastEvidenceQuery.SessionID != "session_1" ||
		gateway.lastEvidenceQuery.RunID != "run_1" ||
		gateway.lastEvidenceQuery.Kind != maclaw.EvaluationEvidenceKindToolCall ||
		gateway.lastEvidenceQuery.Limit != 3 {
		t.Fatalf("query = %#v", gateway.lastEvidenceQuery)
	}
	if strings.Contains(w.Body.String(), `"content"`) || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatalf("list response leaked evidence content: %s", w.Body.String())
	}
	var resp struct {
		Items []maclaw.EvaluationEvidenceSummary `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].Handle != "evalevh_1" {
		t.Fatalf("items = %#v", resp.Items)
	}
}

func TestMaclawEvaluationHandlerListEvidenceUsesMappedInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandlerWithResolver(gateway, maclaw.NewInstanceResolver("inst_mapped", nil))
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/evidence", handler.ListEvidence)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/evidence?instance_id=inst_from_browser_must_be_ignored&session_id=session_1&run_id=run_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastEvidenceQuery.InstanceID != "inst_mapped" {
		t.Fatalf("instance id = %q, want mapped instance", gateway.lastEvidenceQuery.InstanceID)
	}
	if strings.Contains(w.Body.String(), "inst_from_browser_must_be_ignored") {
		t.Fatalf("response should not echo browser supplied instance: %s", w.Body.String())
	}
}

func TestMaclawEvaluationHandlerGetsEvidenceSummaryWithoutContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/evidence/:id", handler.GetEvidence)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/evidence/evidence_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastEvidenceID != "evidence_1" {
		t.Fatalf("evidence id = %q", gateway.lastEvidenceID)
	}
	if strings.Contains(w.Body.String(), `"content"`) || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatalf("get response leaked evidence content: %s", w.Body.String())
	}
	var resp maclaw.EvaluationEvidenceSummary
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "evidence_1" || resp.Handle != "evalevh_1" {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestMaclawEvaluationHandlerHidesEvidenceFromDifferentInstance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true, evidenceInstance: "inst_other"}
	handler := NewMaclawEvaluationHandlerWithResolver(gateway, maclaw.NewInstanceResolver("inst_mapped", nil))
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/evidence/:id", handler.GetEvidence)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/evidence/evidence_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "evalevh_1") || strings.Contains(w.Body.String(), "inst_other") {
		t.Fatalf("cross-instance evidence leaked in response: %s", w.Body.String())
	}
}

func TestMaclawEvaluationHandlerCreatesResourceSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/resources", handler.CreateResource)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/resources", strings.NewReader(`{"name":"Prompt sample","kind":"sample","summary":"safe","payload":"SECRET"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastCreated.Payload != "SECRET" {
		t.Fatalf("created payload = %q", gateway.lastCreated.Payload)
	}
	if strings.Contains(w.Body.String(), "SECRET") {
		t.Fatalf("create response leaked payload: %s", w.Body.String())
	}
}

func TestMaclawEvaluationHandlerCreatesTargetSummaryWithoutSecretLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/targets", handler.CreateTarget)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/targets", strings.NewReader(`{"name":"OpenAI target","kind":"llm","provider":"openai","base_url":"https://api.example/v1","model":"gpt-test","auth_type":"bearer","credential_secret":"sk-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastTargetCreated.CredentialSecret != "sk-secret" {
		t.Fatalf("created target secret = %q", gateway.lastTargetCreated.CredentialSecret)
	}
	if strings.Contains(w.Body.String(), `"credential_secret":`) || strings.Contains(w.Body.String(), "sk-secret") {
		t.Fatalf("create response leaked credential secret: %s", w.Body.String())
	}
}

func TestMaclawEvaluationHandlerNormalizesLocalhostTargetURLForDockerRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/targets", handler.CreateTarget)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/targets", strings.NewReader(`{"name":"Local target","kind":"llm","provider":"openai","base_url":"http://localhost:48760/v1","model":"gpt-test","auth_type":"bearer","credential_secret":"sk-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastTargetCreated.BaseURL != "http://host.docker.internal:48760/v1" {
		t.Fatalf("base_url = %q", gateway.lastTargetCreated.BaseURL)
	}
	if gateway.lastTargetCreated.Metadata["health_url"] != "http://host.docker.internal:48760/v1/models" {
		t.Fatalf("health_url = %q", gateway.lastTargetCreated.Metadata["health_url"])
	}
}

func TestMaclawEvaluationHandlerKeepsLocalhostTargetURLForLocalRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandlerWithResolver(gateway, nil)
	handler.SetRuntimeMode("local")
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/targets", handler.CreateTarget)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/targets", strings.NewReader(`{"name":"Local target","kind":"llm","provider":"openai","base_url":"http://127.0.0.1:48760/v1","model":"gpt-test","auth_type":"bearer","credential_secret":"sk-secret"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastTargetCreated.BaseURL != "http://127.0.0.1:48760/v1" {
		t.Fatalf("base_url = %q", gateway.lastTargetCreated.BaseURL)
	}
	if gateway.lastTargetCreated.Metadata["health_url"] != "http://127.0.0.1:48760/v1/models" {
		t.Fatalf("health_url = %q", gateway.lastTargetCreated.Metadata["health_url"])
	}
}

func TestMaclawEvaluationHandlerGetsTargetSummaryWithoutSecretLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/targets/:id", handler.GetTarget)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/targets/target_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"credential_secret":`) || strings.Contains(w.Body.String(), "sk-") {
		t.Fatalf("get response leaked credential secret: %s", w.Body.String())
	}
	var resp maclaw.EvaluationTargetSummary
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "target_1" || !resp.CredentialSecretSet {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestMaclawEvaluationHandlerProbesTargetWithoutSecretLeak(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.POST("/maclaw/evaluation/targets/:id/health-check", handler.ProbeTarget)

	req := httptest.NewRequest(http.MethodPost, "/maclaw/evaluation/targets/target_1/health-check", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastProbeTargetID != "target_1" {
		t.Fatalf("target id = %q", gateway.lastProbeTargetID)
	}
	if strings.Contains(w.Body.String(), `"credential_secret":`) || strings.Contains(w.Body.String(), "sk-") {
		t.Fatalf("probe response leaked credential secret: %s", w.Body.String())
	}
	var resp maclaw.EvaluationTargetProbeResult
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != maclaw.EvaluationTargetHealthHealthy || resp.Target.ID != "target_1" {
		t.Fatalf("resp = %#v", resp)
	}
}

func TestMaclawEvaluationHandlerPreviewsResourceWithoutPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/resources/:id/preview", handler.PreviewResource)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/resources/res_1/preview", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "payload") && strings.Contains(w.Body.String(), "SECRET") {
		t.Fatalf("preview response leaked payload: %s", w.Body.String())
	}
	var preview maclaw.EvaluationResourcePreview
	if err := json.NewDecoder(w.Body).Decode(&preview); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if preview.Resource.ID != "res_1" || preview.PayloadLineCount != 2 {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestMaclawEvaluationHandlerGetsReport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/reports/:id", handler.GetReport)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/reports/report_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastReportID != "report_1" {
		t.Fatalf("report id = %q", gateway.lastReportID)
	}
	if !strings.Contains(w.Body.String(), "Safety report") || strings.Contains(w.Body.String(), "maclaw-token") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestMaclawEvaluationHandlerExportsReport(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{enabled: true}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/reports/:id/export", handler.ExportReport)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/reports/report_1/export?format=markdown", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if gateway.lastReportID != "report_1" || gateway.lastExportFormat != "markdown" {
		t.Fatalf("report id = %q format = %q", gateway.lastReportID, gateway.lastExportFormat)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "text/markdown") {
		t.Fatalf("content type = %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "report_1.md") {
		t.Fatalf("content disposition = %q", got)
	}
	if w.Body.String() != "# Safety report\n" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

func TestMaclawEvaluationHandlerReturnsUnavailableWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewMaclawEvaluationHandler(&fakeMaclawGateway{})
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/resources", handler.ListResources)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/resources", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestMaclawEvaluationHandlerMapsUpstreamErrorsSafely(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gateway := &fakeMaclawGateway{
		enabled:   true,
		targetErr: maclaw.NewUpstreamError(http.MethodGet, "/api/v1/evaluation/targets/target_1", http.StatusUnauthorized, `{"error":"bad token sk-secret"}`),
	}
	handler := NewMaclawEvaluationHandler(gateway)
	router := newEnterpriseRoleRouter()
	router.GET("/maclaw/evaluation/targets/:id", handler.GetTarget)

	req := httptest.NewRequest(http.MethodGet, "/maclaw/evaluation/targets/target_1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"maclaw_auth_failed"`) {
		t.Fatalf("expected structured auth failure, body = %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-secret") || strings.Contains(w.Body.String(), "/api/v1/evaluation/targets/target_1") {
		t.Fatalf("response leaked upstream secret or internal path: %s", w.Body.String())
	}
}

// The archive route must actually reach the gateway with the path parameter the
// handler reads. A route wired as `:resource_id` while the handler reads `:id`
// would 400 on every call, and no service test would notice.
func TestMaclawEvaluationHandlerArchiveResource(t *testing.T) {
	gateway := &fakeMaclawGateway{enabled: true}
	h := NewMaclawEvaluationHandler(gateway)
	router := gin.New()
	router.POST("/maclaw/evaluation/resources/:id/archive", h.ArchiveResource)

	w := doJSON(router, http.MethodPost, "/maclaw/evaluation/resources/res_1/archive", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if !gateway.archived {
		t.Error("gateway.ArchiveResource was not called")
	}
	if !strings.Contains(w.Body.String(), `"archived"`) {
		t.Errorf("response does not carry the archived status: %s", w.Body.String())
	}
}
