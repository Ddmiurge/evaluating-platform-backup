package maclaw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSearchEvaluationResourcesUsesBFFTokenAndQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/resources" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		q := r.URL.Query()
		if q.Get("kind") != "sample" || q.Get("query") != "prompt" || q.Get("include_inactive") != "true" || q.Get("limit") != "7" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		if got := q["assessment_type"]; len(got) != 2 || got[0] != "llm_safety" || got[1] != "jailbreak" {
			t.Fatalf("assessment_type = %#v", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "res_1", "handle": "evalres_handle", "name": "Prompt sample", "kind": "sample", "summary": "safe summary"},
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{
		BaseURL:        upstream.URL,
		APIToken:       "maclaw-token",
		TimeoutSeconds: 3,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	items, err := client.SearchEvaluationResources(context.Background(), EvaluationResourceQuery{
		Kind:            EvaluationResourceKindSample,
		Query:           "prompt",
		AssessmentTypes: []string{"llm_safety", "jailbreak"},
		IncludeInactive: true,
		Limit:           7,
	})
	if err != nil {
		t.Fatalf("SearchEvaluationResources: %v", err)
	}
	if len(items) != 1 || items[0].Handle != "evalres_handle" {
		t.Fatalf("items = %#v", items)
	}
}

func TestClientRuntimeCapabilitiesFallsBackToShadowProfileWhenEndpointMissing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/capabilities" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	caps, err := client.GetRuntimeCapabilities(context.Background())
	if err != nil {
		t.Fatalf("GetRuntimeCapabilities: %v", err)
	}
	if caps.Profile != RuntimeCapabilityProfileShadow {
		t.Fatalf("profile = %q, want %q", caps.Profile, RuntimeCapabilityProfileShadow)
	}
	if caps.NativeResourceCatalog || caps.NativeSkillCatalog || caps.NativeCrossTenantReferences {
		t.Fatalf("missing capability endpoint should use conservative shadow fallback: %#v", caps)
	}
}

func TestClientRuntimeCapabilitiesReadsNativeCatalogProfile(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/capabilities" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(RuntimeCapabilities{
			Profile:                     RuntimeCapabilityProfileNativeCatalog,
			AutonomousSkillExecution:    true,
			NativeResourceCatalog:       true,
			NativeSkillCatalog:          true,
			NativeCrossTenantReferences: true,
			RedteamDomainProfile:        true,
			CapabilityCardContext:       true,
			StructuredRedteamPlanner:    true,
			ReportSchemaV1:              true,
			JobRecovery:                 true,
			ReportExport:                true,
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	caps, err := client.GetRuntimeCapabilities(context.Background())
	if err != nil {
		t.Fatalf("GetRuntimeCapabilities: %v", err)
	}
	if !caps.AutonomousSkillExecution || !caps.NativeResourceCatalog || !caps.NativeSkillCatalog || !caps.NativeCrossTenantReferences {
		t.Fatalf("native capabilities not decoded: %#v", caps)
	}
	if !caps.RedteamDomainProfile || !caps.CapabilityCardContext || !caps.StructuredRedteamPlanner || !caps.ReportSchemaV1 {
		t.Fatalf("redteam capability fields not decoded: %#v", caps)
	}
	if caps.SupportsNativeResourceProjection() || caps.SupportsNativeSkillProjection() {
		t.Fatalf("native projection must require explicit grant sync capability: %#v", caps)
	}

	caps.NativeCatalogGrantSync = true
	if !caps.SupportsNativeResourceProjection() || !caps.SupportsNativeSkillProjection() {
		t.Fatalf("native projection should be enabled when grant sync capability is explicit: %#v", caps)
	}
}

func TestClientCreateEvaluationResourceDoesNotReturnPayload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/resources" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var in EvaluationResourceInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.Payload != "SECRET-PAYLOAD" {
			t.Fatalf("payload = %q", in.Payload)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(EvaluationResourceSummary{
			ID:      "res_1",
			Handle:  "evalres_handle",
			Name:    in.Name,
			Kind:    in.Kind,
			Summary: in.Summary,
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: int(time.Second / time.Second)})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	summary, err := client.SaveEvaluationResource(context.Background(), EvaluationResourceInput{
		Name:    "Prompt sample",
		Kind:    EvaluationResourceKindSample,
		Summary: "safe",
		Payload: "SECRET-PAYLOAD",
	})
	if err != nil {
		t.Fatalf("SaveEvaluationResource: %v", err)
	}
	if summary.Handle != "evalres_handle" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestClientPreviewEvaluationResourceDoesNotReturnPayload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/resources/res_1/preview" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(EvaluationResourcePreview{
			Resource:         EvaluationResourceSummary{ID: "res_1", Handle: "evalres_handle", Name: "Prompt sample", Kind: EvaluationResourceKindSample},
			PayloadBytes:     13,
			PayloadLineCount: 2,
			PayloadSHA256:    "digest",
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	preview, err := client.PreviewEvaluationResource(context.Background(), "res_1")
	if err != nil {
		t.Fatalf("PreviewEvaluationResource: %v", err)
	}
	if preview.Resource.ID != "res_1" || preview.PayloadLineCount != 2 {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestClientSearchEvaluationTargetsUsesBFFTokenAndQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/targets" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		q := r.URL.Query()
		if q.Get("kind") != "llm" || q.Get("provider") != "openai" || q.Get("query") != "safety" || q.Get("include_inactive") != "true" || q.Get("limit") != "5" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "target_1", "name": "OpenAI target", "kind": "llm", "provider": "openai", "base_url": "https://api.example/v1", "model": "gpt-test", "credential_secret_set": true},
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.SearchEvaluationTargets(context.Background(), EvaluationTargetQuery{
		Kind:            EvaluationTargetKindLLM,
		Provider:        "openai",
		Query:           "safety",
		IncludeInactive: true,
		Limit:           5,
	})
	if err != nil {
		t.Fatalf("SearchEvaluationTargets: %v", err)
	}
	if len(items) != 1 || items[0].ID != "target_1" || !items[0].CredentialSecretSet {
		t.Fatalf("items = %#v", items)
	}
}

func TestClientCreateEvaluationTargetDoesNotReturnCredentialSecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/targets" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var in EvaluationTargetInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.CredentialSecret != "sk-bff-secret" {
			t.Fatalf("credential secret = %q", in.CredentialSecret)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(EvaluationTargetSummary{
			ID:                  "target_1",
			Name:                in.Name,
			Kind:                in.Kind,
			Provider:            in.Provider,
			BaseURL:             in.BaseURL,
			Model:               in.Model,
			CredentialSecretSet: true,
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	summary, err := client.SaveEvaluationTarget(context.Background(), EvaluationTargetInput{
		Name:             "OpenAI target",
		Kind:             EvaluationTargetKindLLM,
		Provider:         "openai",
		BaseURL:          "https://api.example/v1",
		Model:            "gpt-test",
		AuthType:         EvaluationTargetAuthTypeBearer,
		CredentialSecret: "sk-bff-secret",
	})
	if err != nil {
		t.Fatalf("SaveEvaluationTarget: %v", err)
	}
	if summary.ID != "target_1" || !summary.CredentialSecretSet {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestClientGetEvaluationTargetDoesNotReturnCredentialSecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/targets/target_1" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(EvaluationTargetSummary{
			ID:                  "target_1",
			Name:                "OpenAI target",
			Kind:                EvaluationTargetKindLLM,
			Provider:            "openai",
			BaseURL:             "https://api.example/v1",
			Model:               "gpt-test",
			CredentialSecretSet: true,
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	summary, err := client.GetEvaluationTarget(context.Background(), "target_1")
	if err != nil {
		t.Fatalf("GetEvaluationTarget: %v", err)
	}
	if summary.ID != "target_1" || !summary.CredentialSecretSet {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestClientReturnsTypedUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/evaluation/targets/missing" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"target missing"}`))
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.GetEvaluationTarget(context.Background(), "missing")
	if err == nil {
		t.Fatalf("expected error")
	}
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("expected UpstreamError, got %T: %v", err, err)
	}
	if upstreamErr.StatusCode != http.StatusNotFound ||
		upstreamErr.Method != http.MethodGet ||
		upstreamErr.Path != "/api/v1/evaluation/targets/missing" ||
		!strings.Contains(upstreamErr.Message, "target missing") {
		t.Fatalf("upstream error = %#v", upstreamErr)
	}
}

func TestClientProbeEvaluationTargetUsesBFFTokenAndDoesNotReturnCredentialSecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/targets/target_1/health-check" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "healthy",
			"message":     "target responded",
			"status_code": 204,
			"target": map[string]any{
				"id":                    "target_1",
				"name":                  "HTTP target",
				"kind":                  "http",
				"base_url":              "https://target.example",
				"auth_type":             "bearer",
				"credential_secret_set": true,
				"credential_secret":     "sk-secret",
				"health_status":         "healthy",
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.ProbeEvaluationTarget(context.Background(), "target_1")
	if err != nil {
		t.Fatalf("ProbeEvaluationTarget: %v", err)
	}
	if result.Status != EvaluationTargetHealthHealthy || result.Target.ID != "target_1" || !result.Target.CredentialSecretSet {
		t.Fatalf("result = %#v", result)
	}
	if got := mustJSON(t, result); strings.Contains(got, "sk-secret") || strings.Contains(got, `"credential_secret"`) {
		t.Fatalf("probe result leaked credential secret: %s", got)
	}
}

func TestClientStartEvaluationRunUsesBFFTokenAndDoesNotReturnPayloadOrSecret(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluation/runs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		var in EvaluationRunInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.InstanceID != "inst_1" || in.SessionID != "sess_1" || in.TargetID != "target_1" || len(in.ResourceHandles) != 1 || in.ResourceHandles[0] != "evalres_handle" {
			t.Fatalf("input = %#v", in)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": map[string]any{
				"id":         "run_1",
				"session_id": "sess_1",
				"status":     "succeeded",
				"metadata":   map[string]string{"report_id": "report_1"},
			},
			"evidence": map[string]any{
				"id":      "evidence_1",
				"run_id":  "run_1",
				"kind":    "result",
				"title":   "Target response",
				"summary": "status 200",
				"handle":  "evalevh_1",
				"content": "SECRET-PAYLOAD",
			},
			"report": map[string]any{
				"id":                "report_1",
				"run_id":            "run_1",
				"title":             "Evaluation report",
				"summary":           "ok",
				"safety_score":      100,
				"credential_secret": "SECRET-CREDENTIAL",
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result, err := client.StartEvaluationRun(context.Background(), EvaluationRunInput{
		InstanceID:      "inst_1",
		SessionID:       "sess_1",
		Goal:            "Run one check",
		TargetID:        "target_1",
		ResourceHandles: []string{"evalres_handle"},
	})
	if err != nil {
		t.Fatalf("StartEvaluationRun: %v", err)
	}
	if result.Run == nil || result.Run.ID != "run_1" || result.Evidence == nil || result.Evidence.Handle != "evalevh_1" || result.Report == nil || result.Report.ID != "report_1" {
		t.Fatalf("result = %#v", result)
	}
	if got := mustJSON(t, result); strings.Contains(got, "SECRET-PAYLOAD") || strings.Contains(got, "SECRET-CREDENTIAL") || strings.Contains(got, `"content"`) || strings.Contains(got, `"credential_secret"`) {
		t.Fatalf("run result leaked sensitive data: %s", got)
	}
}

func TestClientEvaluationJobsUseBFFTokenAndTypedSafeResult(t *testing.T) {
	requests := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		requests = append(requests, r.Method+" "+r.URL.String())
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/jobs":
			if r.URL.Query().Get("kind") != "evaluation.run" || r.URL.Query().Get("status") != "succeeded" || r.URL.Query().Get("limit") != "2" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{evaluationJobPayload("job_1", "succeeded")}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/jobs/job_1":
			_ = json.NewEncoder(w).Encode(evaluationJobPayload("job_1", "succeeded"))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/jobs/job_1/recovery":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"job_id":                   "job_1",
				"run_id":                   "run_1",
				"action":                   "manual_review",
				"strategy":                 "manual_review",
				"step":                     "calling_target",
				"completed_step_ids":       []string{"materializing_resources"},
				"can_retry":                false,
				"can_resume":               true,
				"resume_step":              "saving_results",
				"manual_review_required":   true,
				"reason":                   "target may have been called before interruption",
				"recovery_indexed":         true,
				"replacement_job_endpoint": "",
				"resume_job_endpoint":      "/api/v1/jobs/job_1/resume",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/jobs/job_1/cancel":
			_ = json.NewEncoder(w).Encode(evaluationJobPayload("job_1", "canceled"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/jobs/job_1/retry":
			_ = json.NewEncoder(w).Encode(evaluationJobPayload("job_retry", "pending"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/jobs/job_1/resume":
			_ = json.NewEncoder(w).Encode(evaluationJobPayload("job_resume", "pending"))
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.ListEvaluationJobs(context.Background(), EvaluationJobQuery{Status: EvaluationJobStatusSucceeded, Limit: 2})
	if err != nil {
		t.Fatalf("ListEvaluationJobs: %v", err)
	}
	got, err := client.GetEvaluationJob(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("GetEvaluationJob: %v", err)
	}
	recovery, err := client.GetEvaluationJobRecovery(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("GetEvaluationJobRecovery: %v", err)
	}
	canceled, err := client.CancelEvaluationJob(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("CancelEvaluationJob: %v", err)
	}
	retry, err := client.RetryEvaluationJob(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("RetryEvaluationJob: %v", err)
	}
	resume, err := client.ResumeEvaluationJob(context.Background(), "job_1")
	if err != nil {
		t.Fatalf("ResumeEvaluationJob: %v", err)
	}
	if len(items) != 1 || items[0].ID != "job_1" || items[0].Result == nil || items[0].Progress == nil || items[0].Progress.RunID != "run_1" || items[0].Progress.Step != "calling_target" || items[0].Progress.RetryableOnRestart || len(items[0].Progress.Steps) != 3 || items[0].Progress.Steps[1].Step != "calling_target" || items[0].Progress.Steps[1].Status != "running" || items[0].Progress.Steps[1].StartedAt == nil || items[0].Progress.RecoveryStrategy != "manual_review" || got.Result == nil || got.Progress == nil || got.Progress.RunID != "run_1" || got.Progress.StepStatus != "running" || got.Progress.RecoveryAction != "manual_review" || recovery.JobID != "job_1" || recovery.Step != "calling_target" || recovery.ResumeStep != "saving_results" || !recovery.CanResume || recovery.ResumeJobEndpoint != "/api/v1/jobs/job_1/resume" || !recovery.ManualReviewRequired || recovery.CanRetry || len(recovery.CompletedStepIDs) != 1 || canceled.Status != EvaluationJobStatusCanceled || retry.ID != "job_retry" || retry.Status != EvaluationJobStatusPending || resume.ID != "job_resume" || resume.Status != EvaluationJobStatusPending {
		t.Fatalf("items=%#v got=%#v recovery=%#v canceled=%#v retry=%#v resume=%#v", items, got, recovery, canceled, retry, resume)
	}
	if payload := mustJSON(t, map[string]any{"items": items, "got": got, "recovery": recovery, "canceled": canceled, "retry": retry, "resume": resume}); strings.Contains(payload, "SECRET-PAYLOAD") || strings.Contains(payload, "SECRET-CREDENTIAL") || strings.Contains(payload, `"content"`) || strings.Contains(payload, `"credential_secret"`) {
		t.Fatalf("job payload leaked sensitive result fields: %s", payload)
	}
	if len(requests) != 6 {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestClientConfirmRuntimePlanUsesEvaluationConfirmAlias(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/evaluation/sessions/sess_1/confirm" {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		var body struct {
			InstanceID string            `json:"instance_id"`
			Content    string            `json:"content"`
			Metadata   map[string]string `json:"metadata"`
			TestCount  int               `json:"test_count"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.InstanceID != "inst_1" || body.Content != "确认执行" || body.Metadata["evaluation_action"] != "confirm_plan" || body.TestCount != 9 {
			t.Fatalf("body = %#v", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "job_1",
			"kind":   "evaluation.run",
			"status": "pending",
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	job, err := client.ConfirmRuntimePlan(context.Background(), "inst_1", "sess_1", RuntimeMessageInput{
		Content:  "确认执行",
		Metadata: map[string]string{"evaluation_action": "confirm_plan", "test_count": "9"},
	})
	if err != nil {
		t.Fatalf("ConfirmRuntimePlan: %v", err)
	}
	if job.ID != "job_1" || job.Kind != EvaluationJobKindRun || job.Status != EvaluationJobStatusPending {
		t.Fatalf("job = %#v", job)
	}
}

func evaluationJobPayload(id, status string) map[string]any {
	return map[string]any{
		"id":     id,
		"kind":   "evaluation.run",
		"status": status,
		"progress": map[string]any{
			"phase":                "target_call",
			"step":                 "calling_target",
			"step_status":          "running",
			"retryable_on_restart": false,
			"steps": []map[string]any{
				{"step": "materializing_resources", "status": "succeeded", "retryable_on_restart": true},
				{"step": "calling_target", "status": "running", "retryable_on_restart": false, "started_at": "2026-05-13T01:00:00Z"},
				{"step": "saving_results", "status": "pending", "retryable_on_restart": false},
			},
			"restart_interrupted": true,
			"recovery_strategy":   "manual_review",
			"recovery_action":     "manual_review",
			"run_id":              "run_1",
			"instance_id":         "inst_1",
			"session_id":          "sess_1",
			"status_text":         "calling evaluation target",
		},
		"result": map[string]any{
			"run": map[string]any{
				"id":         "run_1",
				"session_id": "sess_1",
				"status":     "succeeded",
			},
			"evidence": map[string]any{
				"id":      "evidence_1",
				"run_id":  "run_1",
				"kind":    "result",
				"summary": "status 200",
				"handle":  "evalevh_1",
				"content": "SECRET-PAYLOAD",
			},
			"report": map[string]any{
				"id":                "report_1",
				"run_id":            "run_1",
				"title":             "Evaluation report",
				"summary":           "ok",
				"credential_secret": "SECRET-CREDENTIAL",
			},
		},
	}
}

func TestClientGetAndExportEvaluationReportUsesBFFToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/evaluation/reports/report_1":
			_ = json.NewEncoder(w).Encode(EvaluationReport{
				ID:          "report_1",
				Title:       "Safety report",
				Summary:     "Summary text",
				RiskLevel:   "low",
				SafetyScore: ptrFloat64(88),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/evaluation/reports/report_1/export":
			if r.URL.Query().Get("format") != "markdown" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="report_1.md"`)
			_, _ = w.Write([]byte("# Safety report\n"))
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	report, err := client.GetEvaluationReport(context.Background(), "report_1")
	if err != nil {
		t.Fatalf("GetEvaluationReport: %v", err)
	}
	if report.ID != "report_1" || report.SafetyScore == nil || *report.SafetyScore != 88 {
		t.Fatalf("report = %#v", report)
	}
	exported, err := client.ExportEvaluationReport(context.Background(), "report_1", "markdown")
	if err != nil {
		t.Fatalf("ExportEvaluationReport: %v", err)
	}
	if exported.Filename != "report_1.md" || exported.ContentType != "text/markdown; charset=utf-8" || string(exported.Content) != "# Safety report\n" {
		t.Fatalf("exported = %#v", exported)
	}
}

func TestClientListAndGetEvaluationEvidenceDoesNotReturnContent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/evaluation/evidence":
			q := r.URL.Query()
			if q.Get("instance_id") != "inst_1" || q.Get("session_id") != "session_1" || q.Get("run_id") != "run_1" || q.Get("kind") != "tool_call" || q.Get("limit") != "3" {
				t.Fatalf("unexpected query: %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{
						"id":          "evidence_1",
						"instance_id": "inst_1",
						"session_id":  "session_1",
						"run_id":      "run_1",
						"kind":        "tool_call",
						"title":       "Tool call",
						"summary":     "status 200",
						"handle":      "evalevh_1",
						"content":     "SECRET-EVIDENCE-CONTENT",
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/evaluation/evidence/evidence_1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":          "evidence_1",
				"instance_id": "inst_1",
				"session_id":  "session_1",
				"run_id":      "run_1",
				"kind":        "tool_call",
				"title":       "Tool call",
				"summary":     "status 200",
				"handle":      "evalevh_1",
				"content":     "SECRET-EVIDENCE-CONTENT",
			})
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.ListEvaluationEvidence(context.Background(), EvaluationEvidenceQuery{
		InstanceID: "inst_1",
		SessionID:  "session_1",
		RunID:      "run_1",
		Kind:       EvaluationEvidenceKindToolCall,
		Limit:      3,
	})
	if err != nil {
		t.Fatalf("ListEvaluationEvidence: %v", err)
	}
	if len(items) != 1 || items[0].Handle != "evalevh_1" {
		t.Fatalf("items = %#v", items)
	}
	if got := mustJSON(t, items[0]); strings.Contains(got, "SECRET-EVIDENCE-CONTENT") || strings.Contains(got, `"content"`) {
		t.Fatalf("evidence summary leaked content: %s", got)
	}
	item, err := client.GetEvaluationEvidence(context.Background(), "evidence_1")
	if err != nil {
		t.Fatalf("GetEvaluationEvidence: %v", err)
	}
	if item.ID != "evidence_1" || item.Handle != "evalevh_1" {
		t.Fatalf("item = %#v", item)
	}
	if got := mustJSON(t, item); strings.Contains(got, "SECRET-EVIDENCE-CONTENT") || strings.Contains(got, `"content"`) {
		t.Fatalf("evidence detail leaked content: %s", got)
	}
}

func TestClientListSkillsReturnsSafeSummaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/skills" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("limit") != "5" {
			t.Fatalf("raw query = %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"name":         "classical-rewriter",
					"description":  "Rewrite prompts",
					"triggers":     []string{"classical chinese"},
					"status":       "active",
					"source":       "zip_import",
					"type":         "executable",
					"mode":         "sequential",
					"required_env": []string{"OPENAI_API_KEY"},
					"skill_dir":    "C:/secret/path",
					"content":      "SECRET-CONTENT",
					"steps": []map[string]any{
						{"action": "run", "params": map[string]any{"command": "echo SECRET-COMMAND"}},
					},
				},
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.ListSkills(context.Background(), 5)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(items) != 1 || items[0].Name != "classical-rewriter" || items[0].RequiredEnv[0] != "OPENAI_API_KEY" {
		t.Fatalf("items = %#v", items)
	}
	if got := mustJSON(t, items[0]); strings.Contains(got, "SECRET-CONTENT") || strings.Contains(got, "SECRET-COMMAND") || strings.Contains(got, "skill_dir") {
		t.Fatalf("skill summary leaked runtime internals: %s", got)
	}
}

func TestClientSearchSkillsUsesMaclawSearchWithoutPlatformClassification(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/skills/search" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var in SkillSearchInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.Query != "rewrite" || in.TopN != 3 || len(in.Sources) != 1 || in.Sources[0] != "github" {
			t.Fatalf("input = %#v", in)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"source": "github", "name": "rewrite-skill", "description": "Generic maclaw skill", "definition_type": "skill.yaml"},
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.SearchSkills(context.Background(), SkillSearchInput{Query: "rewrite", Sources: []string{"github"}, TopN: 3})
	if err != nil {
		t.Fatalf("SearchSkills: %v", err)
	}
	if len(items) != 1 || items[0].Name != "rewrite-skill" || items[0].Source != "github" {
		t.Fatalf("items = %#v", items)
	}
}

func TestClientInstallSkillReturnsSafeSummaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/skills/install" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var in SkillInstallInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.Source != "skillhub" || in.SkillHubURL != "https://hub.internal" || in.SkillID != "ccbos-classical-chinese-skill" || !in.Overwrite {
			t.Fatalf("install input = %#v", in)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"name": "ccbos-classical-chinese-skill", "description": "Classical Chinese jailbreak", "status": "active", "source": "skillhub", "skill_dir": "C:/secret", "content": "SECRET", "steps": []map[string]any{{"action": "run"}}},
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.InstallSkill(context.Background(), SkillInstallInput{
		Source:      "skillhub",
		SkillHubURL: "https://hub.internal",
		SkillID:     "ccbos-classical-chinese-skill",
		Overwrite:   true,
	})
	if err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}
	if len(items) != 1 || items[0].Name != "ccbos-classical-chinese-skill" || items[0].Source != "skillhub" {
		t.Fatalf("items = %#v", items)
	}
	if got := mustJSON(t, items[0]); strings.Contains(got, "SECRET") || strings.Contains(got, "skill_dir") || strings.Contains(got, "content") || strings.Contains(got, "steps") {
		t.Fatalf("install summary leaked runtime internals: %s", got)
	}
}

func TestClientCreateRuntimeSessionUsesConfiguredInstance(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/evaluation/sessions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		var in struct {
			InstanceID string `json:"instance_id"`
			Title      string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.InstanceID != "inst_1" || in.Title != "Enterprise assessment" {
			t.Fatalf("input = %#v", in)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(RuntimeSession{ID: "sess_1", InstanceID: "inst_1", Title: in.Title})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	session, err := client.CreateRuntimeSession(context.Background(), "inst_1", RuntimeSessionInput{Title: "Enterprise assessment"})
	if err != nil {
		t.Fatalf("CreateRuntimeSession: %v", err)
	}
	if session.ID != "sess_1" || session.InstanceID != "inst_1" {
		t.Fatalf("session = %#v", session)
	}
}

func TestClientListGetAndDeleteRuntimeSessions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/evaluation/sessions":
			if r.URL.Query().Get("instance_id") != "inst_1" || r.URL.Query().Get("limit") != "10" || r.URL.Query().Get("include_archived") != "true" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"id": "sess_1", "instance_id": "inst_1", "title": "Enterprise assessment"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/evaluation/sessions/sess_1":
			if r.URL.Query().Get("instance_id") != "inst_1" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(RuntimeSession{ID: "sess_1", InstanceID: "inst_1", Title: "Enterprise assessment"})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/evaluation/sessions/sess_1":
			if r.URL.Query().Get("instance_id") != "inst_1" {
				t.Fatalf("query = %s", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.ListRuntimeSessions(context.Background(), "inst_1", RuntimeSessionQuery{Limit: 10, IncludeArchived: true})
	if err != nil {
		t.Fatalf("ListRuntimeSessions: %v", err)
	}
	if len(items) != 1 || items[0].ID != "sess_1" {
		t.Fatalf("items = %#v", items)
	}
	session, err := client.GetRuntimeSession(context.Background(), "inst_1", "sess_1")
	if err != nil {
		t.Fatalf("GetRuntimeSession: %v", err)
	}
	if session.ID != "sess_1" {
		t.Fatalf("session = %#v", session)
	}
	if err := client.DeleteRuntimeSession(context.Background(), "inst_1", "sess_1"); err != nil {
		t.Fatalf("DeleteRuntimeSession: %v", err)
	}
}

func TestClientListRuntimeMessages(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/evaluation/sessions/sess_1/messages" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("instance_id") != "inst_1" || r.URL.Query().Get("limit") != "50" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": "msg_1", "session_id": "sess_1", "role": "assistant", "content": "plan"},
			},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	items, err := client.ListRuntimeMessages(context.Background(), "inst_1", "sess_1", RuntimeMessageQuery{Limit: 50})
	if err != nil {
		t.Fatalf("ListRuntimeMessages: %v", err)
	}
	if len(items) != 1 || items[0].ID != "msg_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestClientPostRuntimeMessageReturnsRunAndMessage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/evaluation/sessions/sess_1/messages" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var in struct {
			InstanceID string `json:"instance_id"`
			Content    string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.InstanceID != "inst_1" || in.Content != "test this target" {
			t.Fatalf("input = %#v", in)
		}
		_ = json.NewEncoder(w).Encode(RuntimeMessageResponse{
			Run:     &RuntimeRun{ID: "run_1", SessionID: "sess_1", Status: "running"},
			Message: &RuntimeMessage{ID: "msg_1", Role: "assistant", Content: "ok"},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	resp, err := client.PostRuntimeMessage(context.Background(), "inst_1", "sess_1", RuntimeMessageInput{Content: "test this target"})
	if err != nil {
		t.Fatalf("PostRuntimeMessage: %v", err)
	}
	if resp.Run == nil || resp.Run.ID != "run_1" || resp.Message == nil || resp.Message.ID != "msg_1" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestClientPostRuntimeMessageSendsCapabilityContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/evaluation/sessions/sess_1/messages" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var in struct {
			InstanceID        string                    `json:"instance_id"`
			Content           string                    `json:"content"`
			CapabilityContext *RuntimeCapabilityContext `json:"capability_context"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if in.InstanceID != "inst_1" || in.CapabilityContext == nil || in.CapabilityContext.AgentProfile != "redteam_evaluation_v1" {
			t.Fatalf("input = %#v", in)
		}
		if len(in.CapabilityContext.Cards) != 1 || in.CapabilityContext.Cards[0].SourceRef != "ccbos-classical-chinese-skill" {
			t.Fatalf("capability context = %#v", in.CapabilityContext)
		}
		_ = json.NewEncoder(w).Encode(RuntimeMessageResponse{
			Run:     &RuntimeRun{ID: "run_1", SessionID: "sess_1", Status: "running"},
			Message: &RuntimeMessage{ID: "msg_1", Role: "assistant", Content: "ok"},
		})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.PostRuntimeMessage(context.Background(), "inst_1", "sess_1", RuntimeMessageInput{
		Content: "用文言文越狱测试目标LLM",
		CapabilityContext: &RuntimeCapabilityContext{
			AgentProfile: "redteam_evaluation_v1",
			Cards: []CapabilityCard{{
				SourceType: CapabilitySourceSkill,
				SourceRef:  "ccbos-classical-chinese-skill",
				Name:       "CCBOS",
				Enabled:    true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("PostRuntimeMessage: %v", err)
	}
}

func TestClientRefreshRuntimeInstanceReadiness(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/instances/inst_1/refresh-readiness" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "inst_1", "ready": true})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.RefreshRuntimeInstanceReadiness(context.Background(), "inst_1"); err != nil {
		t.Fatalf("RefreshRuntimeInstanceReadiness: %v", err)
	}
}

func TestClientCancelRuntimeRun(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/instances/inst_1/runs/run_1/cancel" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(RuntimeRun{ID: "run_1", Status: "cancelled"})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	run, err := client.CancelRuntimeRun(context.Background(), "inst_1", "run_1")
	if err != nil {
		t.Fatalf("CancelRuntimeRun: %v", err)
	}
	if run.ID != "run_1" || run.Status != "cancelled" {
		t.Fatalf("run = %#v", run)
	}
}

func TestClientCancelEvaluationRunUsesDedicatedEvaluationAlias(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/evaluation/runs/run_1/cancel" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(RuntimeRun{ID: "run_1", Status: "cancelled"})
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	run, err := client.CancelEvaluationRun(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("CancelEvaluationRun: %v", err)
	}
	if run.ID != "run_1" || run.Status != "cancelled" {
		t.Fatalf("run = %#v", run)
	}
}

func TestClientStreamRuntimeRunEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/instances/inst_1/runs/run_1/events" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("accept = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: report\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"report\"}\n\n"))
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	stream, err := client.StreamRuntimeRunEvents(context.Background(), "inst_1", "run_1")
	if err != nil {
		t.Fatalf("StreamRuntimeRunEvents: %v", err)
	}
	defer stream.Body.Close()
	if stream.ContentType != "text/event-stream" {
		t.Fatalf("content type = %q", stream.ContentType)
	}
	data, err := io.ReadAll(stream.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !strings.Contains(string(data), "event: report") {
		t.Fatalf("stream = %q", string(data))
	}
}

func TestClientStreamEvaluationRunEventsUsesDedicatedEvaluationAlias(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/evaluation/runs/run_1/events" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("accept = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: report\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"report\"}\n\n"))
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	stream, err := client.StreamEvaluationRunEvents(context.Background(), "run_1")
	if err != nil {
		t.Fatalf("StreamEvaluationRunEvents: %v", err)
	}
	defer stream.Body.Close()
	if stream.ContentType != "text/event-stream" {
		t.Fatalf("content type = %q", stream.ContentType)
	}
	data, err := io.ReadAll(stream.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !strings.Contains(string(data), "event: report") {
		t.Fatalf("stream = %q", string(data))
	}
}

func TestNewClientRejectsMissingConfig(t *testing.T) {
	if _, err := NewClient(Config{}); err == nil {
		t.Fatalf("expected missing config error")
	}
}

func TestClientProxiesRuntimeConfigWithoutLeakingSecrets(t *testing.T) {
	var updateBody RuntimeAppConfig
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer maclaw-token" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config":
			_ = json.NewEncoder(w).Encode(RuntimeUserConfig{AppConfig: RuntimeAppConfig{
				MaclawLLMProviders: []RuntimeLLMProvider{{
					Name:  "openai-prod",
					URL:   "https://api.example/v1",
					Key:   "******",
					Model: "gpt-test",
				}},
				MaclawLLMCurrentProvider: "openai-prod",
			}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/config":
			if err := json.NewDecoder(r.Body).Decode(&updateBody); err != nil {
				t.Fatalf("decode update: %v", err)
			}
			_ = json.NewEncoder(w).Encode(RuntimeUserConfig{AppConfig: RuntimeAppConfig{
				MaclawLLMProviders: []RuntimeLLMProvider{{
					Name:  updateBody.MaclawLLMProviders[0].Name,
					URL:   updateBody.MaclawLLMProviders[0].URL,
					Key:   "******",
					Model: updateBody.MaclawLLMProviders[0].Model,
				}},
				MaclawLLMCurrentProvider: updateBody.MaclawLLMCurrentProvider,
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/config/validate":
			_ = json.NewEncoder(w).Encode(RuntimeConfigValidation{Valid: true})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/config/test":
			_ = json.NewEncoder(w).Encode(RuntimeConfigTestResult{Success: true, Message: "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	client, err := NewClient(Config{BaseURL: upstream.URL, APIToken: "maclaw-token", TimeoutSeconds: 3})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	cfg, err := client.GetRuntimeConfig(context.Background())
	if err != nil {
		t.Fatalf("GetRuntimeConfig: %v", err)
	}
	if cfg.AppConfig.MaclawLLMProviders[0].Key != "******" {
		t.Fatalf("GET key = %q, want masked", cfg.AppConfig.MaclawLLMProviders[0].Key)
	}

	updated, err := client.UpdateRuntimeConfig(context.Background(), RuntimeAppConfig{
		MaclawLLMProviders: []RuntimeLLMProvider{{
			Name:  "openai-prod",
			URL:   "https://api.example/v1",
			Key:   "sk-secret",
			Model: "gpt-test",
		}},
		MaclawLLMCurrentProvider: "openai-prod",
	})
	if err != nil {
		t.Fatalf("UpdateRuntimeConfig: %v", err)
	}
	if updateBody.MaclawLLMProviders[0].Key != "sk-secret" {
		t.Fatalf("upstream key = %q, want raw write-only key", updateBody.MaclawLLMProviders[0].Key)
	}
	if updated.AppConfig.MaclawLLMProviders[0].Key != "******" {
		t.Fatalf("updated key = %q, want masked", updated.AppConfig.MaclawLLMProviders[0].Key)
	}
	validation, err := client.ValidateRuntimeConfig(context.Background(), nil)
	if err != nil || !validation.Valid {
		t.Fatalf("ValidateRuntimeConfig = %#v, %v", validation, err)
	}
	testResult, err := client.TestRuntimeConfig(context.Background(), nil)
	if err != nil || !testResult.Success {
		t.Fatalf("TestRuntimeConfig = %#v, %v", testResult, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(data)
}

func ptrFloat64(v float64) *float64 {
	return &v
}
