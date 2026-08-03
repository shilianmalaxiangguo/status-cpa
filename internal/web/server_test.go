package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/history"
	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

func TestStatusAPIAndSecurityHeaders(t *testing.T) {
	t.Parallel()

	store, err := history.New(filepath.Join(t.TempDir(), "history.jsonl"), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := model.Snapshot{
		Timestamp: time.Now().UTC(),
		Overall:   model.Healthy,
		Connector: model.Connector{Protocol: "http2", Connections: 4, Status: model.Healthy},
	}
	if err := store.Append(snapshot); err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status?range=24h", nil)
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if recorder.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("expected Content-Security-Policy header")
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected no-store API response, got %q", recorder.Header().Get("Cache-Control"))
	}
	var response model.APIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Current.Connector.Connections != 4 {
		t.Fatalf("expected 4 connectors, got %d", response.Current.Connector.Connections)
	}

	assetRecorder := httptest.NewRecorder()
	assetRequest := httptest.NewRequest(http.MethodGet, "/assets/app.js?v=3", nil)
	server.Handler().ServeHTTP(assetRecorder, assetRequest)
	if assetRecorder.Code != http.StatusOK {
		t.Fatalf("expected asset response 200, got %d", assetRecorder.Code)
	}
	if assetRecorder.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable asset response, got %q", assetRecorder.Header().Get("Cache-Control"))
	}
}

func TestAggregateTimelinePreservesWorstProtocolStateAndFullRange(t *testing.T) {
	t.Parallel()

	end := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	start := end.Add(-24 * time.Hour)
	snapshots := []model.Snapshot{
		{Timestamp: start.Add(5 * time.Minute), Overall: model.Degraded, Connector: model.Connector{Status: model.Healthy}, Checks: []model.Check{{ID: "quic-edge", Status: model.Critical}}},
		{Timestamp: start.Add(10 * time.Minute), Overall: model.Degraded, Connector: model.Connector{Status: model.Critical}, Checks: []model.Check{{ID: "quic-edge", Status: model.Healthy}}},
		{Timestamp: end.Add(-time.Minute), Overall: model.Healthy, Connector: model.Connector{Status: model.Healthy}, Checks: []model.Check{{ID: "quic-edge", Status: model.Healthy}}},
	}
	timeline := aggregateTimeline(snapshots, start, end, 96)
	if len(timeline) != 96 {
		t.Fatalf("expected 96 buckets, got %d", len(timeline))
	}
	if timeline[0].Connector.Status != model.Critical || checkStatus(timeline[0].Checks, "quic-edge") != model.Critical {
		t.Fatalf("expected independent worst statuses in first bucket, got %+v", timeline[0])
	}
	if timeline[95].Connector.Status != model.Healthy {
		t.Fatalf("expected latest bucket to be retained, got %+v", timeline[95])
	}
	if timeline[1].Connector.Status != model.Unknown {
		t.Fatalf("expected missing bucket to be unknown, got %+v", timeline[1])
	}
}

func TestStatusAPIMarksStaleSnapshotUnknown(t *testing.T) {
	t.Parallel()

	store, err := history.New(filepath.Join(t.TempDir(), "history.jsonl"), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(model.Snapshot{
		Timestamp: time.Now().Add(-5 * time.Minute).UTC(),
		Overall:   model.Healthy,
		Connector: model.Connector{Status: model.Healthy, Connections: 4},
		Checks:    []model.Check{{ID: "http2-edge", Status: model.Healthy, LatencyMS: 100}},
	}); err != nil {
		t.Fatal(err)
	}
	server, err := NewWithStaleAfter(store, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status?range=24h", nil))
	var response model.APIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Stale || response.Current.Connector.Status != model.Unknown || checkStatus(response.Current.Checks, "http2-edge") != model.Unknown {
		t.Fatalf("expected stale current state to be unknown, got %+v", response)
	}
}

func checkStatus(checks []model.Check, id string) model.Status {
	for _, check := range checks {
		if check.ID == id {
			return check.Status
		}
	}
	return model.Unknown
}
