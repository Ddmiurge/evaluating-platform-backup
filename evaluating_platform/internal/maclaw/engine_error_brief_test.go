package maclaw

// engine_error_brief_test.go — P2-5 · the platform's redaction of a engine-supplied
// diagnosis, and the wiring that stores it.
//
// # What these assertions protect
//
// `engine_runs.error_brief` is persisted, tenant-facing data and the only record of
// why a run failed. Two failure modes matter and they pull in opposite directions:
//
//   - **under-redaction** puts a credential, a provider endpoint or a payload
//     fragment into a database, where it outlives the run;
//   - **over-redaction** removes the cause, which is the same as having no brief —
//     and is the easier mistake to make, because a rule that is merely too broad
//     still looks careful.
//
// Both directions are asserted below. The over-redaction cases are not hypothetical:
// the first version of this rule redacted the error code out of its own message.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactEngineErrorBriefRemovesCredentialsAndTopology(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		mustNot []string
		must    []string
	}{
		{
			name:    "vendor key",
			input:   "provider rejected key sk-abc123DEF456ghi789 at startup",
			mustNot: []string{"sk-abc123DEF456ghi789"},
			must:    []string{"[redacted]", "provider rejected key"},
		},
		{
			name:    "credential assignment",
			input:   "request failed (api_key: super-secret-value)",
			mustNot: []string{"super-secret-value"},
			must:    []string{"request failed"},
		},
		{
			name:    "authorization header",
			input:   "Authorization: Bearer super-secret-value was refused",
			mustNot: []string{"super-secret-value"},
			must:    []string{"was refused"},
		},
		{
			name:    "url host and port",
			input:   "POST https://llm.internal.corp:8443/v1/chat/completions failed with 401",
			mustNot: []string{"llm.internal.corp", "8443"},
			// The path is kept: it names the step that failed.
			must: []string{"https://[host]/v1/chat/completions"},
		},
		{
			name:    "filesystem path",
			input:   "cannot read /home/node/.promptfoo/runs/run-1/redteam-config.yaml",
			mustNot: []string{"/home/node", ".promptfoo"},
			must:    []string{"redteam-config.yaml"},
		},
		{
			name:    "url path is not treated as a filesystem path",
			input:   "GET https://target.internal/v1/models.json timed out",
			mustNot: []string{"target.internal"},
			must:    []string{"https://[host]/v1/models.json"},
		},
		{
			name:    "long hex digest",
			input:   "digest mismatch: a3f9c1d07b2e4856fa90cd13be7450f2",
			mustNot: []string{"a3f9c1d07b2e4856fa90cd13be7450f2"},
			must:    []string{"digest mismatch"},
		},
		{
			name:    "mixed-case token",
			input:   "signature mismatch: QWxhZGRpbjpvcGVuIHNlc2FtZQabcdefgh",
			mustNot: []string{"QWxhZGRpbjpvcGVuIHNlc2FtZQabcdefgh"},
			must:    []string{"signature mismatch"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := redactEngineErrorBrief(tc.input)
			for _, banned := range tc.mustNot {
				if strings.Contains(out, banned) {
					t.Errorf("redacted brief still contains %q: %q", banned, out)
				}
			}
			for _, want := range tc.must {
				if !strings.Contains(out, want) {
					t.Errorf("redacted brief lost %q: %q", want, out)
				}
			}
		})
	}
}

func TestRedactEngineErrorBriefKeepsTheDiagnosis(t *testing.T) {
	// The over-redaction guard. Each of these is something a reader needs, and each
	// was endangered by a rule that was merely too broad.
	for _, input := range []string{
		// An error code is 41 characters of the identifier alphabet.
		"[agent_session_requires_multiturn_strategy] 请求体模板里用了 {{conversation_id}}，但本次运行没有选择多轮策略",
		// The promptfoo message the U7 acceptance run could not otherwise see.
		"jailbreak:meta strategy requires remote generation",
		// Splitting the shared code exists so this kind of sentence survives.
		"promptfoo native generation failed: Invalid plugin id \"prompt-injection\"",
	} {
		out := redactEngineErrorBrief(input)
		if out != input {
			t.Errorf("a diagnosis was altered:\n in:  %q\n out: %q", input, out)
		}
	}
}

func TestRedactEngineErrorBriefReplacesContentShapedMessages(t *testing.T) {
	// Wholesale replacement, not filtering: a partially redacted payload reads as
	// trustworthy, which is worse than an obviously missing one.
	for _, input := range []string{
		`raw payload: {"prompt": "..."}`,
		"raw prompt was rejected",
		"target response body exceeded the limit",
		"evidence content could not be parsed",
	} {
		if got := redactEngineErrorBrief(input); got != "[redacted: message carried content]" {
			t.Errorf("content-shaped message not replaced wholesale: %q -> %q", input, got)
		}
	}
}

func TestRedactEngineErrorBriefShape(t *testing.T) {
	if got := redactEngineErrorBrief("   "); got != "" {
		t.Errorf("blank input = %q, want an empty string (not a literal)", got)
	}

	// Budgeted in RUNES: the messages are largely Chinese, and a byte budget would
	// truncate at roughly a third of the intended length.
	long := strings.Repeat("错", maxEngineErrorBriefRunes*2)
	out := redactEngineErrorBrief(long)
	if got := len([]rune(out)); got > maxEngineErrorBriefRunes+3 {
		t.Errorf("brief is %d runes, want <= %d (+ellipsis)", got, maxEngineErrorBriefRunes)
	}
	if !strings.HasSuffix(out, "...") {
		t.Errorf("truncated brief carries no ellipsis: %q", out)
	}
}

// TestApplyStatusWritesErrorBriefOnlyOnTerminalFailure is the wiring assertion
// (R-4-a): remove the assignment in ApplyStatus and it goes red, which is what the
// column's missing writer looked like for two iterations.
func TestApplyStatusWritesErrorBriefOnlyOnTerminalFailure(t *testing.T) {
	service := NewEngineRunService(nil)

	t.Run("failed run stores the redacted brief", func(t *testing.T) {
		record := &EngineRunRecord{}
		service.ApplyStatus(record, &EngineRunStatus{
			Phase:        EnginePhaseFailed,
			ErrorCode:    "native_generation_promptfoo_failed",
			ErrorMessage: "promptfoo native generation failed: POST https://llm.internal:8443/v1 rejected key sk-abc123DEF456ghi789",
		})
		if record.ErrorBrief == "" {
			t.Fatal("a terminal failure recorded no brief")
		}
		if strings.Contains(record.ErrorBrief, "llm.internal") || strings.Contains(record.ErrorBrief, "sk-abc123DEF456ghi789") {
			t.Errorf("the stored brief was not redacted: %q", record.ErrorBrief)
		}
		if !strings.Contains(record.ErrorBrief, "promptfoo native generation failed") {
			t.Errorf("the stored brief lost the cause: %q", record.ErrorBrief)
		}
	})

	t.Run("running status records nothing", func(t *testing.T) {
		// A mid-run message describes a condition the run may recover from, and
		// would later be read as the cause of a failure that never happened.
		record := &EngineRunRecord{}
		service.ApplyStatus(record, &EngineRunStatus{
			Phase:        EnginePhaseExecutingProbes,
			ErrorMessage: "probe 3 retried",
		})
		if record.ErrorBrief != "" {
			t.Errorf("a non-terminal status wrote a brief: %q", record.ErrorBrief)
		}
	})

	t.Run("succeeded run records nothing", func(t *testing.T) {
		record := &EngineRunRecord{}
		service.ApplyStatus(record, &EngineRunStatus{Phase: EnginePhaseSucceeded})
		if record.ErrorBrief != "" {
			t.Errorf("a success wrote a brief: %q", record.ErrorBrief)
		}
	})

	t.Run("engine status parses the field", func(t *testing.T) {
		// The wire contract: the field the engine sends must land on this struct,
		// and it must be omitted when absent so older engines stay compatible.
		var status EngineRunStatus
		if err := json.Unmarshal([]byte(`{"run_id":"r","phase":"failed","error_code":"x","error_message":"boom"}`), &status); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if status.ErrorMessage != "boom" {
			t.Errorf("ErrorMessage = %q, want %q", status.ErrorMessage, "boom")
		}
		encoded, err := json.Marshal(EngineRunStatus{RunID: "r", Phase: EnginePhaseSucceeded})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(encoded), "error_message") {
			t.Errorf("an empty ErrorMessage must be omitted, got %s", encoded)
		}
	})
}
