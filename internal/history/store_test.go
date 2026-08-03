package history

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

func TestStoreAppendReloadAndPrune(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "history.jsonl")
	store, err := New(path, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, offset := range []time.Duration{-3 * time.Hour, -time.Hour, 0} {
		if err := store.Append(model.Snapshot{Timestamp: now.Add(offset), Overall: model.Healthy}); err != nil {
			t.Fatal(err)
		}
	}

	got := store.Since(now.Add(-2 * time.Hour))
	if len(got) != 2 {
		t.Fatalf("expected 2 retained snapshots, got %d", len(got))
	}

	reloaded, err := New(path, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got = reloaded.Since(now.Add(-2 * time.Hour))
	if len(got) != 2 {
		t.Fatalf("expected 2 snapshots after reload, got %d", len(got))
	}
}

func TestIncidentsIncludesConnectorAndResolution(t *testing.T) {
	t.Parallel()

	base := time.Now().Add(-10 * time.Minute).UTC()
	snapshots := []model.Snapshot{
		{Timestamp: base, Connector: model.Connector{Protocol: "http2", Status: model.Critical, Detail: "0 connectors"}},
		{Timestamp: base.Add(5 * time.Minute), Connector: model.Connector{Protocol: "http2", Status: model.Healthy, Detail: "4 connectors"}},
	}
	incidents := Incidents(snapshots)
	if len(incidents) != 1 {
		t.Fatalf("expected one connector incident, got %d", len(incidents))
	}
	if incidents[0].Open || incidents[0].ResolvedAt.IsZero() {
		t.Fatalf("expected resolved incident, got %+v", incidents[0])
	}
}

func TestIncidentRemainsOpenWhileStatusIsUnknown(t *testing.T) {
	t.Parallel()

	base := time.Now().Add(-10 * time.Minute).UTC()
	snapshots := []model.Snapshot{
		{Timestamp: base, Connector: model.Connector{Protocol: "http2", Status: model.Degraded, Detail: "1 connector"}},
		{Timestamp: base.Add(5 * time.Minute), Connector: model.Connector{Protocol: "http2", Status: model.Unknown, Detail: "metrics unavailable"}},
	}
	incidents := Incidents(snapshots)
	if len(incidents) != 1 || !incidents[0].Open || !incidents[0].ResolvedAt.IsZero() {
		t.Fatalf("expected incident to remain open, got %+v", incidents)
	}

	snapshots = append(snapshots, model.Snapshot{
		Timestamp: base.Add(9 * time.Minute),
		Connector: model.Connector{Protocol: "http2", Status: model.Healthy, Detail: "4 connectors"},
	})
	incidents = Incidents(snapshots)
	if len(incidents) != 1 || incidents[0].Open || incidents[0].ResolvedAt.IsZero() {
		t.Fatalf("expected incident to resolve after healthy evidence, got %+v", incidents)
	}
}
