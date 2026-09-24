package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

const aiInputTestLogin = `{"code":0,"data":{"access_token":"test-token","expires_in":3600}}`

func newProviderTestCollector(baseURL string) *Collector {
	return New(Config{Timeout: time.Second, AIInputEmail: "test@example.invalid", AIInputPassword: "test-password", AIInputLoginURL: baseURL + "/login"})
}

func aiInputTestChannel(id int, model, status string, latency float64, at time.Time) string {
	return fmt.Sprintf(`{"id":%d,"provider":"openai","primary_model":%q,"primary_status":%q,"primary_latency_ms":%v,"timeline":[{"status":%q,"checked_at":%q}]}`, id, model, status, latency, status, at.Format(time.RFC3339))
}

func aiInputTestPayload(channels ...string) string {
	return `{"code":0,"data":{"items":[` + strings.Join(channels, ",") + `]}}`
}

func TestAIInputIndependentChannelsAndSession(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var logins, reads atomic.Int32
	body := aiInputTestPayload(
		aiInputTestChannel(1, "gpt-5.6-sol", "degraded", 11211, now),
		aiInputTestChannel(3, "gpt-5.6-sol", "error", 3511, now),
		aiInputTestChannel(2, "gpt-5.6-sol", "operational", 1492, now),
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login" {
			logins.Add(1)
			var credentials struct{ Email, Password string }
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&credentials) != nil || credentials.Email != "test@example.invalid" || credentials.Password != "test-password" {
				t.Error("invalid login request")
			}
			fmt.Fprint(w, aiInputTestLogin)
			return
		}
		reads.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("monitor request missing bearer token")
		}
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	c := newProviderTestCollector(server.URL)
	var wg sync.WaitGroup
	for i, id := range []int64{3, 2, 1} {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			source := ModelSource{ID: fmt.Sprintf("provider-ai-input-channel-%d", id), Kind: ModelSourceAIInput, ChannelID: id, Model: "gpt-5.6-sol", URL: server.URL}
			check := c.probeModelSource(context.Background(), source, now)
			if check.ID != source.ID || check.Status != []model.Status{model.Critical, model.Healthy, model.Degraded}[i] || check.LatencyMS != []float64{3511, 1492, 11211}[i] {
				t.Errorf("channel status or latency crossed IDs: %+v", check)
			}
			encoded, _ := json.Marshal(check)
			for _, secret := range []string{"test-token", "test-password", "test@example.invalid"} {
				if strings.Contains(string(encoded), secret) {
					t.Error("check leaked credentials")
				}
			}
		}(i, id)
	}
	wg.Wait()
	if logins.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("wanted one login and monitor read, got %d/%d", logins.Load(), reads.Load())
	}
	source := ModelSource{Kind: ModelSourceAIInput, ChannelID: 2, Model: "gpt-5.6-sol", URL: server.URL}
	c.probeModelSource(context.Background(), source, now.Add(time.Minute))
	if logins.Load() != 1 || reads.Load() != 2 {
		t.Fatal("next collection must reuse token but refetch monitor data")
	}
	c.aiInputTokenExp = time.Now().Add(-time.Second)
	c.probeModelSource(context.Background(), source, now.Add(2*time.Minute))
	if logins.Load() != 2 || reads.Load() != 3 {
		t.Fatal("expired token must be replaced")
	}
}

func TestAIInputRejectsInvalidChannelData(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	valid := aiInputTestChannel(3, "gpt-5.6-sol", "operational", 1200, now)
	for _, test := range []struct{ name, body, code string }{
		{"missing channel", aiInputTestPayload(aiInputTestChannel(2, "gpt-5.6-sol", "operational", 100, now)), "source_invalid"},
		{"duplicate id", aiInputTestPayload(valid, valid), "source_invalid"},
		{"wrong model", aiInputTestPayload(strings.ReplaceAll(valid, "gpt-5.6-sol", "gpt-6-astra")), "source_invalid"},
		{"wrong provider", aiInputTestPayload(strings.ReplaceAll(valid, "openai", "other")), "source_invalid"},
		{"stale", aiInputTestPayload(aiInputTestChannel(3, "gpt-5.6-sol", "operational", 1200, now.Add(-4*time.Minute))), "source_stale"},
		{"future", aiInputTestPayload(aiInputTestChannel(3, "gpt-5.6-sol", "operational", 1200, now.Add(2*time.Minute))), "source_stale"},
		{"invalid timestamp", aiInputTestPayload(strings.ReplaceAll(valid, now.Format(time.RFC3339), "invalid")), "source_invalid"},
		{"unknown state", aiInputTestPayload(strings.ReplaceAll(valid, "operational", "pending")), "source_invalid"},
		{"conflicting current", aiInputTestPayload(strings.Replace(valid, `"primary_status":"operational"`, `"primary_status":"error"`, 1)), "source_invalid"},
		{"conflicting latest", aiInputTestPayload(strings.Replace(valid, `"timeline":[`, fmt.Sprintf(`"timeline":[{"status":"error","checked_at":%q},`, now.Format(time.RFC3339)), 1)), "source_invalid"},
		{"empty history", aiInputTestPayload(`{"id":3,"provider":"openai","primary_model":"gpt-5.6-sol","primary_status":"operational","timeline":[]}`), "source_invalid"},
		{"missing success code", strings.Replace(aiInputTestPayload(valid), `"code":0,`, "", 1), "source_invalid"},
		{"error code", strings.Replace(aiInputTestPayload(valid), `"code":0`, `"code":1`, 1), "source_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newJSONServer(test.body, "")
			defer s.Close()
			c := newProviderTestCollector(s.URL)
			check := c.probeModelSource(context.Background(), ModelSource{Kind: ModelSourceAIInput, ChannelID: 3, Model: "gpt-5.6-sol", URL: s.URL}, now)
			if check.Status != model.Unknown || check.FailureCode != test.code || check.LatencyMS != 0 {
				t.Fatalf("invalid channel accepted: %+v", check)
			}
		})
	}
}

func TestAIInputAuthenticationFailures(t *testing.T) {
	for _, mode := range []string{"missing", "rejected", "missing token", "missing expiry", "missing success code", "expired once", "always unauthorized", "forbidden", "HTML", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var logins, reads, redirects int
			now := time.Now().UTC().Truncate(time.Second)
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirects++; w.WriteHeader(200) }))
			defer redirect.Close()
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/login" {
					logins++
					body := aiInputTestLogin
					switch mode {
					case "rejected":
						w.WriteHeader(401)
						fmt.Fprint(w, `{"message":"test-password"}`)
						return
					case "missing token":
						body = `{"code":0,"data":{"expires_in":3600}}`
					case "missing expiry":
						body = `{"code":0,"data":{"access_token":"test-token"}}`
					case "missing success code":
						body = `{"data":{"access_token":"test-token","expires_in":3600}}`
					case "redirect":
						w.Header().Set("Location", redirect.URL)
						w.WriteHeader(302)
						return
					}
					fmt.Fprint(w, body)
					return
				}
				reads++
				if mode == "always unauthorized" || mode == "expired once" && reads == 1 {
					w.WriteHeader(401)
					return
				}
				if mode == "forbidden" {
					w.WriteHeader(403)
					return
				}
				if mode == "HTML" {
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprint(w, "login page")
					return
				}
				fmt.Fprint(w, aiInputTestPayload(aiInputTestChannel(3, "gpt-5.6-sol", "operational", 10, now)))
			}))
			defer s.Close()
			c := newProviderTestCollector(s.URL)
			if mode == "missing" {
				c.config.AIInputPassword = ""
			}
			for i := 0; i < 3; i++ {
				check := c.probeModelSource(context.Background(), ModelSource{Kind: ModelSourceAIInput, ChannelID: 3, Model: "gpt-5.6-sol", URL: s.URL}, now)
				want := model.Unknown
				if mode == "expired once" {
					want = model.Healthy
				}
				if check.Status != want || strings.Contains(check.Detail, "test-password") || strings.Contains(check.Detail, "test-token") {
					t.Fatalf("unexpected auth result: %+v", check)
				}
			}
			wantLogins, wantReads := 1, 0
			switch mode {
			case "missing":
				wantLogins = 0
			case "expired once", "always unauthorized":
				wantLogins, wantReads = 2, 2
			case "forbidden", "HTML":
				wantReads = 1
			}
			if logins != wantLogins || reads != wantReads || redirects != 0 {
				t.Fatalf("unbounded retry or redirect: logins=%d reads=%d redirects=%d", logins, reads, redirects)
			}
		})
	}
}
