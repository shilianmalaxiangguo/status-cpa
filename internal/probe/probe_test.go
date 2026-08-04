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

func TestCollectConnectorProtocols(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		configuration string
		tunnel        string
		wantMode      string
		wantProtocol  string
	}{
		{
			name:          "forced quic with quic connections",
			configuration: `{"protocol":"quic"}`,
			tunnel:        `{"connections":[{"isConnected":true,"protocol":1},{"isConnected":true,"protocol":1}]}`,
			wantMode:      "quic",
			wantProtocol:  "quic",
		},
		{
			name:          "explicit auto with http2 connection",
			configuration: `{"protocol":"auto"}`,
			tunnel:        `{"connections":[{"isConnected":true}]}`,
			wantMode:      "auto",
			wantProtocol:  "http2",
		},
		{
			name:          "missing configured mode uses cloudflared auto default",
			configuration: `{}`,
			tunnel:        `{"connections":[{"isConnected":true}]}`,
			wantMode:      "auto",
			wantProtocol:  "http2",
		},
		{
			name:          "legacy h2mux mode is http2",
			configuration: `{"protocol":"h2mux"}`,
			tunnel:        `{"connections":[{"isConnected":true}]}`,
			wantMode:      "http2",
			wantProtocol:  "http2",
		},
		{
			name:          "mixed active protocols are unknown",
			configuration: `{"protocol":"auto"}`,
			tunnel:        `{"connections":[{"isConnected":true},{"isConnected":true,"protocol":1}]}`,
			wantMode:      "auto",
			wantProtocol:  "unknown",
		},
		{
			name:          "unsupported values are unknown",
			configuration: `{"protocol":"future"}`,
			tunnel:        `{"connections":[{"isConnected":true,"protocol":9}]}`,
			wantMode:      "unknown",
			wantProtocol:  "unknown",
		},
		{
			name:          "no active connections is unknown",
			configuration: `{"protocol":"quic"}`,
			tunnel:        `{"connections":[]}`,
			wantMode:      "quic",
			wantProtocol:  "unknown",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/metrics":
					_, _ = fmt.Fprintln(w, "cloudflared_tunnel_ha_connections 4")
				case configurationDiagPath:
					_, _ = fmt.Fprint(w, test.configuration)
				case tunnelDiagPath:
					_, _ = fmt.Fprint(w, test.tunnel)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			collector := New(Config{MetricsURL: server.URL + "/metrics?ignored=true", Timeout: time.Second})
			connector := collector.collectConnector(context.Background())
			if connector.Mode != test.wantMode || connector.Protocol != test.wantProtocol {
				t.Fatalf("expected mode %s and protocol %s, got %+v", test.wantMode, test.wantProtocol, connector)
			}
			if connector.Status != model.Healthy || connector.Connections != 4 {
				t.Fatalf("expected healthy connector metrics, got %+v", connector)
			}
			statusProtocol := test.wantProtocol
			if statusProtocol == "unknown" && (test.wantMode == "http2" || test.wantMode == "quic") {
				statusProtocol = test.wantMode
			}
			if statusProtocol == "http2" || statusProtocol == "quic" {
				if connector.ProtocolStatuses[statusProtocol] != model.Healthy {
					t.Fatalf("expected healthy %s connector status, got %+v", statusProtocol, connector.ProtocolStatuses)
				}
			}
		})
	}
}

func TestCollectConnectorKeepsDiagnosticsWhenMetricsFail(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case configurationDiagPath:
			_, _ = fmt.Fprint(w, `{"protocol":"quic"}`)
		case tunnelDiagPath:
			_, _ = fmt.Fprint(w, `{"connections":[{"isConnected":true,"protocol":1}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	collector := New(Config{MetricsURL: server.URL + "/metrics", Timeout: time.Second})
	connector := collector.collectConnector(context.Background())
	if connector.Status != model.Unknown || connector.Mode != "quic" || connector.Protocol != "quic" {
		t.Fatalf("expected independent diagnostics with unknown metrics, got %+v", connector)
	}
}

func TestCollectConnectorReportsConfiguredModeWhenNoConnectionsAreActive(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			_, _ = fmt.Fprintln(w, "cloudflared_tunnel_ha_connections 0")
		case configurationDiagPath:
			_, _ = fmt.Fprint(w, `{"protocol":"quic"}`)
		case tunnelDiagPath:
			_, _ = fmt.Fprint(w, `{"connections":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	collector := New(Config{MetricsURL: server.URL + "/metrics", Timeout: time.Second})
	connector := collector.collectConnector(context.Background())
	if connector.Status != model.Critical || connector.Mode != "quic" || connector.Protocol != "unknown" {
		t.Fatalf("expected critical configured QUIC connector with no active protocol, got %+v", connector)
	}
	if connector.ProtocolStatuses["quic"] != model.Critical {
		t.Fatalf("expected critical QUIC connector status, got %+v", connector.ProtocolStatuses)
	}
}

func TestCollectConnectorKeepsMetricsWhenDiagnosticsFail(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			_, _ = fmt.Fprintln(w, "cloudflared_tunnel_ha_connections 4")
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	collector := New(Config{MetricsURL: server.URL + "/metrics", Timeout: time.Second})
	connector := collector.collectConnector(context.Background())
	if connector.Status != model.Healthy || connector.Mode != "unknown" || connector.Protocol != "unknown" {
		t.Fatalf("expected healthy metrics with unknown diagnostics, got %+v", connector)
	}
}

func TestCollectConfiguredModeRejectsInvalidResponseBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "trailing JSON value", body: `{"protocol":"auto"}{}`},
		{name: "oversized response", body: `{"protocol":"auto"}` + strings.Repeat(" ", maxDiagnosticBody)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			defer server.Close()

			collector := New(Config{MetricsURL: server.URL + "/metrics", Timeout: time.Second})
			if mode := collector.collectConfiguredMode(context.Background()); mode != "unknown" {
				t.Fatalf("expected invalid diagnostic response to be unknown, got %q", mode)
			}
		})
	}
}

func TestOverallStatusTreatsEdgeProbeFailureAsDegraded(t *testing.T) {
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
		t.Fatalf("expected degraded for edge probe failure, got %s", status)
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

func TestOverallStatusSummaryFollowsActiveProtocol(t *testing.T) {
	t.Parallel()

	checks := []model.Check{
		{ID: "http2-edge", Status: model.Healthy},
		{ID: "quic-edge", Status: model.Healthy},
	}
	status, summary := overallStatus(model.Connector{Protocol: "quic", Status: model.Healthy}, checks)
	if status != model.Healthy || !strings.Contains(summary, "QUIC 生产 Tunnel") {
		t.Fatalf("expected healthy QUIC production summary, got %s: %s", status, summary)
	}
}
