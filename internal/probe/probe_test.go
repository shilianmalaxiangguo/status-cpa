package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

func TestParseMetric(t *testing.T) {
	t.Parallel()

	input := "# HELP x ignored\ncloudflared_tunnel_ha_connections 4\n"
	value, ok := parseMetric(strings.NewReader(input), "cloudflared_tunnel_ha_connections")
	if !ok || value != 4 {
		t.Fatalf("expected metric value 4, got %v, %v", value, ok)
	}
}

func TestCollectConnectorStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		statusCode  int
		body        string
		want        model.Status
		connections int
	}{
		{name: "non 200 is unknown", statusCode: http.StatusServiceUnavailable, want: model.Unknown},
		{name: "missing metric is unknown", statusCode: http.StatusOK, body: "# no connector metric\n", want: model.Unknown},
		{name: "zero is critical", statusCode: http.StatusOK, body: "cloudflared_tunnel_ha_connections 0\n", want: model.Critical},
		{name: "one is degraded", statusCode: http.StatusOK, body: "cloudflared_tunnel_ha_connections 1\n", want: model.Degraded, connections: 1},
		{name: "four is healthy", statusCode: http.StatusOK, body: "cloudflared_tunnel_ha_connections 4\n", want: model.Healthy, connections: 4},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			collector := New(Config{MetricsURL: server.URL, Timeout: time.Second})
			connector := collector.collectConnector(context.Background())
			if connector.Status != test.want || connector.Connections != test.connections {
				t.Fatalf("expected %s with %d connections, got %+v", test.want, test.connections, connector)
			}
		})
	}
}

func TestCollectConnectorConnectionFailureIsUnknown(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()
	collector := New(Config{MetricsURL: url, Timeout: 100 * time.Millisecond})
	connector := collector.collectConnector(context.Background())
	if connector.Status != model.Unknown {
		t.Fatalf("expected unknown when metrics cannot be reached, got %+v", connector)
	}
}

func TestOverallStatusTreatsUnusedQUICAsDegraded(t *testing.T) {
	t.Parallel()

	connector := model.Connector{Status: model.Healthy, Connections: 4}
	checks := []model.Check{
		{ID: "http2-edge", Status: model.Healthy},
		{ID: "quic-edge", Status: model.Critical},
		{ID: "public-api", Status: model.Healthy},
		{ID: "public-panel", Status: model.Healthy},
	}
	status, _ := overallStatus(connector, checks)
	if status != model.Degraded {
		t.Fatalf("expected degraded for unused QUIC failure, got %s", status)
	}
}

func TestOverallStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		connector model.Status
		checks    []model.Check
		want      model.Status
	}{
		{name: "proven zero connectors", connector: model.Critical, want: model.Critical},
		{name: "metrics unknown", connector: model.Unknown, want: model.Degraded},
		{name: "both public endpoints fail", connector: model.Healthy, checks: []model.Check{{ID: "public-api", Status: model.Critical}, {ID: "public-panel", Status: model.Critical}}, want: model.Critical},
		{name: "two local endpoints fail", connector: model.Healthy, checks: []model.Check{{ID: "local-api", Status: model.Critical}, {ID: "local-panel", Status: model.Critical}}, want: model.Degraded},
		{name: "one public and one local fail", connector: model.Healthy, checks: []model.Check{{ID: "public-api", Status: model.Critical}, {ID: "local-panel", Status: model.Critical}}, want: model.Degraded},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			status, _ := overallStatus(model.Connector{Status: test.connector}, test.checks)
			if status != test.want {
				t.Fatalf("expected %s, got %s", test.want, status)
			}
		})
	}
}
