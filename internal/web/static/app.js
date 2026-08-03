(() => {
  const state = {
    range: "24h",
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

  const demo = new URLSearchParams(window.location.search).get("demo");

  const $ = (id) => document.getElementById(id);

  function formatTime(iso) {
    if (!iso) return "--";
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "--";
    return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  function formatShortDate(iso) {
    if (!iso) return "--";
    const date = new Date(iso);
    return date.toLocaleDateString("zh-CN", { month: "2-digit", day: "2-digit" });
  }

  function formatLatency(value) {
    if (!Number.isFinite(value) || value <= 0) return "--";
    return `${Math.round(value)}`;
  }

  function statusClass(status) {
    return `status-${status || "unknown"}`;
  }

  function setStatusClass(element, status) {
    ["healthy", "degraded", "critical", "unknown"].forEach((value) => element.classList.remove(`status-${value}`));
    element.classList.add(statusClass(status));
  }

  function currentCheck(checks, id) {
    return (checks || []).find((check) => check.id === id) || {
      id,
      status: "unknown",
      detail: "尚无数据",
    };
  }

  function protocolStatus(snapshot, id) {
    if (id === "http2-edge" && snapshot && snapshot.connector && snapshot.connector.status) {
      return snapshot.connector.status;
    }
    return currentCheck(snapshot && snapshot.checks, id).status;
  }

  function uptime(history, id) {
    const values = (history || []).map((snapshot) => protocolStatus(snapshot, id)).filter((status) => status && status !== "unknown");
    if (!values.length) return "--";
    const good = values.filter((value) => value === "healthy").length;
    return `${((good / values.length) * 100).toFixed(2)}%`;
  }

  function render(snapshot, history, incidents, stale = false) {
    $("last-updated").textContent = `更新于 ${formatTime(snapshot.timestamp)}`;
    $("data-stale").hidden = !stale;

    const http2 = currentCheck(snapshot.checks, "http2-edge");
    const quic = currentCheck(snapshot.checks, "quic-edge");
    $("http2-row-latency").textContent = http2.latencyMs ? `${formatLatency(http2.latencyMs)} ms` : "--";
    $("quic-row-latency").textContent = quic.latencyMs ? `${formatLatency(quic.latencyMs)} ms` : "--";
    $("http2-detail").textContent = snapshot.connector ? snapshot.connector.detail : http2.detail;
    $("quic-detail").textContent = quic.detail || "等待真实 QUIC/TLS 握手";
    const rangeLabel = state.range === "7d" ? "7d" : "24h";
    $("http2-uptime-label").textContent = `${rangeLabel} 可用率`;
    $("quic-uptime-label").textContent = `${rangeLabel} 可用率`;
    $("http2-uptime").textContent = uptime(history, "http2-edge");
    $("quic-uptime").textContent = uptime(history, "quic-edge");
    renderRowStatus($("http2-status"), snapshot.connector ? snapshot.connector.status : http2.status);
    renderRowStatus($("quic-status"), quic.status);
    renderTrack($("http2-track"), history, "http2-edge", snapshot.connector ? snapshot.connector.status : "unknown");
    renderTrack($("quic-track"), history, "quic-edge", quic.status);

    const serviceChecks = (snapshot.checks || []).filter((check) => check.id.startsWith("local-") || check.id.startsWith("public-"));
    $("service-table").innerHTML = serviceChecks.length ? serviceChecks.map(renderService).join("") : '<div class="service-loading">暂无服务路径数据</div>';
    $("incidents-list").innerHTML = incidents && incidents.length ? incidents.map(renderIncident).join("") : '<div class="empty-state"><span class="empty-check" aria-hidden="true">&#10003;</span><p>当前时间范围内没有异常记录</p></div>';
    $("incident-count").textContent = `${incidents ? incidents.length : 0} 条`;
    $("range-start").textContent = history && history.length ? formatShortDate(history[0].timestamp) : "--";
  }

  function renderRowStatus(element, status) {
    setStatusClass(element, status);
    element.innerHTML = `<span></span>${labels[status] || labels.unknown}`;
  }

  function renderTrack(element, history, id, currentStatus) {
    const samples = (history || []).slice(-96);
    const count = samples.length || 24;
    element.style.setProperty("--segments", String(count));
    const padded = samples.length ? samples : Array.from({ length: count }, () => null);
    element.innerHTML = padded.slice(-count).map((snapshot) => {
      const status = snapshot ? protocolStatus(snapshot, id) : currentStatus === "healthy" ? "unknown" : currentStatus;
      return `<span class="track-segment ${status || "unknown"}" title="${snapshot ? `${formatTime(snapshot.timestamp)} · ${labels[status] || labels.unknown}` : "暂无数据"}"></span>`;
    }).join("");
    element.setAttribute("aria-label", `${id === "quic-edge" ? "QUIC" : "HTTP/2"} 历史状态，最新为 ${labels[currentStatus] || labels.unknown}`);
  }

  function renderService(check) {
    const status = check.status || "unknown";
    return `<div class="service-row"><div class="service-main"><span class="service-dot ${statusClass(status)}" aria-hidden="true"></span><div><strong>${escapeHTML(check.name)}</strong><span>${escapeHTML(check.detail || check.target || "")}</span></div></div><div class="service-metric"><span>响应</span><strong>${check.latencyMs ? `${formatLatency(check.latencyMs)} ms` : "--"}</strong></div><div class="service-state ${statusClass(status)}">${labels[status] || labels.unknown}</div></div>`;
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
    const historyStep = state.range === "7d" ? 315 : 45;
    const quicStatus = kind === "healthy" ? "healthy" : kind === "critical" ? "critical" : "degraded";
    const overall = kind === "healthy" ? "healthy" : kind === "critical" ? "critical" : "degraded";
    const checks = [
      { id: "http2-edge", name: "HTTP/2 边缘链路", protocol: "http2", status: "healthy", latencyMs: 176, detail: "TCP + TLS 握手成功", target: "198.41.192.167:7844" },
      { id: "quic-edge", name: "QUIC 边缘链路", protocol: "quic", status: quicStatus, latencyMs: quicStatus === "healthy" ? 181 : 0, detail: quicStatus === "healthy" ? "真实 QUIC/TLS 握手成功；未注册 connector" : "timeout: no recent network activity", target: "198.41.192.167:7844" },
      { id: "local-api", name: "CLIProxyAPI 本机", protocol: "origin", status: "healthy", latencyMs: 1, detail: "HTTP 200，路径可达" },
      { id: "local-panel", name: "CPA Manager Plus 本机", protocol: "origin", status: "healthy", latencyMs: 2, detail: "HTTP 200，路径可达" },
      { id: "public-api", name: "API 公网入口", protocol: "http2", status: kind === "critical" ? "critical" : "healthy", latencyMs: 780, detail: kind === "critical" ? "timeout" : "HTTP 200，路径可达" },
      { id: "public-panel", name: "面板公网入口", protocol: "http2", status: kind === "critical" ? "critical" : "healthy", latencyMs: 590, detail: kind === "critical" ? "HTTP 1033" : "HTTP 302，Access 保护生效" },
    ];
    return { current: { timestamp: now, overall, summary: kind === "healthy" ? "生产 HTTP/2 与 QUIC 备用路径均在线" : kind === "critical" ? "公网入口无法找到健康 Tunnel connector" : "生产 HTTP/2 正常，QUIC 备用路径出现丢包", connector: { protocol: "http2", connections: kind === "critical" ? 0 : 4, status: kind === "critical" ? "critical" : "healthy", detail: "演示数据" }, checks, nextProbeAt: now }, history: Array.from({ length: 32 }, (_, index) => { const affected = index > 27; return { timestamp: new Date(Date.now() - (31 - index) * historyStep * 60 * 1000).toISOString(), overall: affected && kind !== "healthy" ? overall : "healthy", connector: { protocol: "http2", connections: affected && kind === "critical" ? 0 : 4, status: affected && kind === "critical" ? "critical" : "healthy", detail: "演示数据" }, checks: checks.map((check) => ({ ...check, status: affected && check.id === "quic-edge" ? quicStatus : affected && kind === "critical" && check.id.startsWith("public-") ? "critical" : "healthy" })) }; }), incidents: kind === "healthy" ? [] : [{ id: "demo-incident", startedAt: new Date(Date.now() - 75 * 60 * 1000).toISOString(), open: true, severity: overall, title: kind === "critical" ? "公网 Tunnel connector 不可用" : "QUIC 备用路径出现降级", detail: kind === "critical" ? "生产公网入口返回 Cloudflare 1033" : "真实 QUIC 握手超时，生产 HTTP/2 保持在线", checkId: "quic-edge" }] };
  }

  async function fetchStatus() {
    const requestRange = state.range;
    const requestID = ++state.requestID;
    if (state.controller) state.controller.abort();
    const controller = new AbortController();
    state.controller = controller;
    $("refresh-button").classList.add("is-loading");
    try {
      const response = await fetch(`/api/status?range=${encodeURIComponent(requestRange)}`, { cache: "no-store", signal: controller.signal });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const data = await response.json();
      if (requestID !== state.requestID || requestRange !== state.range || data.range !== state.range) return;
      state.data = data;
      render(data.current, data.history, data.incidents, data.stale);
    } catch (error) {
      if (error.name === "AbortError") return;
      showToast(`状态读取失败：${error.message}`);
      if (!state.data) {
        render({ overall: "unknown", summary: "无法读取状态采集器" }, [], [], true);
      }
    } finally {
      if (requestID === state.requestID) {
        $("refresh-button").classList.remove("is-loading");
      }
    }
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
  document.querySelectorAll("[data-range]").forEach((button) => button.addEventListener("click", () => {
    state.range = button.dataset.range;
    document.querySelectorAll("[data-range]").forEach((item) => {
      const active = item === button;
      item.classList.toggle("is-active", active);
      item.setAttribute("aria-pressed", String(active));
    });
    loadStatus();
  }));

  loadStatus();
  startAutoRefresh();
})();
