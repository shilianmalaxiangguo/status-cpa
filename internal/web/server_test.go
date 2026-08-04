package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
		Connector: model.Connector{Mode: "auto", Protocol: "quic", Connections: 4, Status: model.Healthy},
		Checks:    []model.Check{{ID: "provider-ai-input", Name: "AI INPUT", Status: model.Healthy, LatencyMS: 1200}},
	}
	if err := store.Append(snapshot); err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status?range=7d", nil)
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
	if !response.HasSnapshot {
		t.Fatal("expected API to report an available snapshot")
	}
	if response.Current.Connector.Mode != "auto" || response.Current.Connector.Protocol != "quic" {
		t.Fatalf("expected auto mode with active QUIC, got %+v", response.Current.Connector)
	}
	if checkStatus(response.Current.Checks, "provider-ai-input") != model.Healthy {
		t.Fatalf("expected provider check in API response, got %+v", response.Current.Checks)
	}
	if response.Range != "60m" || len(response.History) != statusBuckets {
		t.Fatalf("expected fixed 60-minute history, got range %q with %d buckets", response.Range, len(response.History))
	}
	if response.History[1].Timestamp.Sub(response.History[0].Timestamp) != time.Minute {
		t.Fatalf("expected one-minute buckets, got %s", response.History[1].Timestamp.Sub(response.History[0].Timestamp))
	}

	assetRecorder := httptest.NewRecorder()
	assetRequest := httptest.NewRequest(http.MethodGet, "/assets/app.js?v=16", nil)
	server.Handler().ServeHTTP(assetRecorder, assetRequest)
	if assetRecorder.Code != http.StatusOK {
		t.Fatalf("expected asset response 200, got %d", assetRecorder.Code)
	}
	if assetRecorder.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable asset response, got %q", assetRecorder.Header().Get("Cache-Control"))
	}

	indexRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(indexRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	index := indexRecorder.Body.String()
	for _, expected := range []string{`data-theme="dark"`, `id="theme-toggle"`, `id="track-tooltip"`, `role="slider"`, `id="tunnel-state"`, `id="provider-list"`, `id="provider-live-status"`, `id="provider-openai-row"`, `近 60 分钟可用率`, `60 分钟前`, `/assets/app.css?v=16`, `/assets/app.js?v=16`} {
		if !strings.Contains(index, expected) {
			t.Fatalf("expected index to contain %q", expected)
		}
	}
	if strings.Contains(index, `data-range=`) {
		t.Fatal("expected historical range switch to be removed")
	}
	if strings.Contains(index, `id="provider-list" aria-live=`) {
		t.Fatal("expected provider timeline grid not to be a live region")
	}
	if strings.Contains(index, `id="protocol-list" aria-live=`) {
		t.Fatal("expected protocol timeline grid not to be a live region")
	}
}

func TestAggregateTimelinePreservesWorstProtocolStateAndFullRange(t *testing.T) {
	t.Parallel()

	end := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	start := end.Add(-statusWindow)
	snapshots := []model.Snapshot{
		{Timestamp: start.Add(5 * time.Second), Overall: model.Degraded, Connector: model.Connector{Mode: "quic", Protocol: "quic", Status: model.Healthy}, Checks: []model.Check{{ID: "quic-edge", Status: model.Critical}}},
		{Timestamp: start.Add(10 * time.Second), Overall: model.Degraded, Connector: model.Connector{Mode: "quic", Protocol: "quic", Status: model.Critical}, Checks: []model.Check{{ID: "quic-edge", Status: model.Healthy}}},
		{Timestamp: end.Add(-time.Second), Overall: model.Healthy, Connector: model.Connector{Mode: "quic", Protocol: "quic", Status: model.Healthy}, Checks: []model.Check{{ID: "quic-edge", Status: model.Healthy}}},
	}
	timeline := aggregateTimeline(snapshots, start, end, statusBuckets)
	if len(timeline) != statusBuckets {
		t.Fatalf("expected %d buckets, got %d", statusBuckets, len(timeline))
	}
	if timeline[0].Connector.Status != model.Critical || checkStatus(timeline[0].Checks, "quic-edge") != model.Critical {
		t.Fatalf("expected independent worst statuses in first bucket, got %+v", timeline[0])
	}
	if timeline[statusBuckets-1].Connector.Status != model.Healthy {
		t.Fatalf("expected latest bucket to be retained, got %+v", timeline[statusBuckets-1])
	}
	if timeline[1].Connector.Status != model.Unknown {
		t.Fatalf("expected missing bucket to be unknown, got %+v", timeline[1])
	}
	if timeline[1].Connector.Mode != "unknown" || timeline[1].Connector.Protocol != "unknown" {
		t.Fatalf("expected missing bucket protocols to be unknown, got %+v", timeline[1].Connector)
	}
}

func TestTimelineEndFollowsLatestSnapshotAcrossMinuteBoundary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 10, 1, 5, 0, time.UTC)
	latest := time.Date(2026, 8, 4, 10, 0, 15, 0, time.UTC)
	end := timelineEndFor(now, latest, true)
	if want := time.Date(2026, 8, 4, 10, 1, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("expected timeline to end after the latest snapshot minute at %s, got %s", want, end)
	}

	timeline := aggregateTimeline([]model.Snapshot{{
		Timestamp: latest,
		Overall:   model.Healthy,
		Connector: model.Connector{Status: model.Healthy},
	}}, end.Add(-statusWindow), end, statusBuckets)
	if got := timeline[len(timeline)-1].Connector.Status; got != model.Healthy {
		t.Fatalf("expected latest snapshot in the rightmost bucket, got %s", got)
	}
}

func TestTimelineEndUsesCurrentMinuteBeforeFirstSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 10, 1, 5, 0, time.FixedZone("CST", 8*60*60))
	end := timelineEndFor(now, time.Time{}, false)
	if want := time.Date(2026, 8, 4, 2, 2, 0, 0, time.UTC); !end.Equal(want) {
		t.Fatalf("expected empty timeline to follow the current minute at %s, got %s", want, end)
	}
}

func TestAggregateTimelineUsesHalfOpenWindow(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	timeline := aggregateTimeline([]model.Snapshot{
		{Timestamp: start, Overall: model.Healthy, Connector: model.Connector{Status: model.Healthy}},
		{Timestamp: end, Overall: model.Critical, Connector: model.Connector{Status: model.Critical}},
	}, start, end, 1)
	if len(timeline) != 1 || timeline[0].Overall != model.Healthy || timeline[0].Connector.Status != model.Healthy {
		t.Fatalf("expected start included and end excluded, got %+v", timeline)
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
		Connector: model.Connector{Mode: "quic", Protocol: "quic", Status: model.Healthy, Connections: 4},
		Checks: []model.Check{
			{ID: "http2-edge", Status: model.Healthy, LatencyMS: 100},
			{ID: "provider-ai-input", Status: model.Healthy, LatencyMS: 1200},
		},
	}); err != nil {
		t.Fatal(err)
	}
	server, err := NewWithStaleAfter(store, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var response model.APIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Stale || response.Current.Connector.Status != model.Unknown || response.Current.Connector.Mode != "unknown" || response.Current.Connector.Protocol != "unknown" || checkStatus(response.Current.Checks, "http2-edge") != model.Unknown || checkStatus(response.Current.Checks, "provider-ai-input") != model.Unknown {
		t.Fatalf("expected stale current state to be unknown, got %+v", response)
	}
}

func TestAggregateTimelinePreservesConnectorStatusForEachProtocol(t *testing.T) {
	t.Parallel()

	end := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	start := end.Add(-statusWindow)
	snapshots := []model.Snapshot{
		{
			Timestamp: start.Add(10 * time.Second),
			Overall:   model.Degraded,
			Connector: model.Connector{Mode: "auto", Protocol: "quic", Status: model.Degraded, ProtocolStatuses: map[string]model.Status{"quic": model.Degraded}},
		},
		{
			Timestamp: start.Add(20 * time.Second),
			Overall:   model.Critical,
			Connector: model.Connector{Mode: "auto", Protocol: "http2", Status: model.Critical, ProtocolStatuses: map[string]model.Status{"http2": model.Critical}},
		},
	}

	timeline := aggregateTimeline(snapshots, start, end, statusBuckets)
	statuses := timeline[0].Connector.ProtocolStatuses
	if statuses["quic"] != model.Degraded || statuses["http2"] != model.Critical {
		t.Fatalf("expected both protocol connector states in one bucket, got %+v", statuses)
	}
}

func TestStatusAPIUsesUnknownProtocolsWithoutSnapshots(t *testing.T) {
	t.Parallel()

	store, err := history.New(filepath.Join(t.TempDir(), "history.jsonl"), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var response model.APIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Current.Connector.Mode != "unknown" || response.Current.Connector.Protocol != "unknown" {
		t.Fatalf("expected unknown protocols before first snapshot, got %+v", response.Current.Connector)
	}
	if response.HasSnapshot {
		t.Fatal("expected API to distinguish waiting for the first snapshot")
	}
}

func TestStatusAPINormalizesLegacyConnectorHistoryWithoutRewritingIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.jsonl")
	timestamp := time.Now().Add(-time.Minute).UTC()
	legacy := fmt.Sprintf(`{"timestamp":%q,"overall":"healthy","summary":"legacy","connector":{"protocol":"http2","connections":4,"status":"healthy","detail":"legacy connector"},"checks":[],"nextProbeAt":%q}`+"\n", timestamp.Format(time.RFC3339Nano), timestamp.Add(time.Minute).Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(legacy), 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := history.New(path, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var response model.APIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	connector := response.Current.Connector
	if connector.Mode != "unknown" || connector.Protocol != "http2" || connector.ProtocolStatuses["http2"] != model.Healthy {
		t.Fatalf("expected normalized legacy connector, got %+v", connector)
	}
	foundHistory := false
	for _, snapshot := range response.History {
		if snapshot.Connector.ProtocolStatuses["http2"] == model.Healthy {
			foundHistory = true
			break
		}
	}
	if !foundHistory {
		t.Fatal("expected legacy connector status to participate in the history timeline")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != legacy {
		t.Fatal("expected legacy history file to remain byte-for-byte unchanged")
	}
}

func TestStatusAPINormalizesInvalidPersistedStatuses(t *testing.T) {
	t.Parallel()

	store, err := history.New(filepath.Join(t.TempDir(), "history.jsonl"), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	invalid := model.Status(`"><meta http-equiv="refresh" content="0;url=/injected">`)
	if err := store.Append(model.Snapshot{
		Timestamp: time.Now().UTC(),
		Overall:   invalid,
		Connector: model.Connector{
			Mode:             "auto",
			Protocol:         "quic",
			ProtocolStatuses: map[string]model.Status{"quic": invalid},
			Status:           invalid,
		},
		Checks: []model.Check{{ID: "local-api", Name: "local", Status: invalid}},
	}); err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var response model.APIResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Current.Overall != model.Unknown || response.Current.Connector.Status != model.Unknown || response.Current.Connector.ProtocolStatuses["quic"] != model.Unknown || checkStatus(response.Current.Checks, "local-api") != model.Unknown {
		t.Fatalf("expected invalid persisted statuses to normalize to unknown, got %+v", response.Current)
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
