package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coulof/ovn-topology/pkg/api"
	"github.com/coulof/ovn-topology/pkg/graph"
)

// VersionConfig holds build-time version metadata.
type VersionConfig struct {
	Version            string
	APIVersion         string
	OVNNbSchemaVersion string
	OVNSbSchemaVersion string
	GitCommit          string
	BuildDate          string
}

// Server serves HTTP and SSE topology endpoints.
type Server struct {
	store   *graph.Store
	version VersionConfig
	logger  *slog.Logger
	mux     *http.ServeMux
	// heartbeat is how often an idle SSE stream gets a comment line, so proxies
	// (the Rancher service proxy) do not close it between graph changes.
	heartbeat time.Duration
}

// NewServer creates a new HTTP server routing all topology API endpoints.
func NewServer(store *graph.Store, version VersionConfig, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		store:   store,
		version: version,
		logger:  logger,
		mux:     http.NewServeMux(),

		heartbeat: 15 * time.Second,
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// Base API v1 endpoints
	s.mux.HandleFunc("GET /api/v1/graph", s.handleGraph)
	s.mux.HandleFunc("GET /api/v1/node/{id}", s.handleNode)
	s.mux.HandleFunc("GET /api/v1/chassis", s.handleChassis)
	s.mux.HandleFunc("GET /api/v1/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	s.mux.HandleFunc("GET /api/v1/healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/v1/readyz", s.handleReadyz)

	// Top-level liveness/readiness probes (k8s standard)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)
	s.mux.HandleFunc("GET /version", s.handleVersion)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	s.mux.ServeHTTP(w, r)
	s.logger.Debug("http request completed",
		"method", r.Method,
		"path", r.URL.Path,
		"duration", time.Since(start))
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if !s.store.IsReady() {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "OVSDB monitors are synchronizing initial snapshot")
		return
	}

	snapshot := s.store.Current()
	currentETag := fmt.Sprintf(`"%d"`, snapshot.Revision)

	if match := r.Header.Get("If-None-Match"); match != "" {
		if match == currentETag || match == fmt.Sprintf("%d", snapshot.Revision) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	query := r.URL.Query()
	view := query.Get("view")
	focus := query.Get("focus")
	depth, _ := strconv.Atoi(query.Get("depth"))
	page, _ := strconv.Atoi(query.Get("page"))
	pageSize, _ := strconv.Atoi(query.Get("pageSize"))

	resp, err := snapshot.BuildGraph(view, focus, depth, page, pageSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	w.Header().Set("ETag", currentETag)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	if !s.store.IsReady() {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "OVSDB monitors are synchronizing initial snapshot")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		// Fallback for query param or trimmed path
		id = strings.TrimPrefix(r.URL.Path, "/api/v1/node/")
	}
	if id == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "node id is required")
		return
	}

	snapshot := s.store.Current()
	detail, err := snapshot.GetNodeDetail(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleChassis(w http.ResponseWriter, _ *http.Request) {
	if !s.store.IsReady() {
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "OVSDB monitors are synchronizing initial snapshot")
		return
	}

	snapshot := s.store.Current()
	chassisList := snapshot.GetChassisList()
	writeJSON(w, http.StatusOK, chassisList)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "STREAMING_UNSUPPORTED", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	eventCh, unsubscribe := s.store.Subscribe()
	defer unsubscribe()

	// Initial event with current revision
	initialSnapshot := s.store.Current()
	initialEvent := api.GraphEvent{
		Revision: int(initialSnapshot.Revision),
		Changed:  []string{"*"},
	}
	data, _ := json.Marshal(initialEvent)
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(s.heartbeat)
	defer heartbeat.Stop()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-eventCh:
			if !ok {
				return
			}
			evBytes, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", evBytes); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.StatusResponse{
		Status: "ok",
	})
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if !s.store.IsReady() {
		msg := "waiting for initial OVSDB monitor sync"
		writeJSON(w, http.StatusServiceUnavailable, api.StatusResponse{
			Status:  "not_ready",
			Message: &msg,
		})
		return
	}
	writeJSON(w, http.StatusOK, api.StatusResponse{
		Status: "ok",
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, api.VersionInfo{
		Version:            s.version.Version,
		ApiVersion:         s.version.APIVersion,
		OvnNbSchemaVersion: s.version.OVNNbSchemaVersion,
		OvnSbSchemaVersion: s.version.OVNSbSchemaVersion,
		GitCommit:          s.version.GitCommit,
		BuildDate:          s.version.BuildDate,
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, api.ErrorResponse{
		Code:    code,
		Message: message,
	})
}

// ErrServerClosed is returned when the HTTP server shuts down normally.
var ErrServerClosed = errors.New("http: Server closed")
