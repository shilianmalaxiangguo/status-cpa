package model

import "time"

type Status string

const (
	Healthy  Status = "healthy"
	Degraded Status = "degraded"
	Critical Status = "critical"
	Unknown  Status = "unknown"
)

type Check struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Protocol       string   `json:"protocol"`
	Status         Status   `json:"status"`
	LatencyMS      float64  `json:"latencyMs,omitempty"`
	RateMultiplier *float64 `json:"rateMultiplier,omitempty"`
	Value          float64  `json:"value,omitempty"`
	ValueLabel     string   `json:"valueLabel,omitempty"`
	Target         string   `json:"target,omitempty"`
	Detail         string   `json:"detail"`
	FailureCode    string   `json:"failureCode,omitempty"`
}

type Snapshot struct {
	Timestamp   time.Time `json:"timestamp"`
	Overall     Status    `json:"overall"`
	Summary     string    `json:"summary"`
	Connector   Connector `json:"connector"`
	Checks      []Check   `json:"checks"`
	NextProbeAt time.Time `json:"nextProbeAt"`
}

type Connector struct {
	Mode             string            `json:"mode"`
	Protocol         string            `json:"protocol"`
	ProtocolStatuses map[string]Status `json:"protocolStatuses,omitempty"`
	Connections      int               `json:"connections"`
	Status           Status            `json:"status"`
	Detail           string            `json:"detail"`
}

type Incident struct {
	ID         string    `json:"id"`
	StartedAt  time.Time `json:"startedAt"`
	ResolvedAt time.Time `json:"resolvedAt,omitempty"`
	Open       bool      `json:"open"`
	Severity   Status    `json:"severity"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail"`
	CheckID    string    `json:"checkId"`
}

type APIResponse struct {
	GeneratedAt time.Time  `json:"generatedAt"`
	HasSnapshot bool       `json:"hasSnapshot"`
	Current     Snapshot   `json:"current"`
	History     []Snapshot `json:"history"`
	Incidents   []Incident `json:"incidents"`
	Range       string     `json:"range"`
	Stale       bool       `json:"stale"`
}

func Severity(s Status) int {
	switch s {
	case Critical:
		return 3
	case Degraded:
		return 2
	case Unknown:
		return 1
	default:
		return 0
	}
}
