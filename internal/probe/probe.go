package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

const (
	probeServerName       = "probe.cftunnel.com"
	quicALPN              = "argotunnel"
	edgePort              = 7844
	configurationDiagPath = "/diag/configuration"
	tunnelDiagPath        = "/diag/tunnel"
	maxDiagnosticBody     = 1 << 20
)

var edgeHosts = []string{
	"region1.v2.argotunnel.com",
	"region2.v2.argotunnel.com",
}

type Config struct {
	MetricsURL      string
	Endpoints       []Endpoint
	ModelSources    []ModelSource
	Timeout         time.Duration
	AIInputEmail    string
	AIInputPassword string
	AIInputLoginURL string
}

type Endpoint struct {
	ID             string
	Name           string
	URL            string
	ExpectedStatus []int
	Protocol       string
}

type Collector struct {
	config          Config
	client          *http.Client
	mu              sync.Mutex
	aiInputAuthMu   sync.Mutex
	aiInputToken    string
	aiInputTokenExp time.Time
	aiInputCacheAt  time.Time
	aiInputCacheURL string
	aiInputCache    json.RawMessage
	aiInputCacheErr error
}

func New(config Config) *Collector {
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	transport.Proxy = nil
	return &Collector{
		config: config,
		client: &http.Client{
			Timeout:   config.Timeout,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *Collector) Collect(ctx context.Context, now time.Time, next time.Time) model.Snapshot {
	// quic-go intentionally serializes some global tracing setup. Keeping one
	// collection at a time also prevents overlapping slow probes after outages.
	c.mu.Lock()
	defer c.mu.Unlock()

	connectorCh := make(chan model.Connector, 1)
	quicCh := make(chan model.Check, 1)
	http2Ch := make(chan model.Check, 1)
	endpointCh := make(chan model.Check, len(c.config.Endpoints))
	type modelSourceResult struct {
		index int
		check model.Check
	}
	modelSourceCh := make(chan modelSourceResult, len(c.config.ModelSources))

	go func() { connectorCh <- c.collectConnector(ctx) }()
	go func() { quicCh <- c.probeQUIC(ctx) }()
	go func() { http2Ch <- c.probeHTTP2(ctx) }()
	for _, endpoint := range c.config.Endpoints {
		endpoint := endpoint
		go func() { endpointCh <- c.probeEndpoint(ctx, endpoint) }()
	}
	for index, source := range c.config.ModelSources {
		index, source := index, source
		go func() { modelSourceCh <- modelSourceResult{index: index, check: c.probeModelSource(ctx, source, now)} }()
	}

	connector := <-connectorCh
	networkChecks := []model.Check{<-http2Ch, <-quicCh}
	for range c.config.Endpoints {
		networkChecks = append(networkChecks, <-endpointCh)
	}
	modelSourceChecks := make([]model.Check, len(c.config.ModelSources))
	for range c.config.ModelSources {
		result := <-modelSourceCh
		modelSourceChecks[result.index] = result.check
	}
	return buildSnapshot(now, next, connector, networkChecks, modelSourceChecks)
}

func buildSnapshot(now, next time.Time, connector model.Connector, networkChecks, modelSourceChecks []model.Check) model.Snapshot {
	overall, summary := overallStatus(connector, networkChecks)
	checks := make([]model.Check, 0, len(networkChecks)+len(modelSourceChecks))
	checks = append(checks, networkChecks...)
	checks = append(checks, modelSourceChecks...)
	return model.Snapshot{
		Timestamp:   now.UTC(),
		Overall:     overall,
		Summary:     summary,
		Connector:   connector,
		Checks:      checks,
		NextProbeAt: next.UTC(),
	}
}

func (c *Collector) collectConnector(ctx context.Context) model.Connector {
	modeCh := make(chan string, 1)
	protocolCh := make(chan string, 1)
	go func() { modeCh <- c.collectConfiguredMode(ctx) }()
	go func() { protocolCh <- c.collectActiveProtocol(ctx) }()

	connector := c.collectConnectorMetrics(ctx)
	connector.Mode = <-modeCh
	connector.Protocol = <-protocolCh
	statusProtocol := connector.Protocol
	if statusProtocol == "unknown" && (connector.Mode == "http2" || connector.Mode == "quic") {
		statusProtocol = connector.Mode
	}
	if statusProtocol == "http2" || statusProtocol == "quic" {
		connector.ProtocolStatuses = map[string]model.Status{statusProtocol: connector.Status}
	}
	if connector.Status == model.Healthy {
		if protocol := protocolDisplayName(connector.Protocol); protocol != "" {
			connector.Detail = fmt.Sprintf("%d 条生产 %s connector 在线", connector.Connections, protocol)
		}
	}
	return connector
}

func (c *Collector) collectConnectorMetrics(ctx context.Context) model.Connector {
	connector := model.Connector{
		Mode:     "unknown",
		Protocol: "unknown",
		Status:   model.Unknown,
		Detail:   "尚未读取 cloudflared 指标",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.MetricsURL, nil)
	if err != nil {
		connector.Detail = "cloudflared metrics URL 配置无效"
		return connector
	}
	resp, err := c.client.Do(req)
	if err != nil {
		connector.Detail = "无法读取 cloudflared metrics"
		return connector
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		connector.Detail = fmt.Sprintf("cloudflared metrics 返回 HTTP %d", resp.StatusCode)
		return connector
	}
	connections, ok := parseMetric(resp.Body, "cloudflared_tunnel_ha_connections")
	if !ok {
		connector.Detail = "metrics 中没有 connector 数量"
		return connector
	}
	connector.Connections = int(connections)
	switch {
	case connector.Connections >= 4:
		connector.Status = model.Healthy
		connector.Detail = fmt.Sprintf("%d 条生产 connector 在线", connector.Connections)
	case connector.Connections > 0:
		connector.Status = model.Degraded
		connector.Detail = fmt.Sprintf("仅 %d 条生产 connector 在线", connector.Connections)
	default:
		connector.Status = model.Critical
		connector.Detail = "没有生产 connector 在线"
	}
	return connector
}

func (c *Collector) collectConfiguredMode(ctx context.Context) string {
	payload := struct {
		Protocol string `json:"protocol"`
	}{}
	if !c.collectDiagnostic(ctx, configurationDiagPath, &payload) {
		return "unknown"
	}
	if strings.TrimSpace(payload.Protocol) == "" {
		return "auto"
	}
	return normalizeConfiguredMode(payload.Protocol)
}

func (c *Collector) collectActiveProtocol(ctx context.Context) string {
	payload := struct {
		Connections []struct {
			Connected bool   `json:"isConnected"`
			Protocol  *int64 `json:"protocol"`
		} `json:"connections"`
	}{}
	if !c.collectDiagnostic(ctx, tunnelDiagPath, &payload) {
		return "unknown"
	}

	active := ""
	for _, connection := range payload.Connections {
		if !connection.Connected {
			continue
		}
		protocol := diagnosticProtocol(connection.Protocol)
		if protocol == "unknown" || active != "" && active != protocol {
			return "unknown"
		}
		active = protocol
	}
	if active == "" {
		return "unknown"
	}
	return active
}

func (c *Collector) collectDiagnostic(ctx context.Context, path string, target any) bool {
	endpoint, err := diagnosticURL(c.config.MetricsURL, path)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiagnosticBody+1))
	if err != nil || len(body) > maxDiagnosticBody {
		return false
	}
	return json.Unmarshal(body, target) == nil
}

func diagnosticURL(metricsURL, path string) (string, error) {
	parsed, err := url.Parse(metricsURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("metrics URL must include scheme and host")
	}
	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func normalizeConfiguredMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto":
		return "auto"
	case "quic":
		return "quic"
	case "http2", "h2mux":
		return "http2"
	default:
		return "unknown"
	}
}

func diagnosticProtocol(value *int64) string {
	// HTTP/2 is enum zero in cloudflared and is omitted from diagnostic JSON.
	if value == nil || *value == 0 {
		return "http2"
	}
	if *value == 1 {
		return "quic"
	}
	return "unknown"
}

func protocolDisplayName(protocol string) string {
	switch protocol {
	case "http2":
		return "HTTP/2"
	case "quic":
		return "QUIC"
	default:
		return ""
	}
}

func parseMetric(r io.Reader, name string) (float64, bool) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, name) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != name {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		return value, err == nil
	}
	return 0, false
}

func (c *Collector) probeEndpoint(ctx context.Context, endpoint Endpoint) model.Check {
	check := model.Check{
		ID:       endpoint.ID,
		Name:     endpoint.Name,
		Protocol: endpoint.Protocol,
		Target:   endpoint.URL,
		Status:   model.Unknown,
		Detail:   "尚未探测",
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.URL, nil)
	if err != nil {
		check.Detail = err.Error()
		return check
	}
	req.Header.Set("User-Agent", "status-cpa/1.0")
	resp, err := c.client.Do(req)
	check.LatencyMS = elapsedMS(started)
	if err != nil {
		check.Status = model.Critical
		check.FailureCode = classifyError(err)
		check.Detail = compactError(err)
		return check
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1024)
	if containsInt(endpoint.ExpectedStatus, resp.StatusCode) {
		check.Status = model.Healthy
		check.Detail = fmt.Sprintf("HTTP %d，路径可达", resp.StatusCode)
		return check
	}
	check.Status = model.Critical
	check.FailureCode = "unexpected_http_status"
	check.Detail = fmt.Sprintf("预期 %v，实际 HTTP %d", endpoint.ExpectedStatus, resp.StatusCode)
	return check
}

func (c *Collector) probeHTTP2(ctx context.Context) model.Check {
	check := model.Check{
		ID:       "http2-edge",
		Name:     "HTTP/2 边缘链路",
		Protocol: "http2",
		Target:   "Cloudflare edge:7844/TCP",
		Status:   model.Critical,
		Detail:   "TCP/TLS 握手失败",
	}
	addresses, err := resolveEdges(ctx)
	if err != nil {
		check.Status = model.Unknown
		check.FailureCode = "dns_failed"
		check.Detail = compactError(err)
		return check
	}
	var lastErr error
	for _, address := range randomized(addresses) {
		started := time.Now()
		dialer := &tls.Dialer{
			NetDialer: &net.Dialer{Timeout: c.config.Timeout},
			Config:    edgeTLSConfig(nil),
		}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			check.Status = model.Healthy
			check.LatencyMS = elapsedMS(started)
			check.Target = address
			check.Detail = "TCP + TLS 握手成功"
			_ = conn.Close()
			return check
		}
		lastErr = err
	}
	check.FailureCode = classifyError(lastErr)
	check.Detail = compactError(lastErr)
	return check
}

func (c *Collector) probeQUIC(ctx context.Context) model.Check {
	check := model.Check{
		ID:       "quic-edge",
		Name:     "QUIC 边缘链路",
		Protocol: "quic",
		Target:   "Cloudflare edge:7844/UDP",
		Status:   model.Degraded,
		Detail:   "QUIC 握手失败",
	}
	addresses, err := resolveEdges(ctx)
	if err != nil {
		check.Status = model.Unknown
		check.FailureCode = "dns_failed"
		check.Detail = compactError(err)
		return check
	}
	var lastErr error
	for _, address := range randomized(addresses) {
		addrPort, err := netip.ParseAddrPort(address)
		if err != nil {
			lastErr = err
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
		started := time.Now()
		conn, err := quic.DialAddrEarly(probeCtx, addrPort.String(), edgeTLSConfig([]string{quicALPN}), &quic.Config{
			HandshakeIdleTimeout: c.config.Timeout,
			MaxIdleTimeout:       c.config.Timeout,
		})
		if err == nil {
			select {
			case <-conn.HandshakeComplete():
				check.Status = model.Healthy
				check.LatencyMS = elapsedMS(started)
				check.Target = addrPort.String()
				check.Detail = "真实 QUIC/TLS 握手成功；未注册 connector"
				_ = conn.CloseWithError(0, "status probe complete")
				cancel()
				return check
			case <-probeCtx.Done():
				lastErr = probeCtx.Err()
				_ = conn.CloseWithError(0, "status probe timeout")
			}
		} else {
			lastErr = err
		}
		cancel()
	}
	check.FailureCode = classifyError(lastErr)
	check.Detail = compactError(lastErr)
	return check
}

func edgeTLSConfig(nextProtos []string) *tls.Config {
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	for _, pem := range cloudflareOriginCAPEM {
		roots.AppendCertsFromPEM([]byte(pem))
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: probeServerName,
		NextProtos: nextProtos,
		RootCAs:    roots,
	}
}

func resolveEdges(ctx context.Context) ([]string, error) {
	resolver := net.DefaultResolver
	seen := map[string]struct{}{}
	var addresses []string
	for _, host := range edgeHosts {
		ips, err := resolver.LookupNetIP(ctx, "ip4", host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			address := net.JoinHostPort(ip.String(), strconv.Itoa(edgePort))
			if _, ok := seen[address]; ok {
				continue
			}
			seen[address] = struct{}{}
			addresses = append(addresses, address)
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("Cloudflare Tunnel edge DNS 没有返回 IPv4 地址")
	}
	return addresses, nil
}

func randomized(values []string) []string {
	copyValues := append([]string(nil), values...)
	rand.Shuffle(len(copyValues), func(i, j int) {
		copyValues[i], copyValues[j] = copyValues[j], copyValues[i]
	})
	if len(copyValues) > 4 {
		copyValues = copyValues[:4]
	}
	return copyValues
}

func overallStatus(connector model.Connector, checks []model.Check) (model.Status, string) {
	if connector.Status == model.Critical {
		return model.Critical, "生产 Tunnel 当前没有健康 connector"
	}
	criticalPublicEndpoints := 0
	degraded := connector.Status == model.Degraded || connector.Status == model.Unknown
	for _, check := range checks {
		if check.Status == model.Unknown || check.Status == model.Degraded {
			degraded = true
		}
		if check.Status == model.Critical {
			if strings.HasPrefix(check.ID, "public-") {
				criticalPublicEndpoints++
			} else {
				degraded = true
			}
		}
	}
	if criticalPublicEndpoints >= 2 {
		return model.Critical, "两个公网服务入口均不可达"
	}
	if criticalPublicEndpoints == 1 || degraded {
		return model.Degraded, "生产服务可用，但检测到网络路径降级"
	}
	switch connector.Protocol {
	case "quic":
		return model.Healthy, "QUIC 生产 Tunnel 与 HTTP/2 备用路径均正常"
	case "http2":
		return model.Healthy, "HTTP/2 生产 Tunnel 与 QUIC 备用路径均正常"
	default:
		return model.Healthy, "生产 Tunnel 与两条边缘路径均正常"
	}
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func elapsedMS(started time.Time) float64 {
	return float64(time.Since(started).Microseconds()) / 1000
}

func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}
	var dnsErr *net.DNSError
	var netErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		return "dns_failed"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case strings.Contains(strings.ToLower(err.Error()), "refused"):
		return "connection_refused"
	case strings.Contains(strings.ToLower(err.Error()), "certificate"):
		return "tls_certificate"
	default:
		return "network_error"
	}
}

func compactError(err error) string {
	if err == nil {
		return "未知网络错误"
	}
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 180 {
		message = message[:177] + "..."
	}
	return message
}

var cloudflareOriginCAPEM = []string{`-----BEGIN CERTIFICATE-----
MIICiTCCAi6gAwIBAgIUXZP3MWb8MKwBE1Qbawsp1sfA/Y4wCgYIKoZIzj0EAwIw
gY8xCzAJBgNVBAYTAlVTMRMwEQYDVQQIEwpDYWxpZm9ybmlhMRYwFAYDVQQHEw1T
YW4gRnJhbmNpc2NvMRkwFwYDVQQKExBDbG91ZEZsYXJlLCBJbmMuMTgwNgYDVQQL
Ey9DbG91ZEZsYXJlIE9yaWdpbiBTU0wgRUNDIENlcnRpZmljYXRlIEF1dGhvcml0
eTAeFw0xOTA4MjMyMTA4MDBaFw0yOTA4MTUxNzAwMDBaMIGPMQswCQYDVQQGEwJV
UzETMBEGA1UECBMKQ2FsaWZvcm5pYTEWMBQGA1UEBxMNU2FuIEZyYW5jaXNjbzEZ
MBcGA1UEChMQQ2xvdWRGbGFyZSwgSW5jLjE4MDYGA1UECxMvQ2xvdWRGbGFyZSBP
cmlnaW4gU1NMIEVDQyBDZXJ0aWZpY2F0ZSBBdXRob3JpdHkwWTATBgcqhkjOPQIB
BggqhkjOPQMBBwNCAASR+sGALuaGshnUbcxKry+0LEXZ4NY6JUAtSeA6g87K3jaA
xpIg9G50PokpfWkhbarLfpcZu0UAoYy2su0EhN7wo2YwZDAOBgNVHQ8BAf8EBAMC
AQYwEgYDVR0TAQH/BAgwBgEB/wIBAjAdBgNVHQ4EFgQUhTBdOypw1O3VkmcH/es5
tBoOOKcwHwYDVR0jBBgwFoAUhTBdOypw1O3VkmcH/es5tBoOOKcwCgYIKoZIzj0E
AwIDSQAwRgIhAKilfntP2ILGZjwajktkBtXE1pB4Y/fjAfLkIRUzrI15AiEA5UCL
XYZZ9m2c3fKwIenMMojL1eqydsgqj/wK4p5kagQ=
-----END CERTIFICATE-----`}
