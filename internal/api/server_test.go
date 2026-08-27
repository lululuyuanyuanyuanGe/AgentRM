package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/cgroup"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/daemon"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/mlfq"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/model"
	"github.com/lululuyuanyuanyuanGe/AgentRM/internal/store"
)

func TestJobLifecycle(t *testing.T) {
	handler, groups, advance := testHandler(t)
	groups.Add("kubepods/sandbox-a/job-a", cgroup.CPUStat{}, 100)

	created := doJSON(t, handler, http.MethodPost, "/v1/jobs", map[string]any{
		"job_id": "job-a", "sandbox_id": "sandbox-a", "cgroup_path": "kubepods/sandbox-a/job-a",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var job model.ToolJob
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Level != model.QueueQ0 || job.CPUWeight != 10000 {
		t.Fatalf("unexpected new job: %+v", job)
	}

	_ = groups.SetUsage(job.CgroupPath, 250)
	advance(time.Second)
	tick := doJSON(t, handler, http.MethodPost, "/v1/tick", nil)
	if tick.Code != http.StatusOK {
		t.Fatalf("tick status=%d body=%s", tick.Code, tick.Body.String())
	}

	get := doJSON(t, handler, http.MethodGet, "/v1/jobs/job-a", nil)
	if err := json.Unmarshal(get.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Level != model.QueueQ1 || job.CPUWeight != 3000 {
		t.Fatalf("job was not demoted: %+v", job)
	}

	finished := doJSON(t, handler, http.MethodPost, "/v1/jobs/job-a/finish", map[string]any{})
	if finished.Code != http.StatusOK {
		t.Fatalf("finish status=%d body=%s", finished.Code, finished.Body.String())
	}
	if err := json.Unmarshal(finished.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.State != model.JobFinished || job.CPUWeight != 100 {
		t.Fatalf("unexpected finished job: %+v", job)
	}
}

func TestConfigAndSnapshot(t *testing.T) {
	handler, _, _ := testHandler(t)
	config := doJSON(t, handler, http.MethodGet, "/v1/config", nil)
	if config.Code != http.StatusOK || !bytes.Contains(config.Body.Bytes(), []byte(`"Q0"`)) {
		t.Fatalf("unexpected config response: %d %s", config.Code, config.Body.String())
	}
	snapshot := doJSON(t, handler, http.MethodGet, "/v1/scheduler", nil)
	if snapshot.Code != http.StatusOK || !bytes.Contains(snapshot.Body.Bytes(), []byte(`"running":0`)) {
		t.Fatalf("unexpected snapshot response: %d %s", snapshot.Code, snapshot.Body.String())
	}
}

func testHandler(t *testing.T) (http.Handler, *cgroup.MemoryClient, func(time.Duration)) {
	t.Helper()
	config := mlfq.DefaultConfig()
	config.Q0.Quantum = 250 * time.Microsecond
	engine, err := mlfq.New(config)
	if err != nil {
		t.Fatal(err)
	}
	groups := cgroup.NewMemoryClient()
	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	nodeDaemonNow := &now
	nodeDaemon := daemon.New(store.NewMemoryJobStore(), groups, engine, daemon.WithClock(func() time.Time { return *nodeDaemonNow }))
	advance := func(duration time.Duration) { *nodeDaemonNow = nodeDaemonNow.Add(duration) }
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(nodeDaemon, logger).Handler(), groups, advance
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
	request := httptest.NewRequest(method, path, payload)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
