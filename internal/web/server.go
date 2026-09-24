package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/history"
	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

//go:embed static/*
var staticFiles embed.FS

const (
	statusWindow  = 60 * time.Minute
	statusBuckets = 60
)

type Server struct {
	store      *history.Store
	mux        *http.ServeMux
	staleAfter time.Duration
}

func New(store *history.Store) (*Server, error) {
	return NewWithStaleAfter(store, 3*time.Minute)
}

func NewWithStaleAfter(store *history.Store, staleAfter time.Duration) (*Server, error) {
	if staleAfter <= 0 {
		return nil, errors.New("stale threshold must be positive")
	}
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("prepare static files: %w", err)
	}
	s := &Server{store: store, mux: http.NewServeMux(), staleAfter: staleAfter}
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.Handle("GET /assets/", http.StripPrefix("/assets/", staticFileServer(staticFS)))
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveStatic(w, r, staticFS, "index.html", "text/html; charset=utf-8")
	})
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return securityHeaders(s.mux)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	snapshots := withoutObsoleteChecks(s.store.Since(time.Time{}))
	var current model.Snapshot
	ok := len(snapshots) > 0
	if ok {
		current = snapshots[len(snapshots)-1]
	}
	timelineEnd := timelineEndFor(now, current.Timestamp, ok)
	cutoff := timelineEnd.Add(-statusWindow)
	stale := !ok || now.Sub(current.Timestamp) > s.staleAfter
	if !ok {
		current = model.Snapshot{
			Timestamp: now,
			Overall:   model.Unknown,
			Summary:   "等待第一次网络探测",
			Connector: model.Connector{Mode: "unknown", Protocol: "unknown", Status: model.Unknown, Detail: "尚无数据"},
		}
	} else {
		current = normalizeSnapshot(current)
		if stale {
			current = staleSnapshot(current)
		}
	}
	incidents := filterIncidents(history.Incidents(snapshots), cutoff)
	response := model.APIResponse{
		GeneratedAt: now,
		HasSnapshot: ok,
		Current:     current,
		History:     aggregateTimeline(snapshots, cutoff, timelineEnd, statusBuckets),
		Incidents:   incidents,
		Range:       "60m",
		Stale:       stale,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(response); err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
	}
}

func timelineEndFor(now, latest time.Time, hasSnapshot bool) time.Time {
	anchor := now
	if hasSnapshot && !latest.IsZero() {
		anchor = latest
	}
	return anchor.UTC().Truncate(time.Minute).Add(time.Minute)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	current, ok := s.store.Latest()
	status := http.StatusOK
	if !ok || time.Since(current.Timestamp) > s.staleAfter {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"ok":%t,"hasSnapshot":%t}`+"\n", status == http.StatusOK, ok)
}

func aggregateTimeline(snapshots []model.Snapshot, start, end time.Time, count int) []model.Snapshot {
	if count <= 0 || !end.After(start) {
		return nil
	}
	width := end.Sub(start) / time.Duration(count)
	result := make([]model.Snapshot, count)
	populated := make([]bool, count)
	for i := range result {
		result[i] = model.Snapshot{
			Timestamp: start.Add(time.Duration(i) * width),
			Overall:   model.Unknown,
			Connector: model.Connector{Mode: "unknown", Protocol: "unknown", Status: model.Unknown, Detail: "此时间段无探测数据"},
		}
	}
	for _, snapshot := range snapshots {
		if snapshot.Timestamp.Before(start) || !snapshot.Timestamp.Before(end) {
			continue
		}
		index := int(snapshot.Timestamp.Sub(start) / width)
		if index >= count {
			index = count - 1
		}
		normalized := normalizeSnapshot(snapshot)
		if !populated[index] {
			bucketTime := result[index].Timestamp
			result[index] = normalized
			result[index].Timestamp = bucketTime
			populated[index] = true
			continue
		}
		mergeWorst(&result[index], normalized)
	}
	return result
}

func normalizeSnapshot(snapshot model.Snapshot) model.Snapshot {
	normalized := cloneSnapshot(snapshot)
	normalized.Overall = normalizeStatus(normalized.Overall)
	if normalized.Connector.Mode == "" {
		normalized.Connector.Mode = "unknown"
	}
	if normalized.Connector.Protocol == "" {
		normalized.Connector.Protocol = "unknown"
	}
	normalized.Connector.Status = normalizeStatus(normalized.Connector.Status)
	for protocol, status := range normalized.Connector.ProtocolStatuses {
		normalized.Connector.ProtocolStatuses[protocol] = normalizeStatus(status)
	}
	for i := range normalized.Checks {
		normalized.Checks[i].Status = normalizeStatus(normalized.Checks[i].Status)
	}
	protocol := normalized.Connector.Protocol
	if len(normalized.Connector.ProtocolStatuses) == 0 && (protocol == "http2" || protocol == "quic") {
		normalized.Connector.ProtocolStatuses = map[string]model.Status{protocol: normalized.Connector.Status}
	}
	return normalized
}

func normalizeStatus(status model.Status) model.Status {
	switch status {
	case model.Healthy, model.Degraded, model.Critical, model.Unknown:
		return status
	default:
		return model.Unknown
	}
}

func cloneSnapshot(snapshot model.Snapshot) model.Snapshot {
	cloned := snapshot
	cloned.Checks = append([]model.Check(nil), snapshot.Checks...)
	cloned.Connector.ProtocolStatuses = cloneProtocolStatuses(snapshot.Connector.ProtocolStatuses)
	return cloned
}

func withoutObsoleteChecks(snapshots []model.Snapshot) []model.Snapshot {
	filtered := append([]model.Snapshot(nil), snapshots...)
	for i, snapshot := range snapshots {
		hasObsolete := false
		for _, check := range snapshot.Checks {
			if obsoleteProvider(check.ID) {
				hasObsolete = true
				break
			}
		}
		if !hasObsolete {
			continue
		}
		checks := make([]model.Check, 0, len(snapshot.Checks)-1)
		for _, check := range snapshot.Checks {
			if !obsoleteProvider(check.ID) {
				checks = append(checks, check)
			}
		}
		filtered[i].Checks = checks
	}
	return filtered
}

func obsoleteProvider(id string) bool {
	switch id {
	case "provider-ciii", "provider-jimu-ai", "provider-openai-responses", "provider-openai-conversations":
		return true
	default:
		return false
	}
}

func mergeWorst(bucket *model.Snapshot, candidate model.Snapshot) {
	if model.Severity(candidate.Overall) >= model.Severity(bucket.Overall) {
		bucket.Overall = candidate.Overall
		bucket.Summary = candidate.Summary
	}
	protocolStatuses := cloneProtocolStatuses(bucket.Connector.ProtocolStatuses)
	for protocol, status := range candidate.Connector.ProtocolStatuses {
		current, ok := protocolStatuses[protocol]
		if !ok || model.Severity(status) >= model.Severity(current) {
			if protocolStatuses == nil {
				protocolStatuses = make(map[string]model.Status)
			}
			protocolStatuses[protocol] = status
		}
	}
	if model.Severity(candidate.Connector.Status) >= model.Severity(bucket.Connector.Status) {
		bucket.Connector = candidate.Connector
	}
	bucket.Connector.ProtocolStatuses = protocolStatuses
	indices := make(map[string]int, len(bucket.Checks))
	for i, check := range bucket.Checks {
		indices[check.ID] = i
	}
	for _, check := range candidate.Checks {
		index, ok := indices[check.ID]
		if !ok {
			indices[check.ID] = len(bucket.Checks)
			bucket.Checks = append(bucket.Checks, check)
			continue
		}
		if model.Severity(check.Status) >= model.Severity(bucket.Checks[index].Status) {
			bucket.Checks[index] = check
		}
	}
}

func cloneProtocolStatuses(statuses map[string]model.Status) map[string]model.Status {
	if statuses == nil {
		return nil
	}
	cloned := make(map[string]model.Status, len(statuses))
	for protocol, status := range statuses {
		cloned[protocol] = status
	}
	return cloned
}

func staleSnapshot(snapshot model.Snapshot) model.Snapshot {
	stale := cloneSnapshot(snapshot)
	stale.Overall = model.Unknown
	stale.Summary = "最新探测数据已过期"
	stale.Connector.Status = model.Unknown
	stale.Connector.Mode = "unknown"
	stale.Connector.Protocol = "unknown"
	stale.Connector.ProtocolStatuses = nil
	stale.Connector.Connections = 0
	stale.Connector.Detail = "最新 connector 数据已过期"
	for i := range stale.Checks {
		stale.Checks[i].Status = model.Unknown
		stale.Checks[i].LatencyMS = 0
		stale.Checks[i].Detail = "最新探测数据已过期"
	}
	return stale
}

func filterIncidents(incidents []model.Incident, cutoff time.Time) []model.Incident {
	filtered := make([]model.Incident, 0, len(incidents))
	for _, incident := range incidents {
		if incident.Open || !incident.ResolvedAt.Before(cutoff) {
			filtered = append(filtered, incident)
		}
	}
	return filtered
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

func staticFileServer(files fs.FS) http.Handler {
	server := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		server.ServeHTTP(w, r)
	})
}

func serveStatic(w http.ResponseWriter, r *http.Request, files fs.FS, name, contentType string) {
	content, err := fs.ReadFile(files, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(content)
}
