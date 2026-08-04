(() => {
  const state = {
    requestID: 0,
    controller: null,
    timer: null,
    data: null,
  };

  const labels = {
    healthy: "正常",
    degraded: "降级",
    critical: "中断",
    unknown: "未知",
  };

  const protocolLabels = {
    http2: "HTTP/2",
    quic: "QUIC",
    unknown: "未知",
  };

  const providers = [
    { id: "provider-ai-input", label: "AI INPUT" },
    { id: "provider-pipio", label: "PIPIO", noLatency: "未提供" },
    { id: "provider-krill", label: "KRILL" },
    { id: "provider-openai", label: "OPENAI Codex API", noLatency: "不适用" },
  ];

  const demo = new URLSearchParams(window.location.search).get("demo");

  const $ = (id) => document.getElementById(id);

  function formatTime(iso) {
    if (!iso) return "--";
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "--";
    return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  function formatLatency(value) {
    if (!Number.isFinite(value) || value <= 0) return "--";
    return `${Math.round(value)}`;
  }

  function normalizeStatus(status) {
    return Object.prototype.hasOwnProperty.call(labels, status) ? status : "unknown";
  }

  function statusClass(status) {
    return `status-${normalizeStatus(status)}`;
  }

  function setStatusClass(element, status) {
    ["healthy", "degraded", "critical", "unknown"].forEach((value) => element.classList.remove(`status-${value}`));
    element.classList.add(statusClass(normalizeStatus(status)));
  }

  function currentCheck(checks, id) {
    return (checks || []).find((check) => check.id === id) || {
      id,
      status: "unknown",
      detail: "尚无数据",
    };
  }

  function connectorMode(connector) {
    const mode = connector && connector.mode;
    return ["auto", "http2", "quic"].includes(mode) ? mode : "unknown";
  }

  function activeConnectorProtocol(connector) {
    if (!connector || connector.mode === undefined || connector.mode === "") return "unknown";
    return ["http2", "quic"].includes(connector.protocol) ? connector.protocol : "unknown";
  }

  function productionConnectorProtocol(connector) {
    const active = activeConnectorProtocol(connector);
    if (active !== "unknown") return active;
    const mode = connectorMode(connector);
    return ["http2", "quic"].includes(mode) ? mode : "unknown";
  }

  function protocolKey(id) {
    return id === "quic-edge" ? "quic" : "http2";
  }

  function protocolStatus(snapshot, id) {
    const connector = snapshot && snapshot.connector;
    const protocol = protocolKey(id);
    const storedStatus = connector && connector.protocolStatuses && connector.protocolStatuses[protocol];
    if (["healthy", "degraded", "critical", "unknown"].includes(storedStatus)) {
      return storedStatus;
    }
    if (connector && productionConnectorProtocol(connector) === protocol && connector.status) {
      return normalizeStatus(connector.status);
    }
    return normalizeStatus(currentCheck(snapshot && snapshot.checks, id).status);
  }

  function uptime(history, id) {
    const values = (history || []).map((snapshot) => protocolStatus(snapshot, id)).filter((status) => status && status !== "unknown");
    if (!values.length) return "--";
    const good = values.filter((value) => value === "healthy").length;
    return `${((good / values.length) * 100).toFixed(2)}%`;
  }

  function checkStatus(snapshot, id) {
    return normalizeStatus(currentCheck(snapshot && snapshot.checks, id).status);
  }

  function checkUptime(history, id) {
    const values = (history || []).map((snapshot) => checkStatus(snapshot, id)).filter((status) => status !== "unknown");
    if (!values.length) return "--";
    const good = values.filter((value) => value === "healthy").length;
    return `${((good / values.length) * 100).toFixed(2)}%`;
  }

  function renderTunnelState(connector) {
    const mode = connectorMode(connector);
    const active = activeConnectorProtocol(connector);
    const status = normalizeStatus(connector && connector.status);
    let text = "当前隧道 · 未知";
    if (mode === "auto") {
      text = `自动选择 · 当前 ${protocolLabels[active]}`;
    } else if (active !== "unknown" && mode !== "unknown" && active !== mode) {
      text = `配置 ${protocolLabels[mode]} · 当前 ${protocolLabels[active]}`;
    } else if (active !== "unknown") {
      text = `当前隧道 · ${protocolLabels[active]}`;
    } else if (mode !== "unknown") {
      text = `已配置 ${protocolLabels[mode]} · 当前未知`;
    }
    $("tunnel-state-label").textContent = `${text} · ${labels[status]}`;
    setStatusClass($("tunnel-state"), status);
  }

  function renderProtocolRole(protocol, connector) {
    const row = $(`${protocol}-row`);
    const role = $(`${protocol}-role`);
    const mode = connectorMode(connector);
    const active = activeConnectorProtocol(connector);
    const isActive = active === protocol;
    row.classList.toggle("is-active", isActive);
    if (isActive) {
      row.setAttribute("aria-current", "true");
    } else {
      row.removeAttribute("aria-current");
    }
    role.classList.remove("role-standby", "role-unknown");
    if (isActive) {
      role.textContent = "当前生产隧道";
    } else if (mode === protocol) {
      role.textContent = active === "unknown" ? "已配置生产隧道" : "已配置但当前未使用";
      role.classList.add("role-unknown");
    } else if (active !== "unknown") {
      role.textContent = "备用路径探针";
      role.classList.add("role-standby");
    } else if (mode === "auto") {
      role.textContent = "自动候选路径";
      role.classList.add("role-unknown");
    } else {
      role.textContent = "路径探针";
      role.classList.add("role-unknown");
    }
  }

  function render(snapshot, history, incidents, stale = false, hasSnapshot = true, alertMessage = "") {
    $("last-updated").textContent = hasSnapshot ? `更新于 ${formatTime(snapshot.timestamp)}` : "等待第一次探测";
    const alert = $("data-stale");
    alert.hidden = !alertMessage && !stale && hasSnapshot;
    alert.textContent = alertMessage || (hasSnapshot ? "最新探测数据已过期，当前状态已标记为未知。" : "等待第一次网络探测，当前暂无状态数据。");

    const http2 = currentCheck(snapshot.checks, "http2-edge");
    const quic = currentCheck(snapshot.checks, "quic-edge");
    const connector = snapshot.connector || { mode: "unknown", protocol: "unknown", status: "unknown", detail: "尚无 connector 数据" };
    const production = productionConnectorProtocol(connector);
    renderTunnelState(connector);
    renderProtocolRole("http2", connector);
    renderProtocolRole("quic", connector);
    $("http2-row-latency").textContent = http2.latencyMs ? `${formatLatency(http2.latencyMs)} ms` : "--";
    $("quic-row-latency").textContent = quic.latencyMs ? `${formatLatency(quic.latencyMs)} ms` : "--";
    $("http2-detail").textContent = production === "http2" ? connector.detail : http2.detail;
    $("quic-detail").textContent = production === "quic" ? connector.detail : quic.detail;
    $("http2-uptime").textContent = uptime(history, "http2-edge");
    $("quic-uptime").textContent = uptime(history, "quic-edge");
    const http2Status = protocolStatus(snapshot, "http2-edge");
    const quicStatus = protocolStatus(snapshot, "quic-edge");
    renderRowStatus($("http2-status"), http2Status);
    renderRowStatus($("quic-status"), quicStatus);
    renderTrack($("http2-track"), history, "http2-edge", http2Status);
    renderTrack($("quic-track"), history, "quic-edge", quicStatus);

    renderProviders(snapshot, history);

    const serviceChecks = (snapshot.checks || []).filter((check) => check.id.startsWith("local-") || check.id.startsWith("public-"));
    $("service-table").innerHTML = serviceChecks.length ? serviceChecks.map(renderService).join("") : '<div class="service-loading">暂无服务路径数据</div>';
    $("incidents-list").innerHTML = incidents && incidents.length ? incidents.map(renderIncident).join("") : '<div class="empty-state"><span class="empty-check" aria-hidden="true">&#10003;</span><p>当前时间范围内没有异常记录</p></div>';
    $("incident-count").textContent = `${incidents ? incidents.length : 0} 条`;
  }

  function renderProviders(snapshot, history) {
    const summary = [];
    providers.forEach((provider) => {
      const check = currentCheck(snapshot && snapshot.checks, provider.id);
      const status = normalizeStatus(check.status);
      $(`${provider.id}-detail`).textContent = check.detail || "尚无模型状态数据";
      $(`${provider.id}-latency`).textContent = check.latencyMs ? `${formatLatency(check.latencyMs)} ms` : status !== "unknown" && provider.noLatency ? provider.noLatency : "--";
      $(`${provider.id}-uptime`).textContent = checkUptime(history, provider.id);
      renderRowStatus($(`${provider.id}-status`), status);
      renderCheckTrack($(`${provider.id}-track`), history, provider.id, provider.label, status);
      summary.push(`${provider.label} ${labels[status]}`);
    });
    const liveStatus = $("provider-live-status");
    const nextSummary = `上游健康度：${summary.join("，")}`;
    if (liveStatus.textContent !== nextSummary) liveStatus.textContent = nextSummary;
  }

  function renderRowStatus(element, status) {
    status = normalizeStatus(status);
    setStatusClass(element, status);
    element.innerHTML = `<span></span>${labels[status]}`;
  }

  function renderTrack(element, history, id, currentStatus) {
    const label = id === "quic-edge" ? "QUIC" : "HTTP/2";
    renderHistoryTrack(element, history, currentStatus, label, (snapshot) => protocolStatus(snapshot, id));
  }

  function renderCheckTrack(element, history, id, label, currentStatus) {
    renderHistoryTrack(element, history, currentStatus, label, (snapshot) => checkStatus(snapshot, id));
  }

  function renderHistoryTrack(element, history, currentStatus, label, statusFor) {
    const count = 60;
    const samples = (history || []).slice(-count);
    element.style.setProperty("--segments", String(count));
    const padded = Array.from({ length: count - samples.length }, () => null).concat(samples);
    element.innerHTML = padded.map((snapshot) => {
      const status = snapshot ? normalizeStatus(statusFor(snapshot)) : "unknown";
      return `<span class="track-segment ${status}" title="${snapshot ? `${formatTime(snapshot.timestamp)} · ${labels[status]}` : "暂无数据"}"></span>`;
    }).join("");
    element.setAttribute("aria-label", `${label} 历史状态，最新为 ${labels[normalizeStatus(currentStatus)]}`);
  }

  function renderService(check) {
    const status = normalizeStatus(check.status);
    return `<div class="service-row"><div class="service-main"><span class="service-dot ${statusClass(status)}" aria-hidden="true"></span><div><strong>${escapeHTML(check.name)}</strong><span>${escapeHTML(check.detail || check.target || "")}</span></div></div><div class="service-metric"><span>响应</span><strong>${check.latencyMs ? `${formatLatency(check.latencyMs)} ms` : "--"}</strong></div><div class="service-state ${statusClass(status)}">${labels[status]}</div></div>`;
  }

  function renderIncident(incident) {
    const date = formatTime(incident.startedAt);
    const state = incident.open ? "进行中" : `已恢复 · ${formatTime(incident.resolvedAt)}`;
    return `<article class="incident-row"><time class="incident-time" datetime="${escapeHTML(incident.startedAt)}">${date}</time><div class="incident-copy"><strong>${escapeHTML(incident.title)}</strong><span>${escapeHTML(incident.detail || "")}</span></div><span class="incident-state ${incident.open ? "is-open" : ""}">${state}</span></article>`;
  }

  function escapeHTML(value) {
    return String(value == null ? "" : value).replace(/[&<>'"]/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" }[character]));
  }

  function demoData(kind) {
    const now = new Date().toISOString();
    const historyStep = 1;
    const quicStatus = kind === "healthy" ? "healthy" : kind === "critical" ? "critical" : "degraded";
    const overall = kind === "healthy" ? "healthy" : kind === "critical" ? "critical" : "degraded";
    const aiInputStatus = kind === "critical" ? "critical" : "healthy";
    const krillStatus = kind === "degraded" ? "degraded" : "healthy";
    const mode = kind === "healthy" ? "auto" : kind === "critical" ? "quic" : "http2";
    const protocol = kind === "healthy" ? "quic" : kind === "critical" ? "unknown" : "http2";
    const connectorStatus = kind === "critical" ? "critical" : "healthy";
    const connector = {
      mode,
      protocol,
      connections: kind === "critical" ? 0 : 4,
      status: connectorStatus,
      detail: kind === "critical" ? "没有生产 connector 在线" : `4 条生产 ${protocolLabels[protocol]} connector 在线`,
    };
    const checks = [
      { id: "http2-edge", name: "HTTP/2 边缘链路", protocol: "http2", status: "healthy", latencyMs: 176, detail: "TCP + TLS 握手成功", target: "198.41.192.167:7844" },
      { id: "quic-edge", name: "QUIC 边缘链路", protocol: "quic", status: quicStatus, latencyMs: quicStatus === "healthy" ? 181 : 0, detail: quicStatus === "healthy" ? "真实 QUIC/TLS 握手成功；未注册 connector" : "timeout: no recent network activity", target: "198.41.192.167:7844" },
      { id: "local-api", name: "CLIProxyAPI 本机", protocol: "origin", status: "healthy", latencyMs: 1, detail: "HTTP 200，路径可达" },
      { id: "local-panel", name: "CPA Manager Plus 本机", protocol: "origin", status: "healthy", latencyMs: 2, detail: "HTTP 200，路径可达" },
      { id: "public-api", name: "API 公网入口", protocol: "http2", status: kind === "critical" ? "critical" : "healthy", latencyMs: 780, detail: kind === "critical" ? "timeout" : "HTTP 200，路径可达" },
      { id: "public-panel", name: "面板公网入口", protocol: "http2", status: kind === "critical" ? "critical" : "healthy", latencyMs: 590, detail: kind === "critical" ? "HTTP 1033" : "HTTP 302，Access 保护生效" },
      { id: "provider-ai-input", name: "AI INPUT", protocol: "model", status: aiInputStatus, latencyMs: aiInputStatus === "healthy" ? 2820 : 0, detail: aiInputStatus === "healthy" ? "gpt-5.6-sol 最近探测正常" : "gpt-5.6-sol 最近探测失败" },
      { id: "provider-pipio", name: "PIPIO", protocol: "model", status: "healthy", latencyMs: 0, detail: "gpt-5.6-sol 发布状态正常；未提供模型延迟和更新时间" },
      { id: "provider-krill", name: "KRILL", protocol: "model", status: krillStatus, latencyMs: 447, detail: `gpt-5.6-sol 发布状态${krillStatus === "healthy" ? "正常" : "降级"}；延迟为 TTFT P99` },
      { id: "provider-openai", name: "OPENAI", protocol: "model", status: "healthy", latencyMs: 0, detail: "Codex API 官方聚合状态正常；非 gpt-5.6-sol 单模型探测" },
    ];
    const summary = kind === "healthy" ? "自动模式当前选择 QUIC，HTTP/2 备用路径正常" : kind === "critical" ? "公网入口无法找到健康 Tunnel connector" : "HTTP/2 生产隧道正常，QUIC 备用路径出现降级";
    const history = Array.from({ length: 60 }, (_, index) => {
      const affected = index > 55;
      const connectorFailed = affected && kind === "critical";
      return {
        timestamp: new Date(Date.now() - (59 - index) * historyStep * 60 * 1000).toISOString(),
        overall: affected && kind !== "healthy" ? overall : "healthy",
        connector: {
          ...connector,
          protocol: connectorFailed ? "unknown" : protocol === "unknown" ? "quic" : protocol,
          connections: connectorFailed ? 0 : 4,
          status: connectorFailed ? "critical" : "healthy",
        },
        checks: checks.map((check) => ({
          ...check,
          status: affected && check.id === "quic-edge" ? quicStatus
            : affected && kind === "critical" && (check.id.startsWith("public-") || check.id === "provider-ai-input") ? "critical"
              : affected && kind === "degraded" && check.id === "provider-krill" ? "degraded"
                : "healthy",
        })),
      };
    });
    const incidents = kind === "healthy" ? [] : [{
      id: "demo-incident",
      startedAt: new Date(Date.now() - 75 * 60 * 1000).toISOString(),
      open: true,
      severity: overall,
      title: kind === "critical" ? "公网 Tunnel connector 不可用" : "QUIC 备用路径出现降级",
      detail: kind === "critical" ? "生产公网入口返回 Cloudflare 1033" : "真实 QUIC 握手超时，生产 HTTP/2 保持在线",
      checkId: "quic-edge",
    }];
    return { current: { timestamp: now, overall, summary, connector, checks, nextProbeAt: now }, history, incidents };
  }

  async function fetchStatus() {
    const requestID = ++state.requestID;
    if (state.controller) state.controller.abort();
    const controller = new AbortController();
    state.controller = controller;
    $("refresh-button").classList.add("is-loading");
    try {
      const response = await fetch("/api/status", { cache: "no-store", signal: controller.signal });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json();
      if (requestID !== state.requestID) return;
      state.data = data;
      render(data.current, data.history, data.incidents, data.stale, data.hasSnapshot !== false);
    } catch (error) {
      if (error.name === "AbortError") return;
      showToast(`状态读取失败：${error.message}`);
      const previous = state.data;
      const hasPreviousSnapshot = Boolean(previous && previous.hasSnapshot !== false);
      const failedAt = hasPreviousSnapshot ? formatTime(previous.current.timestamp) : "--";
      const current = markSnapshotUnknown(previous && previous.current, "状态刷新失败");
      const alertMessage = hasPreviousSnapshot
        ? `状态刷新失败，当前显示 ${failedAt} 的上次成功数据，实时状态已标记为未知。`
        : previous
          ? "状态刷新失败；此前仍在等待第一次探测，当前没有可用数据。"
          : "无法读取状态采集器，当前没有可用数据。";
      render(current, previous ? previous.history : [], previous ? previous.incidents : [], true, hasPreviousSnapshot, alertMessage);
    } finally {
      if (requestID === state.requestID) {
        $("refresh-button").classList.remove("is-loading");
      }
    }
  }

  function markSnapshotUnknown(snapshot, detail) {
    const current = snapshot || {};
    return {
      ...current,
      overall: "unknown",
      summary: detail,
      connector: {
        ...(current.connector || {}),
        mode: "unknown",
        protocol: "unknown",
        protocolStatuses: {},
        connections: 0,
        status: "unknown",
        detail,
      },
      checks: (current.checks || []).map((check) => ({ ...check, status: "unknown", latencyMs: 0, detail })),
    };
  }

  function loadStatus() {
    if (["healthy", "degraded", "critical"].includes(demo)) {
      state.data = demoData(demo);
      render(state.data.current, state.data.history, state.data.incidents);
      $("last-updated").textContent = "演示状态 · 未写入服务器";
      return;
    }
    fetchStatus();
  }

  function showToast(message) {
    const toast = $("toast");
    toast.textContent = message;
    toast.classList.add("is-visible");
    window.clearTimeout(showToast.timer);
    showToast.timer = window.setTimeout(() => toast.classList.remove("is-visible"), 3200);
  }

  function startAutoRefresh() {
    window.clearInterval(state.timer);
    if ($("auto-refresh").checked) state.timer = window.setInterval(loadStatus, 30000);
  }

  $("refresh-button").addEventListener("click", loadStatus);
  $("auto-refresh").addEventListener("change", startAutoRefresh);

  loadStatus();
  startAutoRefresh();
})();
