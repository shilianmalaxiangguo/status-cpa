package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/history"
	"github.com/shilianmalaxiangguo/status-cpa/internal/probe"
	"github.com/shilianmalaxiangguo/status-cpa/internal/web"
)

var version = "dev"

type config struct {
	listen     string
	data       string
	interval   time.Duration
	retention  time.Duration
	metricsURL string
	version    bool
}

func main() {
	if err := run(); err != nil {
		slog.Error("status-cpa stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseConfig()
	if err != nil {
		return err
	}
	if cfg.version {
		fmt.Println(version)
		return nil
	}

	store, err := history.New(cfg.data, cfg.retention)
	if err != nil {
		return fmt.Errorf("initialize history: %w", err)
	}
	collector := probe.New(probe.Config{
		MetricsURL: cfg.metricsURL,
		Timeout:    5 * time.Second,
		Endpoints: []probe.Endpoint{
			{ID: "local-api", Name: "CLIProxyAPI 本机", URL: "http://127.0.0.1:8317/", ExpectedStatus: []int{http.StatusOK}, Protocol: "origin"},
			{ID: "local-panel", Name: "CPA Manager Plus 本机", URL: "http://127.0.0.1:18317/management.html", ExpectedStatus: []int{http.StatusOK}, Protocol: "origin"},
			{ID: "public-api", Name: "API 公网入口", URL: "https://api.longxiachaogu.com/", ExpectedStatus: []int{http.StatusOK}, Protocol: "http2"},
			{ID: "public-panel", Name: "面板公网入口", URL: "https://cpa.longxiachaogu.com/management.html", ExpectedStatus: []int{http.StatusFound}, Protocol: "http2"},
		},
		ModelSources: []probe.ModelSource{
			{ID: "provider-ai-input", Name: "AI INPUT", URL: "https://status.input.im/api/status", Kind: probe.ModelSourceAIInput},
			{
				ID:          "provider-ciii",
				Name:        "CIII",
				URL:         "https://status.ciii.club/api/status-page/heartbeat/codex",
				MetadataURL: "https://status.ciii.club/api/status-page/codex",
				Kind:        probe.ModelSourceCIII,
			},
			{ID: "provider-pipio", Name: "PIPIO", URL: "https://pipio.io/api/uptime/status", Kind: probe.ModelSourcePIPIO},
			{ID: "provider-krill", Name: "KRILL", URL: "https://www.krill-ai.net/api/public/channel-status?hours=24", Kind: probe.ModelSourceKrill},
			{
				ID:          "provider-jimu-ai",
				Name:        "JiMu-Ai",
				URL:         "https://status.yiqiu.dev/api/status-page/heartbeat/ai",
				MetadataURL: "https://status.yiqiu.dev/api/status-page/ai",
				Kind:        probe.ModelSourceJiMuAI,
			},
			{ID: "provider-openai-conversations", Name: "OPENAI", URL: "https://status.openai.com/api/v2/components.json", Kind: probe.ModelSourceOpenAI},
		},
	})

	rootCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		collectLoop(rootCtx, store, collector, cfg.interval)
	}()

	webServer, err := web.NewWithStaleAfter(store, 2*cfg.interval+30*time.Second)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           webServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("status-cpa listening", "address", cfg.listen, "version", version)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-rootCtx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		select {
		case <-collectorDone:
			return shutdownErr
		case <-shutdownCtx.Done():
			return errors.Join(shutdownErr, shutdownCtx.Err())
		}
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func collectLoop(ctx context.Context, store *history.Store, collector *probe.Collector, interval time.Duration) {
	collect := func() {
		now := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		snapshot := collector.Collect(probeCtx, now, now.Add(interval))
		if err := store.Append(snapshot); err != nil {
			slog.Error("append probe snapshot", "error", err)
			return
		}
		slog.Info("network probe complete", "overall", snapshot.Overall, "connector_count", snapshot.Connector.Connections)
	}

	collect()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

func parseConfig() (config, error) {
	var cfg config
	flag.StringVar(&cfg.listen, "listen", env("STATUS_CPA_LISTEN", "127.0.0.1:19090"), "HTTP listen address")
	flag.StringVar(&cfg.data, "data", env("STATUS_CPA_DATA", "./data/history.jsonl"), "history JSONL path")
	flag.DurationVar(&cfg.interval, "interval", envDuration("STATUS_CPA_INTERVAL", time.Minute), "probe interval")
	flag.DurationVar(&cfg.retention, "retention", envDuration("STATUS_CPA_RETENTION", 7*24*time.Hour), "history retention")
	flag.StringVar(&cfg.metricsURL, "metrics-url", env("STATUS_CPA_METRICS_URL", "http://127.0.0.1:20241/metrics"), "cloudflared metrics URL")
	flag.BoolVar(&cfg.version, "version", false, "print version and exit")
	flag.Parse()
	if cfg.interval < 10*time.Second {
		return config{}, errors.New("interval must be at least 10s")
	}
	if cfg.retention < time.Hour {
		return config{}, errors.New("retention must be at least 1h")
	}
	if strings.TrimSpace(cfg.listen) == "" || strings.TrimSpace(cfg.data) == "" {
		return config{}, errors.New("listen and data must not be empty")
	}
	return cfg, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
