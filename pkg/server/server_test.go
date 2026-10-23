package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/graph"
)

func setupTestServer(t *testing.T) (*Server, *graph.Store) {
	t.Helper()
	snap := graph.LoadTestSnapshot(t)
	store := graph.NewStore(snap.NodeMapper)
	store.Apply(func(draft *graph.Snapshot) []string {
		*draft = *snap
		return []string{"*"}
	})

	version := VersionConfig{
		Version:            "0.1.0",
		APIVersion:         "v1",
		OVNNbSchemaVersion: "7.3.0",
		OVNSbSchemaVersion: "20.29.0",
		GitCommit:          "abc1234",
		BuildDate:          "2026-10-20",
	}

	srv := NewServer(store, version, nil)
	return srv, store
}

func TestHealthAndReadiness(t *testing.T) {
	srv, store := setupTestServer(t)

	// Before ready
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 before ready, got %d", w.Code)
	}

	// Healthz always ok
	req = httptest.NewRequest("GET", "/healthz", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for healthz, got %d", w.Code)
	}

	// Set ready
	store.SetReady(true)
	req = httptest.NewRequest("GET", "/readyz", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for readyz after ready, got %d", w.Code)
	}
}

func TestGraphEndpointAndETag(t *testing.T) {
	srv, store := setupTestServer(t)
	store.SetReady(true)

	// 1. Initial GET /api/v1/graph
	req := httptest.NewRequest("GET", "/api/v1/graph?view=l3", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("expected ETag header in response")
	}

	var resp api.GraphResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal graph response: %v", err)
	}
	if resp.View != api.GraphResponseViewL3 {
		t.Errorf("expected l3 view, got %s", resp.View)
	}

	// 2. GET with If-None-Match matching ETag -> 304 Not Modified
	req = httptest.NewRequest("GET", "/api/v1/graph?view=l3", nil)
	req.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotModified {
		t.Errorf("expected 304 Not Modified, got %d", w.Code)
	}
}

func TestNodeDetailEndpoint(t *testing.T) {
	srv, store := setupTestServer(t)
	store.SetReady(true)

	req := httptest.NewRequest("GET", "/api/v1/node/lr:lr-default-uuid", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var detail api.NodeDetail
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatalf("failed to unmarshal node detail: %v", err)
	}
	if detail.Id != "lr:lr-default-uuid" {
		t.Errorf("expected lr:lr-default-uuid, got %s", detail.Id)
	}

	// 404 for unknown node
	req = httptest.NewRequest("GET", "/api/v1/node/lr:non-existent", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown node, got %d", w.Code)
	}
}

func TestChassisEndpoint(t *testing.T) {
	srv, store := setupTestServer(t)
	store.SetReady(true)

	req := httptest.NewRequest("GET", "/api/v1/chassis", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var list api.ChassisList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("failed to unmarshal chassis list: %v", err)
	}
	if len(list.Items) != 3 {
		t.Errorf("expected 3 chassis items, got %d", len(list.Items))
	}
}

func TestVersionEndpoint(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/version", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var v api.VersionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("failed to unmarshal version info: %v", err)
	}
	if v.Version != "0.1.0" || v.ApiVersion != "v1" {
		t.Errorf("unexpected version info: %+v", v)
	}
}

func TestEventsSSE(t *testing.T) {
	srv, store := setupTestServer(t)
	store.SetReady(true)

	server := httptest.NewServer(srv)
	defer server.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("GET", server.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to connect to sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %s", resp.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(resp.Body)
	// Read initial event
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read line: %v", err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("expected line to start with data: , got %s", line)
	}
}

// TestEventsHeartbeat: an idle stream gets comment lines so proxies keep it open.
func TestEventsHeartbeat(t *testing.T) {
	srv, store := setupTestServer(t)
	store.SetReady(true)
	srv.heartbeat = 20 * time.Millisecond
	server := httptest.NewServer(srv)
	defer server.Close()

	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(server.URL + "/api/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	reader := bufio.NewReader(resp.Body)
	sawData := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before a heartbeat (data seen: %v): %v", sawData, err)
		}
		if strings.HasPrefix(line, "data: ") {
			sawData = true
		}
		if strings.HasPrefix(line, ": keepalive") {
			if !sawData {
				t.Errorf("the initial event must come before heartbeats")
			}
			return
		}
	}
}

// TestNoCORSHeaders: the daemon is reached through the Kubernetes service proxy
// (same origin as Rancher), so it allows no cross-origin access.
func TestNoCORSHeaders(t *testing.T) {
	srv, _ := setupTestServer(t)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("version: %d", w.Code)
	}
	if v := w.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Errorf("unexpected CORS header: %q", v)
	}
}
