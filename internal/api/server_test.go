package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/backend"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/controller"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/queue"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/scheduler"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

func TestSessionResourceLifecycle(t *testing.T) {
	handler := testHandler(t)
	create := doJSON(t, handler, http.MethodPost, "/v1/sessions", map[string]any{
		"session_id":    "demo",
		"min_resource":  map[string]any{"cpu_milli": 1000, "memory_bytes": 1073741824},
		"max_resource":  map[string]any{"cpu_milli": 8000, "memory_bytes": 8589934592},
		"task_priority": 1,
	})
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", create.Code, create.Body.String())
	}

	request := doJSON(t, handler, http.MethodPost, "/v1/sessions/demo/resources", map[string]any{
		"desired_resource": map[string]any{"cpu_milli": 4000, "memory_bytes": 2147483648},
		"generation":       1,
		"priority":         2,
	})
	if request.Code != http.StatusAccepted {
		t.Fatalf("request status = %d, body=%s", request.Code, request.Body.String())
	}

	run := doJSON(t, handler, http.MethodPost, "/v1/scheduler/run-once", map[string]any{})
	if run.Code != http.StatusOK {
		t.Fatalf("scheduler status = %d, body=%s", run.Code, run.Body.String())
	}
	get := doJSON(t, handler, http.MethodGet, "/v1/sessions/demo", nil)
	var session model.Session
	if err := json.Unmarshal(get.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Allocated.CPUMilli != 4000 || session.AppliedGeneration != 1 {
		t.Fatalf("unexpected session: %+v", session)
	}
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	resourceScheduler, err := scheduler.New(scheduler.DefaultConfig(model.Resources{CPUMilli: 16000, MemoryBytes: 32 * 1024 * model.MiB}))
	if err != nil {
		t.Fatal(err)
	}
	resourceController := controller.New(store.NewMemorySessionStore(), queue.New(), resourceScheduler, backend.NewMemoryBackend())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(resourceController, logger).Handler()
}

func doJSON(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, payload).WithContext(context.Background())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
