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

const (
	ciiiModelMetadata   = `{"publicGroupList":[{"monitorList":[{"id":13,"name":"Ciii-codex gpt-5.6-sol","type":"keyword"}]}]}`
	jimuAIModelMetadata = `{"publicGroupList":[{"monitorList":[{"id":6,"name":"gpt-5.5","type":"http"}]}]}`
)

func TestProbeModelSources(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 5, 10, 0, 0, time.UTC)
	tests := []struct {
		name         string
		kind         ModelSourceKind
		body         string
		metadataBody string
		wantStatus   model.Status
		wantLatency  float64
		wantCode     string
		wantDetail   string
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
			name:         "JiMu-Ai exact model health and latency",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":1,"time":"2026-08-04 05:09:30.123","msg":"","ping":42}]}}`,
			metadataBody: jimuAIModelMetadata,
			wantStatus:   model.Healthy,
			wantLatency:  42,
			wantDetail:   "gpt-5.5 最近探测正常",
		},
		{
			name:         "JiMu-Ai exact model outage",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":0,"time":"2026-08-04 05:09:30.123","msg":"timeout","ping":null}]}}`,
			metadataBody: jimuAIModelMetadata,
			wantStatus:   model.Critical,
			wantCode:     "reported_outage",
			wantDetail:   "gpt-5.5 最近探测失败",
		},
		{
			name:         "JiMu-Ai pending model heartbeat",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":2,"time":"2026-08-04 05:09:30.123","msg":"","ping":null}]}}`,
			metadataBody: jimuAIModelMetadata,
			wantStatus:   model.Degraded,
			wantCode:     "reported_degradation",
			wantDetail:   "gpt-5.5 最近探测确认中",
		},
		{
			name:         "JiMu-Ai model maintenance",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":3,"time":"2026-08-04 05:09:30.123","msg":"","ping":null}]}}`,
			metadataBody: jimuAIModelMetadata,
			wantStatus:   model.Degraded,
			wantCode:     "reported_degradation",
			wantDetail:   "gpt-5.5 最近探测维护中",
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
			name:       "OpenAI Conversations aggregate health",
			kind:       ModelSourceOpenAI,
			body:       `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"operational"}]}`,
			wantStatus: model.Healthy,
			wantDetail: "Conversations 官方聚合状态正常",
		},
		{
			name:       "OpenAI Conversations degraded performance",
			kind:       ModelSourceOpenAI,
			body:       `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"degraded_performance"}]}`,
			wantStatus: model.Degraded,
			wantCode:   "reported_degradation",
			wantDetail: "Conversations 官方聚合状态降级",
		},
		{
			name:       "OpenAI Conversations partial outage",
			kind:       ModelSourceOpenAI,
			body:       `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"partial_outage"}]}`,
			wantStatus: model.Degraded,
			wantCode:   "reported_degradation",
			wantDetail: "Conversations 官方聚合状态降级",
		},
		{
			name:       "OpenAI Conversations maintenance",
			kind:       ModelSourceOpenAI,
			body:       `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"under_maintenance"}]}`,
			wantStatus: model.Degraded,
			wantCode:   "reported_degradation",
			wantDetail: "Conversations 官方聚合状态降级",
		},
		{
			name:       "OpenAI Conversations full outage",
			kind:       ModelSourceOpenAI,
			body:       `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"full_outage"}]}`,
			wantStatus: model.Critical,
			wantCode:   "reported_outage",
			wantDetail: "Conversations 官方聚合状态中断",
		},
		{
			name:       "OpenAI Conversations legacy major outage",
			kind:       ModelSourceOpenAI,
			body:       `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"major_outage"}]}`,
			wantStatus: model.Critical,
			wantCode:   "reported_outage",
			wantDetail: "Conversations 官方聚合状态中断",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := newJSONServer(test.body, test.metadataBody)
			defer server.Close()
			collector := New(Config{Timeout: time.Second})
			metadataURL := ""
			if test.kind == ModelSourceCIII || test.kind == ModelSourceJiMuAI {
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

func TestThreeModelsRemainIndependent(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 6, 30, 0, 0, time.UTC)
	models := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra"}
	tests := []struct {
		name      string
		kind      ModelSourceKind
		body      string
		statuses  []model.Status
		latencies []float64
	}{
		{
			name: "INPUT",
			kind: ModelSourceAIInput,
			body: fmt.Sprintf(`{"generated_at":%d,"services":[
				{"model":"gpt-5.6-terra","last":{"ts":%d,"ok":true,"latency_ms":303}},
				{"model":"gpt-6-astra","last":{"ts":%d,"ok":false,"latency_ms":null}},
				{"model":"gpt-5.6-sol","last":{"ts":%d,"ok":true,"latency_ms":202}},
				{"model":"gpt-6-sol","last":{"ts":%d,"ok":false}}
			]}`, now.Unix(), now.Unix(), now.Unix(), now.Unix(), now.Unix()),
			statuses:  []model.Status{model.Critical, model.Healthy, model.Healthy},
			latencies: []float64{0, 202, 303},
		},
		{
			name: "PIPIO",
			kind: ModelSourcePIPIO,
			body: `{"success":true,"data":[{"monitors":[
				{"name":"gpt-5.6-terra","status":0,"heartbeats":[1,0]},
				{"name":"gpt-6-astra","status":1,"heartbeats":[0,1]},
				{"name":"gpt-5.6-sol","status":2,"heartbeats":[1,2]},
				{"name":"gpt-6-sol","status":0,"heartbeats":[0]}
			]}]}`,
			statuses:  []model.Status{model.Healthy, model.Degraded, model.Critical},
			latencies: []float64{0, 0, 0},
		},
		{
			name: "KRILL",
			kind: ModelSourceKrill,
			body: `{"success":true,"code":0,"data":{"channels":[
				{"channel_key":"openai_gpt_5_6_terra","model_name":"gpt-5.6-terra","current_status":1,"history":[{"s":1,"ts":"2026-09-23 06:29:00"},{"s":0,"ts":"2026-09-23 06:28:00"}]},
				{"channel_key":"openai_gpt_6_astra","model_name":"gpt-6-astra","current_status":2,"history":[{"s":2,"ts":"2026-09-23 06:29:00"}]},
				{"channel_key":"openai_gpt_5_6_sol","model_name":"gpt-5.6-sol","current_status":0,"history":[{"s":0,"ts":"2026-09-23 06:29:00"}]},
				{"channel_key":"openai_gpt_6_sol","model_name":"gpt-6-sol","current_status":1,"history":[{"s":1,"ts":"2026-09-23 06:29:00"}]}
			],"perf":[
				{"channel_key":"openai_gpt_5_6_sol","ttft_p99_ms":222},
				{"channel_key":"openai_gpt_5_6_terra","ttft_p99_ms":333},
				{"channel_key":"openai_gpt_6_astra","ttft_p99_ms":111}
			]}}`,
			statuses:  []model.Status{model.Degraded, model.Critical, model.Healthy},
			latencies: []float64{111, 222, 333},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for i, target := range models {
				t.Run(target, func(t *testing.T) {
					server := newJSONServer(test.body, "")
					defer server.Close()
					collector := New(Config{Timeout: time.Second})
					check := collector.probeModelSource(context.Background(), ModelSource{
						ID: "test-" + target, Name: test.name, Model: target, URL: server.URL, Kind: test.kind,
					}, now)
					if check.Status != test.statuses[i] || check.LatencyMS != test.latencies[i] || !strings.Contains(check.Detail, target) || check.ID != "test-"+target {
						t.Fatalf("model identity, status or latency leaked: %+v", check)
					}

					// Other healthy models must not substitute for a missing target.
					missing := newJSONServer(strings.ReplaceAll(test.body, target, "different-model"), "")
					defer missing.Close()
					check = collector.probeModelSource(context.Background(), ModelSource{
						Model: target, URL: missing.URL, Kind: test.kind,
					}, now)
					if check.Status != model.Unknown || check.FailureCode != "source_invalid" {
						t.Fatalf("missing %s must remain unknown: %+v", target, check)
					}
				})
			}
		})
	}
}

func TestProbeJiMuAIDiscoversMonitorID(t *testing.T) {
	t.Parallel()

	server := newJSONServer(
		`{"heartbeatList":{"6":[{"status":0,"time":"2026-08-04 05:09:30.000","msg":"wrong monitor","ping":null}],"42":[{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":31}]}}`,
		`{"publicGroupList":[{"monitorList":[{"id":6,"name":"gpt-5.6-sol","type":"http"},{"id":42,"name":"gpt-5.5","type":"http"}]}]}`,
	)
	defer server.Close()

	collector := New(Config{Timeout: time.Second})
	check := collector.probeModelSource(context.Background(), ModelSource{
		ID:          "provider-jimu-ai",
		Name:        "JiMu-Ai",
		URL:         server.URL,
		MetadataURL: server.URL + "/metadata",
		Kind:        ModelSourceJiMuAI,
	}, time.Date(2026, 8, 4, 5, 10, 0, 0, time.UTC))
	if check.Status != model.Healthy || check.LatencyMS != 31 || check.Detail != "gpt-5.5 最近探测正常" {
		t.Fatalf("expected dynamically discovered gpt-5.5 monitor, got %+v", check)
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
		wantDetail   string
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
			name:         "JiMu-Ai rejects metadata without target probe",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":3}]}}`,
			metadataBody: `{"publicGroupList":[{"monitorList":[{"id":21,"name":"gpt-5.6-sol","type":"http"},{"id":8,"name":"claude-opus-4-6","type":"http"}]}]}`,
			wantCode:     "source_invalid",
			wantDetail:   "尚未公开 gpt-5.5 探针",
		},
		{
			name:         "JiMu-Ai rejects duplicate target probes",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":3}]}}`,
			metadataBody: `{"publicGroupList":[{"monitorList":[{"id":6,"name":"gpt-5.5","type":"http"},{"id":7,"name":"gpt-5.5","type":"http"}]}]}`,
			wantCode:     "source_invalid",
			wantDetail:   "多个 gpt-5.5 探针",
		},
		{
			name:         "JiMu-Ai rejects target ID shared with another monitor",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":3}]}}`,
			metadataBody: `{"publicGroupList":[{"monitorList":[{"id":6,"name":"gpt-5.5","type":"http"},{"id":6,"name":"gpt-5.6-sol","type":"http"}]}]}`,
			wantCode:     "source_invalid",
			wantDetail:   "探针 ID 绑定不唯一",
		},
		{
			name:         "JiMu-Ai rejects non-HTTP target probe",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":3}]}}`,
			metadataBody: `{"publicGroupList":[{"monitorList":[{"id":6,"name":"gpt-5.5","type":"keyword"}]}]}`,
			wantCode:     "source_invalid",
			wantDetail:   "探针类型不是 HTTP",
		},
		{
			name:         "JiMu-Ai rejects conflicting latest heartbeat content",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":3},{"status":1,"time":"2026-08-04 05:09:30.000","msg":"","ping":4}]}}`,
			metadataBody: jimuAIModelMetadata,
			wantCode:     "source_invalid",
			wantDetail:   "心跳内容相互冲突",
		},
		{
			name:         "JiMu-Ai stale model heartbeat",
			kind:         ModelSourceJiMuAI,
			body:         `{"heartbeatList":{"6":[{"status":1,"time":"2026-08-04 05:02:59.000","msg":"","ping":3}]}}`,
			metadataBody: jimuAIModelMetadata,
			wantCode:     "source_stale",
			wantDetail:   "超过 7 分钟未更新",
		},
		{
			name:     "OpenAI rejects former Responses component",
			kind:     ModelSourceOpenAI,
			body:     `{"components":[{"id":"01JP8CD9JR3HR6Y7G4Q75N4DVW","name":"Responses","status":"operational"}]}`,
			wantCode: "source_invalid",
		},
		{
			name:     "OpenAI rejects matching name with wrong ID",
			kind:     ModelSourceOpenAI,
			body:     `{"components":[{"id":"01KMP3KP5MGE23B80K1EK4S8PV","name":"Conversations","status":"operational"}]}`,
			wantCode: "source_invalid",
		},
		{
			name:     "OpenAI rejects matching ID with wrong name",
			kind:     ModelSourceOpenAI,
			body:     `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Responses","status":"operational"}]}`,
			wantCode: "source_invalid",
		},
		{
			name:     "OpenAI rejects duplicate Conversations component",
			kind:     ModelSourceOpenAI,
			body:     `{"components":[{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"operational"},{"id":"01JMXBNJXGV1T5GT2M9XA83XNG","name":"Conversations","status":"operational"}]}`,
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
					if test.kind == ModelSourceJiMuAI {
						body = jimuAIModelMetadata
					}
					if test.metadataBody != "" {
						body = test.metadataBody
					}
				}
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()
			collector := New(Config{Timeout: time.Second})
			metadataURL := ""
			if test.kind == ModelSourceCIII || test.kind == ModelSourceJiMuAI {
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
			if test.wantDetail != "" && !strings.Contains(check.Detail, test.wantDetail) {
				t.Fatalf("expected detail containing %q, got %q", test.wantDetail, check.Detail)
			}
		})
	}
}

func TestProbeJiMuAITruncatesHeartbeatMessage(t *testing.T) {
	t.Parallel()

	message := strings.Repeat("x", 2000)
	body := fmt.Sprintf(`{"heartbeatList":{"6":[{"status":0,"time":"2026-08-04 05:09:30.000","msg":"%s","ping":null}]}}`, message)
	server := newJSONServer(body, jimuAIModelMetadata)
	defer server.Close()
	collector := New(Config{Timeout: time.Second})
	check := collector.probeModelSource(context.Background(), ModelSource{
		ID:          "provider-jimu-ai",
		Name:        "JiMu-Ai",
		URL:         server.URL,
		MetadataURL: server.URL + "/metadata",
		Kind:        ModelSourceJiMuAI,
	}, time.Date(2026, 8, 4, 5, 10, 0, 0, time.UTC))
	if check.Status != model.Critical || check.FailureCode != "reported_outage" {
		t.Fatalf("expected reported outage, got %+v", check)
	}
	if strings.Count(check.Detail, "x") != maxModelSourceMessageRunes || !strings.HasSuffix(check.Detail, "...") {
		t.Fatalf("expected bounded heartbeat message, got %d bytes: %q", len(check.Detail), check.Detail)
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

func newJSONServer(body, metadataBody string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.URL.Path == "/metadata" {
			if metadataBody == "" {
				metadataBody = ciiiModelMetadata
			}
			_, _ = fmt.Fprint(w, metadataBody)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
}
