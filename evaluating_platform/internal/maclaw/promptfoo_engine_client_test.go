package maclaw

// promptfoo_engine_client_test.go — verifies the anti-corruption client
// against a fake engine HTTP server (mirrors src/routes/runs.ts behavior).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stringsReader(s string) io.Reader { return strings.NewReader(s) }

const testToken = "test-bearer-token"

func newFakeEngine(t *testing.T, handler http.Handler) *PromptfooEngineClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewPromptfooEngineClient(srv.URL, testToken, 5)
}

func TestEngineClientDisabled(t *testing.T) {
	client := NewPromptfooEngineClient("", "x", 5)
	if client.Enabled() {
		t.Fatal("client with empty baseURL should be disabled")
	}
	if err := client.Health(context.Background()); err != ErrEngineUnavailable {
		t.Fatalf("health: want ErrEngineUnavailable, got %v", err)
	}
	if _, err := client.GetRun(context.Background(), "r1"); err != ErrEngineUnavailable {
		t.Fatalf("get: want ErrEngineUnavailable, got %v", err)
	}
	if err := client.CreateRun(context.Background(), &EngineRunRequest{RunID: "r1"}); err != ErrEngineUnavailable {
		t.Fatalf("create: want ErrEngineUnavailable, got %v", err)
	}
	if err := client.CancelRun(context.Background(), "r1"); err != ErrEngineUnavailable {
		t.Fatalf("cancel: want ErrEngineUnavailable, got %v", err)
	}
	if err := client.StreamRunEvents(context.Background(), "r1", nil); err != ErrEngineUnavailable {
		t.Fatalf("stream: want ErrEngineUnavailable, got %v", err)
	}
}

func TestEngineClientHealth(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		client := newFakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "" {
				t.Error("health should not send auth")
			}
			w.WriteHeader(http.StatusOK)
		}))
		if err := client.Health(context.Background()); err != nil {
			t.Fatalf("health: %v", err)
		}
	})

	t.Run("service missing", func(t *testing.T) {
		client := NewPromptfooEngineClient("http://127.0.0.1:1", "x", 1)
		if err := client.Health(context.Background()); err != ErrEngineUnavailable {
			t.Fatalf("want ErrEngineUnavailable, got %v", err)
		}
	})

	t.Run("server error", func(t *testing.T) {
		client := newFakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		if err := client.Health(context.Background()); err != ErrEngineUnavailable {
			t.Fatalf("want ErrEngineUnavailable, got %v", err)
		}
	})
}

func TestEngineClientCreateRun(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		want     error
		generic  bool // expect non-sentinel error
	}{
		{name: "accepted", status: http.StatusAccepted, want: nil},
		{name: "queue full", status: http.StatusConflict, body: `{"error":"queue_full"}`, want: ErrEngineQueueFull},
		{name: "run exists", status: http.StatusConflict, body: `{"error":"run_already_exists"}`, want: ErrEngineRunExists},
		{name: "unauthorized", status: http.StatusUnauthorized, want: ErrEngineUnauthorized},
		{name: "server error", status: http.StatusInternalServerError, generic: true},
		{name: "bad request", status: http.StatusBadRequest, generic: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/runs" {
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
					t.Errorf("missing bearer, got %q", got)
				}
				var body EngineRunRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("bad body: %v", err)
				} else if body.RunID != "run-1" {
					t.Errorf("unexpected run_id %q", body.RunID)
				}
				if tc.body != "" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				w.WriteHeader(tc.status)
			}))
			err := client.CreateRun(context.Background(), &EngineRunRequest{RunID: "run-1"})
			switch {
			case tc.generic:
				if err == nil || err == ErrEngineUnavailable {
					t.Fatalf("want generic error, got %v", err)
				}
			default:
				if err != tc.want {
					t.Fatalf("want %v, got %v", tc.want, err)
				}
			}
		})
	}
}

func TestEngineClientGetRun(t *testing.T) {
	client := newFakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs/run-1":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(EngineRunStatus{
				RunID:         "run-1",
				Phase:         EnginePhaseExecutingProbes,
				PlannedCount:  10,
				ExecutedCount: 4,
			})
		case "/runs/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/runs/with-result":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(EngineRunStatus{
				RunID: "with-result",
				Phase: EnginePhaseSucceeded,
				Result: &EngineSafeResult{
					Totals: EngineRunTotals{Probes: 5, AttackSuccess: 2, PassRate: 0.6},
				},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))

	t.Run("ok", func(t *testing.T) {
		st, err := client.GetRun(context.Background(), "run-1")
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if st.Phase != EnginePhaseExecutingProbes || st.PlannedCount != 10 || st.ExecutedCount != 4 {
			t.Fatalf("unexpected status %+v", st)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, err := client.GetRun(context.Background(), "missing"); err != ErrEngineNotFound {
			t.Fatalf("want ErrEngineNotFound, got %v", err)
		}
	})

	t.Run("with result", func(t *testing.T) {
		st, err := client.GetRun(context.Background(), "with-result")
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if st.Result == nil || st.Result.Totals.Probes != 5 || st.Result.Totals.AttackSuccess != 2 {
			t.Fatalf("unexpected result %+v", st.Result)
		}
	})
}

func TestEngineClientCancelRun(t *testing.T) {
	client := newFakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/runs/run-1/cancel":
			w.WriteHeader(http.StatusOK)
		case "/runs/gone/cancel":
			w.WriteHeader(http.StatusNotFound)
		case "/runs/terminal/cancel":
			// run_not_cancelable — idempotent success
			w.WriteHeader(http.StatusConflict)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))

	if err := client.CancelRun(context.Background(), "run-1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := client.CancelRun(context.Background(), "gone"); err != ErrEngineNotFound {
		t.Fatalf("want ErrEngineNotFound, got %v", err)
	}
	if err := client.CancelRun(context.Background(), "terminal"); err != nil {
		t.Fatalf("409 should map to success, got %v", err)
	}
}

func TestEngineClientStreamRunEvents(t *testing.T) {
	client := newFakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runs/run-1/events" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("missing SSE accept header, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// event: progress header line (parser only reads data: lines)
		_, _ = w.Write([]byte("event: progress\n"))
		_, _ = w.Write([]byte(`data: {"run_id":"run-1","phase":"executing_probes","planned_count":5,"executed_count":1}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"run_id":"run-1","phase":"succeeded","planned_count":5,"executed_count":5}` + "\n\n"))
		// flush pending event without trailing blank line then close
		_, _ = w.Write([]byte(`data: {"run_id":"run-1","phase":"failed"}`))
	}))
	var phases []EngineRunPhase
	err := client.StreamRunEvents(context.Background(), "run-1", func(ev *EngineRunStatus) {
		phases = append(phases, ev.Phase)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(phases) != 3 || phases[0] != EnginePhaseExecutingProbes || phases[1] != EnginePhaseSucceeded || phases[2] != EnginePhaseFailed {
		t.Fatalf("unexpected phases %v", phases)
	}
}

func TestEngineClientStreamRunEventsNotFound(t *testing.T) {
	client := newFakeEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	err := client.StreamRunEvents(context.Background(), "nope", nil)
	if err != ErrEngineNotFound {
		t.Fatalf("want ErrEngineNotFound, got %v", err)
	}
}

func TestParseSSEStream(t *testing.T) {
	t.Run("consecutive data lines merge into one event", func(t *testing.T) {
		body := "data: {\"a\":1}\ndata: {\"a\":2}\n\n"
		var payloads []string
		if err := parseSSEStream(stringsReader(body), func(d []byte) { payloads = append(payloads, string(d)) }); err != nil {
			t.Fatalf("parse: %v", err)
		}
		// The engine emits single-line JSON events only; consecutive data
		// lines concatenate into one payload (simple parser, spec-adequate).
		if len(payloads) != 1 || payloads[0] != `{"a":1}{"a":2}` {
			t.Fatalf("want 1 merged event, got %d: %v", len(payloads), payloads)
		}
	})
	t.Run("ignore non-data lines", func(t *testing.T) {
		body := ": comment\nretry: 5000\ndata: {\"x\":9}\n\n"
		var payloads []string
		if err := parseSSEStream(stringsReader(body), func(d []byte) { payloads = append(payloads, string(d)) }); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(payloads) != 1 || payloads[0] != `{"x":9}` {
			t.Fatalf("unexpected payloads %v", payloads)
		}
	})
}

func TestEscapePathSegment(t *testing.T) {
	cases := map[string]string{
		"run-123_ABC": "run-123_ABC",
		"../../etc/passwd": "etcpasswd",
		"run with spaces": "runwithspaces",
		"":                "",
	}
	for in, want := range cases {
		if got := escapePathSegment(in); got != want {
			t.Errorf("escapePathSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewPromptfooEngineClientDefaults(t *testing.T) {
	c := NewPromptfooEngineClient("http://x:1/", "", 0)
	if c.baseURL != "http://x:1" {
		t.Fatalf("baseURL not trimmed: %q", c.baseURL)
	}
	if c.httpClient.Timeout <= 0 {
		t.Fatalf("default timeout not applied: %v", c.httpClient.Timeout)
	}
}