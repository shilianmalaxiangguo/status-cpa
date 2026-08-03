package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/history"
	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

//go:embed static/*
var staticFiles embed.FS

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
	rangeName, duration := parseRange(r.URL.Query().Get("range"))
	cutoff := now.Add(-duration)
	snapshots := s.store.Since(cutoff)
	current, ok := s.store.Latest()
	stale := !ok || now.Sub(current.Timestamp) > s.staleAfter
	if !ok {
		current = model.Snapshot{
			Timestamp: now,
			Overall:   model.Unknown,
			Summary:   "等待第一次网络探测",
			Connector: model.Connector{Protocol: "http2", Status: model.Unknown, Detail: "尚无数据"},
		}
	} else if stale {
		current = staleSnapshot(current)
	}
	incidents := filterIncidents(history.Incidents(s.store.Since(time.Time{})), cutoff)
	response := model.APIResponse{
		GeneratedAt: now,
		Current:     current,
		History:     aggregateTimeline(snapshots, cutoff, now, 96),
		Incidents:   incidents,
		Range:       rangeName,
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

func parseRange(value string) (string, time.Duration) {
	switch strings.ToLower(value) {
	case "7d", "168h":
		return "7d", 7 * 24 * time.Hour
	default:
		return "24h", 24 * time.Hour
	}
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
			Connector: model.Connector{Protocol: "http2", Status: model.Unknown, Detail: "此时间段无探测数据"},
		}
	}
	for _, snapshot := range snapshots {
		if snapshot.Timestamp.Before(start) || snapshot.Timestamp.After(end) {
			continue
		}
		index := int(snapshot.Timestamp.Sub(start) / width)
		if index >= count {
			index = count - 1
		}
		if !populated[index] {
			bucketTime := result[index].Timestamp
			result[index] = cloneSnapshot(snapshot)
			result[index].Timestamp = bucketTime
			populated[index] = true
			continue
		}
		mergeWorst(&result[index], snapshot)
	}
	return result
}

func cloneSnapshot(snapshot model.Snapshot) model.Snapshot {
	cloned := snapshot
	cloned.Checks = append([]model.Check(nil), snapshot.Checks...)
	return cloned
}

func mergeWorst(bucket *model.Snapshot, candidate model.Snapshot) {
	if model.Severity(candidate.Overall) >= model.Severity(bucket.Overall) {
		bucket.Overall = candidate.Overall
		bucket.Summary = candidate.Summary
	}
	if model.Severity(candidate.Connector.Status) >= model.Severity(bucket.Connector.Status) {
		bucket.Connector = candidate.Connector
	}
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

func staleSnapshot(snapshot model.Snapshot) model.Snapshot {
	stale := cloneSnapshot(snapshot)
	stale.Overall = model.Unknown
	stale.Summary = "最新探测数据已过期"
	stale.Connector.Status = model.Unknown
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
