package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

type Store struct {
	mu        sync.RWMutex
	path      string
	retention time.Duration
	snapshots []model.Snapshot
	compacted time.Time
}

func New(path string, retention time.Duration) (*Store, error) {
	if retention <= 0 {
		return nil, errors.New("retention must be positive")
	}
	s := &Store{path: path, retention: retention, compacted: time.Now()}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	defer f.Close()

	cutoff := time.Now().Add(-s.retention)
	stale := false
	scanner := bufio.NewScanner(f)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 1024*1024)
	for scanner.Scan() {
		var snapshot model.Snapshot
		if err := json.Unmarshal(scanner.Bytes(), &snapshot); err != nil {
			continue
		}
		if !snapshot.Timestamp.Before(cutoff) {
			s.snapshots = append(s.snapshots, snapshot)
		} else {
			stale = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan history: %w", err)
	}
	sort.Slice(s.snapshots, func(i, j int) bool {
		return s.snapshots[i].Timestamp.Before(s.snapshots[j].Timestamp)
	})
	if stale {
		return s.rewriteLocked()
	}
	return nil
}

func (s *Store) Append(snapshot model.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("open history for append: %w", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err == nil {
		_, err = f.Write(append(encoded, '\n'))
	}
	closeErr := f.Close()
	if err != nil {
		return fmt.Errorf("append history: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close history: %w", closeErr)
	}

	s.snapshots = append(s.snapshots, snapshot)
	s.pruneLocked(snapshot.Timestamp.Add(-s.retention))
	if snapshot.Timestamp.Sub(s.compacted) >= 24*time.Hour {
		if err := s.rewriteLocked(); err != nil {
			return err
		}
		s.compacted = snapshot.Timestamp
	}
	return nil
}

func (s *Store) rewriteLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	tempPath := s.path + ".tmp"
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("create compacted history: %w", err)
	}
	encoder := json.NewEncoder(f)
	for _, snapshot := range s.snapshots {
		if err := encoder.Encode(snapshot); err != nil {
			_ = f.Close()
			return fmt.Errorf("encode compacted history: %w", err)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync compacted history: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close compacted history: %w", err)
	}
	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace compacted history: %w", err)
	}
	return nil
}

func (s *Store) pruneLocked(cutoff time.Time) {
	first := 0
	for first < len(s.snapshots) && s.snapshots[first].Timestamp.Before(cutoff) {
		first++
	}
	if first > 0 {
		s.snapshots = append([]model.Snapshot(nil), s.snapshots[first:]...)
	}
}

func (s *Store) Since(cutoff time.Time) []model.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idx := sort.Search(len(s.snapshots), func(i int) bool {
		return !s.snapshots[i].Timestamp.Before(cutoff)
	})
	return append([]model.Snapshot(nil), s.snapshots[idx:]...)
}

func (s *Store) Latest() (model.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.snapshots) == 0 {
		return model.Snapshot{}, false
	}
	return s.snapshots[len(s.snapshots)-1], true
}

func Incidents(snapshots []model.Snapshot) []model.Incident {
	type activeIncident struct {
		incident model.Incident
		status   model.Status
	}
	active := map[string]activeIncident{}
	var incidents []model.Incident

	for _, snapshot := range snapshots {
		checks := append([]model.Check(nil), snapshot.Checks...)
		checks = append(checks, model.Check{
			ID:       "production-connector",
			Name:     "生产 Tunnel connector",
			Protocol: snapshot.Connector.Protocol,
			Status:   snapshot.Connector.Status,
			Detail:   snapshot.Connector.Detail,
		})
		for _, check := range checks {
			current, exists := active[check.ID]
			bad := check.Status == model.Degraded || check.Status == model.Critical
			if bad && !exists {
				active[check.ID] = activeIncident{incident: model.Incident{
					ID:        fmt.Sprintf("%s-%d", check.ID, snapshot.Timestamp.Unix()),
					StartedAt: snapshot.Timestamp,
					Open:      true,
					Severity:  check.Status,
					Title:     check.Name + " " + statusTitle(check.Status),
					Detail:    check.Detail,
					CheckID:   check.ID,
				}, status: check.Status}
				continue
			}
			if bad && exists {
				if model.Severity(check.Status) > model.Severity(current.incident.Severity) {
					current.incident.Severity = check.Status
				}
				current.incident.Detail = check.Detail
				active[check.ID] = current
				continue
			}
			if check.Status == model.Healthy && exists {
				current.incident.Open = false
				current.incident.ResolvedAt = snapshot.Timestamp
				incidents = append(incidents, current.incident)
				delete(active, check.ID)
			}
		}
	}
	for _, current := range active {
		incidents = append(incidents, current.incident)
	}
	sort.Slice(incidents, func(i, j int) bool {
		return incidents[i].StartedAt.After(incidents[j].StartedAt)
	})
	if len(incidents) > 20 {
		incidents = incidents[:20]
	}
	return incidents
}

func statusTitle(status model.Status) string {
	if status == model.Critical {
		return "不可用"
	}
	return "出现降级"
}
