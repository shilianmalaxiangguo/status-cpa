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

const ciiiModelMetadata = `{"publicGroupList":[{"monitorList":[{"id":13,"name":"Ciii-codex gpt-5.6-sol","type":"keyword"}]}]}`

func TestProbeModelSources(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 5, 10, 0, 0, time.UTC)
	tests := []struct {
		name        string
		kind        ModelSourceKind
		body        string
		wantStatus  model.Status
		wantLatency float64
		wantCode    string
		wantDetail  string
	}{
		{
			name:        "AI INPUT exact model health and latency",
			kind:        ModelSourceAIInput,
			body:        fmt.Sprintf(`{"generated_at":%d,"services":[{"model":"gpt-5.6-sol","uptime_pct":98.33,"last":{"ts":%d,"ok":true,"latency_ms":2820}}]}`, now.Add(-10*time.Second).Unix(), now.Add(-20*time.Second).Unix()),
			wantStatus:  model.Healthy,
			wantLatency: 2820,
			wantDetail:  "最近探测正常",
		},
		{
			name:       "PIPIO exact model outage",
			kind:       ModelSourcePIPIO,
			body:       `{"success":true,"data":[{"categoryName":"模型可用性","monitors":[{"name":"gpt-5.6-sol","status":0,"heartbeats":[1,1,0]}]}]}`,
			wantStatus: model.Critical,
			wantCode:   "reported_outage",
			wantDetail: "发布状态中断",
		},
		{
			name:       "PIPIO pending model status",
			kind:       ModelSourcePIPIO,
			body:       `{"success":true,"data":[{"categoryName":"模型可用性","monitors":[{"name":"gpt-5.6-sol","status":2,"heartbeats":[1,1,2]}]}]}`,
			wantStatus: model.Degraded,
			wantCode:   "reported_degradation",
			wantDetail: "发布状态确认中",
		},
		{
			name:       "PIPIO model maintenance",
			kind:       ModelSourcePIPIO,
			body:       `{"success":true,"data":[{"categoryName":"模型可用性","monitors":[{"name":"gpt-5.6-sol","status":3,"heartbeats":[1,1,3]}]}]}`,
			wantStatus: model.Degraded,
			wantCode:   "reported_degradation",
			wantDetail: "发布状态维护中",
		},
		{
			name:        "KRILL finds latest model state in reversed history",
			kind:        ModelSourceKrill,
			body:        fmt.Sprintf(`{"success":true,"code":0,"data":{"channels":[{"channel_key":"openai_gpt_5_6_sol","model_name":"gpt-5.6-sol","current_status":2,"history":[{"s":2,"ts":"%s"},{"s":1,"ts":"2026-08-04 05:06:00"}]}],"perf":[{"channel_key":"openai_gpt_5_6_sol","ttft_p99_ms":447}]}}`, now.Add(-time.Minute).Format("2006-01-02 15:04:05")),
			wantStatus:  model.Degraded,
			wantLatency: 447,
			wantCode:    "reported_degradation",
			wantDetail:  "TTFT P99",
		},
		{
			name:        "CIII exact model health and latency",
			kind:        ModelSourceCIII,
			body:        `{"heartbeatList":{"13":[{"status":1,"time":"2026-08-04 05:09:30.123","msg":"","ping":3002}]}}`,
			wantStatus:  model.Healthy,
			wantLatency: 3002,
			wantDetail:  "最近探测正常",
		},
		{
			name:       "CIII exact model outage",
			kind:       ModelSourceCIII,
			body:       `{"heartbeatList":{"13":[{"status":0,"time":"2026-08-04 05:09:30.123","msg":"","ping":null}]}}`,
			wantStatus: model.Critical,
			wantCode:   "reported_outage",
			wantDetail: "最近探测失败",
		},
		{
			name:       "CIII pending model heartbeat",
			kind:       ModelSourceCIII,
			body:       `{"heartbeatList":{"13":[{"status":2,"time":"2026-08-04 05:09:30.123","msg":"","ping":null}]}}`,
			wantStatus: model.Degraded,
			wantCode:   "reported_degradation",
			wantDetail: "最近探测确认中",
		},
		{
			name:       "CIII model maintenance",
			kind:       ModelSourceCIII,
			body:       `{"heartbeatList":{"13":[{"status":3,"time":"2026-08-04 05:09:30.123","msg":"","ping":null}]}}`,
			wantStatus: model.Degraded,
			wantCode:   "reported_degradation",
			wantDetail: "最近探测维护中",
		},
		{
			name:       "OpenAI Responses aggregate health",
			kind:       ModelSourceOpenAI,
			body:       `{"components":[{"id":"01JP8CD9JR3HR6Y7G4Q75N4DVW","name":"Responses","status":"operational"}]}`,
			wantStatus: model.Healthy,
			wantDetail: "非 gpt-5.6-sol 单模型探测",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newJSONServer(test.body)
			defer server.Close()
			collector := New(Config{Timeout: time.Second})
			metadataURL := ""
			if test.kind == ModelSourceCIII {
				metadataURL = server.URL + "/metadata"
			}
			check := collector.probeModelSource(context.Background(), ModelSource{
				ID:          "model-source-test",
				Name:        test.name,
				URL:         server.URL,
				MetadataURL: metadataURL,
				Kind:        test.kind,
			}, now)
			if check.Status != test.wantStatus || check.LatencyMS != test.wantLatency {
				t.Fatalf("expected %s at %.0f ms, got %+v", test.wantStatus, test.wantLatency, check)
			}
			if check.FailureCode != test.wantCode {
				t.Fatalf("expected failure code %q, got %+v", test.wantCode, check)
			}
			if !strings.Contains(check.Detail, test.wantDetail) {
				t.Fatalf("expected detail containing %q, got %q", test.wantDetail, check.Detail)
			}
		})
	}
}

func TestProbeModelSourcesRejectInvalidOrStaleData(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 5, 10, 0, 0, time.UTC)
	tests := []struct {
		name         string
		kind         ModelSourceKind
		body         string
		metadataBody string
		contentType  string
		wantCode     string
	}{
		{
			name:     "AI INPUT stale sample",
			kind:     ModelSourceAIInput,
			body:     fmt.Sprintf(`{"generated_at":%d,"services":[{"model":"gpt-5.6-sol","last":{"ts":%d,"ok":true,"latency_ms":100}}]}`, now.Add(-4*time.Minute).Unix(), now.Add(-4*time.Minute).Unix()),
			wantCode: "source_stale",
		},
		{
			name:     "PIPIO conflicting heartbeat",
			kind:     ModelSourcePIPIO,
			body:     `{"success":true,"data":[{"monitors":[{"name":"gpt-5.6-sol","status":1,"heartbeats":[0]}]}]}`,
			wantCode: "source_invalid",
		},
		{
			name:     "KRILL stale sample",
			kind:     ModelSourceKrill,
			body:     `{"success":true,"code":0,"data":{"channels":[{"channel_key":"openai_gpt_5_6_sol","model_name":"gpt-5.6-sol","current_status":1,"history":[{"s":1,"ts":"2026-08-04 04:50:00"}]}],"perf":[]}}`,
			wantCode: "source_stale",
		},
		{
			name:     "KRILL conflicting states at latest timestamp",
			kind:     ModelSourceKrill,
			body:     `{"success":true,"code":0,"data":{"channels":[{"channel_key":"openai_gpt_5_6_sol","model_name":"gpt-5.6-sol","current_status":1,"history":[{"s":1,"ts":"2026-08-04 05:09:00"},{"s":2,"ts":"2026-08-04 05:09:00"}]}],"perf":[]}}`,
			wantCode: "source_invalid",
		},
		{
			name:     "CIII stale model heartbeat",
			kind:     ModelSourceCIII,
			body:     `{"heartbeatList":{"13":[{"status":1,"time":"2026-08-04 05:06:00.000","msg":"","ping":100}]}}`,
			wantCode: "source_stale",
		},
		{
			name:         "CIII rejects mismatched model metadata",
			kind:         ModelSourceCIII,
			body:         `{"heartbeatList":{"13":[{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":100}]}}`,
			metadataBody: `{"publicGroupList":[{"monitorList":[{"id":13,"name":"different model","type":"keyword"}]}]}`,
			wantCode:     "source_invalid",
		},
		{
			name:     "OpenAI requires exact component",
			kind:     ModelSourceOpenAI,
			body:     `{"components":[{"id":"01KMP3KP5MGE23B80K1EK4S8PV","name":"Codex API","status":"operational"}]}`,
			wantCode: "source_invalid",
		},
		{
			name:        "HTML is not accepted as model status",
			kind:        ModelSourceAIInput,
			body:        `<html><body>All systems operational</body></html>`,
			contentType: "text/html",
			wantCode:    "source_unreadable",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contentType := test.contentType
				if contentType == "" {
					contentType = "application/json"
				}
				w.Header().Set("Content-Type", contentType)
				body := test.body
				if r.URL.Path == "/metadata" {
					body = ciiiModelMetadata
					if test.metadataBody != "" {
						body = test.metadataBody
					}
				}
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			collector := New(Config{Timeout: time.Second})
			metadataURL := ""
			if test.kind == ModelSourceCIII {
				metadataURL = server.URL + "/metadata"
			}
			check := collector.probeModelSource(context.Background(), ModelSource{
				ID:          "model-source-test",
				Name:        test.name,
				URL:         server.URL,
				MetadataURL: metadataURL,
				Kind:        test.kind,
			}, now)
			if check.Status != model.Unknown || check.FailureCode != test.wantCode {
				t.Fatalf("expected unknown with %s, got %+v", test.wantCode, check)
			}
		})
	}
}

func TestBuildSnapshotKeepsModelSourcesOutOfNetworkStatus(t *testing.T) {
	t.Parallel()

	networkChecks := []model.Check{
		{ID: "http2-edge", Status: model.Healthy},
		{ID: "quic-edge", Status: model.Healthy},
	}
	modelSourceChecks := []model.Check{
		{ID: "krill-without-required-prefix", Status: model.Critical},
		{ID: "provider-pipio", Status: model.Unknown},
	}
	snapshot := buildSnapshot(time.Now(), time.Now().Add(time.Minute), model.Connector{Status: model.Healthy}, networkChecks, modelSourceChecks)
	if snapshot.Overall != model.Healthy {
		t.Fatalf("expected arbitrary model source IDs not to change Tunnel health, got %s", snapshot.Overall)
	}
	if len(snapshot.Checks) != len(networkChecks)+len(modelSourceChecks) {
		t.Fatalf("expected all checks to remain visible, got %+v", snapshot.Checks)
	}
}

func newJSONServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Path == "/metadata" {
			_, _ = fmt.Fprint(w, ciiiModelMetadata)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
}
