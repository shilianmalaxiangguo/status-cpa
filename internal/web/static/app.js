(() => {
  const state = {
    requestID: 0,
    controller: null,
    timer: null,
    data: null,
    tracks: new Map(),
    activeTrack: null,
    tooltipHideTimer: null,
  };

  const themeStorageKey = "status-cpa-theme";

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
    { id: "provider-ai-input", label: "AI INPUT", latencyLabel: "延迟" },
    { id: "provider-ciii", label: "CIII", latencyLabel: "延迟" },
    { id: "provider-pipio", label: "PIPIO", latencyLabel: "延迟", noLatency: "未提供" },
    { id: "provider-krill", label: "KRILL", latencyLabel: "TTFT P99" },
    { id: "provider-jimu-ai", label: "JiMu-Ai", latencyLabel: "延迟" },
    { id: "provider-openai-conversations", label: "OPENAI Conversations", latencyLabel: "延迟", noLatency: "不适用" },
  ];

  const demo = new URLSearchParams(window.location.search).get("demo");

  const $ = (id) => document.getElementById(id);

  function readTheme() {
    try {
      const stored = window.localStorage.getItem(themeStorageKey);
      return stored === "light" || stored === "dark" ? stored : "dark";
    } catch (_) {
      return "dark";
    }
  }

  function applyTheme(theme, persist = false) {
    const selected = theme === "light" ? "light" : "dark";
    const isDark = selected === "dark";
    document.documentElement.dataset.theme = selected;
    $("theme-toggle").setAttribute("aria-pressed", String(isDark));
    $("theme-toggle").title = `切换到${isDark ? "浅色" : "深色"}主题`;
    $("theme-icon").textContent = isDark ? "☀" : "☾";
    document.querySelector('meta[name="theme-color"]').content = isDark ? "#000000" : "#f7f8f5";
    if (!persist) return;
    try {
      window.localStorage.setItem(themeStorageKey, selected);
    } catch (_) {
      // The selected theme still applies for this page when storage is unavailable.
    }
  }

  function formatTime(iso) {
    if (!iso) return "--";
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "--";
    return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  function formatSnapshotTime(iso) {
    if (!iso) return "时间未知";
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "时间未知";
    return date.toLocaleString("zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    });
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

  function findCheck(checks, id) {
    return (checks || []).find((check) => check.id === id);
  }

  function currentCheck(checks, id) {
    return findCheck(checks, id) || {
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

  function missingPoint(label, snapshot, metricLabel) {
    return {
      label,
      timestamp: snapshot && snapshot.timestamp,
      status: "unknown",
      metricLabel,
      metricValue: "--",
      detail: "此分钟无探测数据",
    };
  }

  function protocolHistoryPoint(snapshot, id, label) {
    if (!snapshot) return missingPoint(label, null, "握手延迟");
    const connector = snapshot.connector || {};
    const protocol = protocolKey(id);
    const edgeCheck = findCheck(snapshot.checks, id);
    const storedStatus = connector.protocolStatuses && connector.protocolStatuses[protocol];
    const hasConnectorStatus = ["healthy", "degraded", "critical", "unknown"].includes(storedStatus);
    const isProduction = productionConnectorProtocol(connector) === protocol;
    const status = protocolStatus(snapshot, id);
    const details = [];

    if (isProduction && connector.detail) {
      details.push(connector.detail);
    } else if (hasConnectorStatus) {
      details.push(`${label} connector ${labels[status]}；此分钟未保留详细原因`);
    }
    if (edgeCheck && edgeCheck.detail) {
      details.push(`${hasConnectorStatus || isProduction ? "独立握手" : "握手"}：${edgeCheck.detail}`);
    }

    return {
      label,
      timestamp: snapshot.timestamp,
      status,
      metricLabel: "握手延迟",
      metricValue: edgeCheck && edgeCheck.latencyMs ? `${formatLatency(edgeCheck.latencyMs)} ms` : "--",
      detail: details.join("；") || "此分钟无探测数据",
    };
  }

  function providerHistoryPoint(snapshot, provider) {
    if (!snapshot) return missingPoint(provider.label, null, provider.latencyLabel);
    const check = findCheck(snapshot.checks, provider.id);
    if (!check) return missingPoint(provider.label, snapshot, provider.latencyLabel);
    const status = normalizeStatus(check.status);
    return {
      label: provider.label,
      timestamp: snapshot.timestamp,
      status,
      metricLabel: provider.latencyLabel,
      metricValue: check.latencyMs ? `${formatLatency(check.latencyMs)} ms` : status !== "unknown" && provider.noLatency ? provider.noLatency : "--",
      detail: check.detail || "该分钟未提供探测详情",
    };
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
    renderTrack($("http2-track"), history, "http2-edge");
    renderTrack($("quic-track"), history, "quic-edge");

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
      renderCheckTrack($(`${provider.id}-track`), history, provider);
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

  function renderTrack(element, history, id) {
    const label = id === "quic-edge" ? "QUIC" : "HTTP/2";
    renderHistoryTrack(element, history, label, (snapshot) => protocolHistoryPoint(snapshot, id, label));
  }

  function renderCheckTrack(element, history, provider) {
    renderHistoryTrack(element, history, provider.label, (snapshot) => providerHistoryPoint(snapshot, provider));
  }

  function renderHistoryTrack(element, history, label, pointFor) {
    const count = 60;
    const samples = (history || []).slice(-count);
    const track = ensureTrack(element, label);
    const previousPoint = track.points[track.selectedIndex];
    const previousTimestamp = previousPoint && previousPoint.timestamp;
    const wasActive = state.activeTrack === track;
    const wasPinned = track.pinned;
    const points = Array.from({ length: count - samples.length }, () => null).concat(samples).map(pointFor);
    let selectedIndex = previousTimestamp
      ? points.findIndex((point) => point.timestamp === previousTimestamp)
      : previousPoint
        ? Math.min(track.selectedIndex, points.length - 1)
        : points.length - 1;
    const selectionExpired = previousTimestamp && selectedIndex < 0;
    if (selectedIndex < 0) selectedIndex = points.length - 1;

    element.style.setProperty("--segments", String(count));
    const fragment = document.createDocumentFragment();
    points.forEach((point) => {
      const segment = document.createElement("span");
      segment.className = `track-segment ${normalizeStatus(point.status)}`;
      segment.setAttribute("aria-hidden", "true");
      fragment.append(segment);
    });
    element.replaceChildren(fragment);
    element.setAttribute("aria-label", `${label} 近 60 分钟历史状态`);

    track.label = label;
    track.points = points;
    track.selectedIndex = selectedIndex;
    updateTrackARIA(track);
    if (wasActive && wasPinned && selectionExpired) {
      track.pinned = false;
      hideTrackTooltip(track, true);
    } else if (wasActive) {
      showTrackTooltip(track);
    }
  }

  function ensureTrack(element, label) {
    const existing = state.tracks.get(element.id);
    if (existing) {
      existing.label = label;
      return existing;
    }

    const track = {
      element,
      label,
      points: [],
      selectedIndex: 59,
      pinned: false,
    };
    state.tracks.set(element.id, track);
    element.addEventListener("pointermove", (event) => {
      if (event.pointerType === "touch" || track.pinned || !track.points.length) return;
      const index = trackIndexAt(track, event.clientX);
      if (state.activeTrack === track && track.selectedIndex === index) return;
      selectTrackPoint(track, index);
    });
    element.addEventListener("pointerleave", () => {
      if (!track.pinned) scheduleTrackTooltipHide(track);
    });
    element.addEventListener("click", (event) => {
      if (!track.points.length) return;
      const index = trackIndexAt(track, event.clientX);
      if (state.activeTrack === track && track.pinned && track.selectedIndex === index) {
        closeTrackTooltip();
        return;
      }
      track.pinned = true;
      selectTrackPoint(track, index);
    });
    element.addEventListener("focus", () => {
      window.requestAnimationFrame(() => {
        if (document.activeElement !== element || !track.points.length) return;
        selectTrackPoint(track, track.selectedIndex);
      });
    });
    element.addEventListener("blur", () => {
      if (state.activeTrack !== track) return;
      track.pinned = false;
      hideTrackTooltip(track, true);
    });
    element.addEventListener("keydown", (event) => handleTrackKeydown(event, track));
    return track;
  }

  function trackIndexAt(track, clientX) {
    const bounds = track.element.getBoundingClientRect();
    const ratio = bounds.width ? (clientX - bounds.left) / bounds.width : 1;
    return Math.max(0, Math.min(track.points.length - 1, Math.floor(ratio * track.points.length)));
  }

  function handleTrackKeydown(event, track) {
    let nextIndex = track.selectedIndex;
    switch (event.key) {
      case "ArrowLeft":
      case "ArrowDown":
        nextIndex -= 1;
        break;
      case "ArrowRight":
      case "ArrowUp":
        nextIndex += 1;
        break;
      case "Home":
        nextIndex = 0;
        break;
      case "End":
        nextIndex = track.points.length - 1;
        break;
      case "Enter":
      case " ":
        event.preventDefault();
        if (state.activeTrack === track && track.pinned) {
          closeTrackTooltip();
        } else {
          track.pinned = true;
          showTrackTooltip(track);
        }
        return;
      case "Escape":
        event.preventDefault();
        closeTrackTooltip();
        return;
      default:
        return;
    }
    event.preventDefault();
    selectTrackPoint(track, Math.max(0, Math.min(track.points.length - 1, nextIndex)));
  }

  function selectTrackPoint(track, index) {
    if (!track.points.length) return;
    track.selectedIndex = Math.max(0, Math.min(track.points.length - 1, index));
    updateTrackARIA(track);
    showTrackTooltip(track);
  }

  function updateTrackARIA(track) {
    const point = track.points[track.selectedIndex];
    if (!point) {
      track.element.setAttribute("aria-valuetext", "等待历史数据");
      return;
    }
    track.element.setAttribute("aria-valuenow", String(track.selectedIndex + 1));
    track.element.setAttribute("aria-valuemax", String(track.points.length));
    track.element.setAttribute("aria-valuetext", `${formatSnapshotTime(point.timestamp)}，${point.label} ${labels[normalizeStatus(point.status)]}，${point.metricLabel} ${point.metricValue}，${point.detail}`);
  }

  function showTrackTooltip(track) {
    const point = track.points[track.selectedIndex];
    const segment = track.element.children[track.selectedIndex];
    if (!point || !segment) return;

    cancelTrackTooltipHide();
    if (state.activeTrack && state.activeTrack !== track) {
      state.activeTrack.pinned = false;
      clearTrackHighlight(state.activeTrack);
    }
    state.activeTrack = track;
    clearTrackHighlight(track);
    segment.classList.add("is-selected");

    $("track-tooltip-label").textContent = point.label;
    $("track-tooltip-time").textContent = formatSnapshotTime(point.timestamp);
    $("track-tooltip-time").dateTime = point.timestamp || "";
    $("track-tooltip-state").textContent = labels[normalizeStatus(point.status)];
    setStatusClass($("track-tooltip-state"), point.status);
    $("track-tooltip-metric-label").textContent = point.metricLabel;
    $("track-tooltip-metric-value").textContent = point.metricValue;
    $("track-tooltip-detail").textContent = point.detail;

    const tooltip = $("track-tooltip");
    tooltip.hidden = false;
    positionTrackTooltip(track, segment);
  }

  function positionTrackTooltip(track, segment) {
    const tooltip = $("track-tooltip");
    const trackBounds = track.element.getBoundingClientRect();
    const segmentBounds = segment.getBoundingClientRect();
    const viewportWidth = document.documentElement.clientWidth;
    const viewportHeight = document.documentElement.clientHeight;
    const tooltipWidth = tooltip.offsetWidth;
    const tooltipHeight = tooltip.offsetHeight;
    const anchorX = (segmentBounds.left + segmentBounds.right) / 2;
    const edgeSpace = 12;
    const left = Math.max(tooltipWidth / 2 + edgeSpace, Math.min(viewportWidth - tooltipWidth / 2 - edgeSpace, anchorX));
    const gap = 8;
    const spaceAbove = trackBounds.top - gap - edgeSpace;
    const spaceBelow = viewportHeight - trackBounds.bottom - gap - edgeSpace;
    const showBelow = spaceBelow >= tooltipHeight || (spaceAbove < tooltipHeight && spaceBelow > spaceAbove);
    const preferredTop = showBelow ? trackBounds.bottom + gap : trackBounds.top - tooltipHeight - gap;
    const top = Math.max(edgeSpace, Math.min(viewportHeight - tooltipHeight - edgeSpace, preferredTop));
    const arrowX = Math.max(16, Math.min(tooltipWidth - 16, anchorX - (left - tooltipWidth / 2)));

    tooltip.classList.toggle("is-below", showBelow);
    tooltip.style.left = `${left}px`;
    tooltip.style.top = `${top}px`;
    tooltip.style.setProperty("--tooltip-arrow-x", `${arrowX}px`);
  }

  function clearTrackHighlight(track) {
    const selected = track.element.querySelector(".track-segment.is-selected");
    if (selected) selected.classList.remove("is-selected");
  }

  function cancelTrackTooltipHide() {
    window.clearTimeout(state.tooltipHideTimer);
    state.tooltipHideTimer = null;
  }

  function scheduleTrackTooltipHide(track) {
    cancelTrackTooltipHide();
    state.tooltipHideTimer = window.setTimeout(() => hideTrackTooltip(track), 120);
  }

  function hideTrackTooltip(track, force = false) {
    const active = state.activeTrack;
    if (!active || track && active !== track || !force && active.pinned) return;
    cancelTrackTooltipHide();
    clearTrackHighlight(active);
    $("track-tooltip").hidden = true;
    $("track-tooltip").classList.remove("is-below");
    state.activeTrack = null;
  }

  function closeTrackTooltip() {
    if (!state.activeTrack) return;
    state.activeTrack.pinned = false;
    hideTrackTooltip(state.activeTrack, true);
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
    const nowMs = Date.now();
    const now = new Date(nowMs).toISOString();
    const historyEnd = Math.floor(nowMs / 60000) * 60000;
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
      { id: "provider-ciii", name: "CIII", protocol: "model", status: "healthy", latencyMs: 3002, detail: "gpt-5.6-sol 最近探测正常" },
      { id: "provider-pipio", name: "PIPIO", protocol: "model", status: "healthy", latencyMs: 0, detail: "gpt-5.6-sol 发布状态正常；未提供模型延迟和更新时间" },
      { id: "provider-krill", name: "KRILL", protocol: "model", status: krillStatus, latencyMs: 447, detail: `gpt-5.6-sol 发布状态${krillStatus === "healthy" ? "正常" : "降级"}；延迟为 TTFT P99` },
      { id: "provider-jimu-ai", name: "JiMu-Ai", protocol: "model", status: "healthy", latencyMs: 42, detail: "gpt-5.6-sol 最近探测正常" },
      { id: "provider-openai-conversations", name: "OPENAI", protocol: "model", status: "healthy", latencyMs: 0, detail: "Conversations 官方聚合状态正常；非 gpt-5.6-sol 单模型探测" },
    ];
    const summary = kind === "healthy" ? "自动模式当前选择 QUIC，HTTP/2 备用路径正常" : kind === "critical" ? "公网入口无法找到健康 Tunnel connector" : "HTTP/2 生产隧道正常，QUIC 备用路径出现降级";
    const history = Array.from({ length: 60 }, (_, index) => {
      const affected = index > 55;
      const connectorFailed = affected && kind === "critical";
      const historyProtocol = connectorFailed ? "unknown" : protocol === "unknown" ? "quic" : protocol;
      return {
        timestamp: new Date(historyEnd - (59 - index) * historyStep * 60 * 1000).toISOString(),
        overall: affected && kind !== "healthy" ? overall : "healthy",
        connector: {
          ...connector,
          protocol: historyProtocol,
          connections: connectorFailed ? 0 : 4,
          status: connectorFailed ? "critical" : "healthy",
          detail: connectorFailed ? "没有生产 connector 在线" : `4 条生产 ${protocolLabels[historyProtocol]} connector 在线`,
        },
        checks: checks.map((check) => {
          const status = affected && check.id === "quic-edge" ? quicStatus
            : affected && kind === "critical" && (check.id.startsWith("public-") || check.id === "provider-ai-input") ? "critical"
              : affected && kind === "degraded" && check.id === "provider-krill" ? "degraded"
                : "healthy";
          if (status !== "healthy") return { ...check, status };
          if (check.id === "quic-edge") return { ...check, status, latencyMs: 181, detail: "真实 QUIC/TLS 握手成功；未注册 connector" };
          if (check.id === "public-api") return { ...check, status, latencyMs: 780, detail: "HTTP 200，路径可达" };
          if (check.id === "public-panel") return { ...check, status, latencyMs: 590, detail: "HTTP 302，Access 保护生效" };
          if (check.id === "provider-ai-input") return { ...check, status, latencyMs: 2820, detail: "gpt-5.6-sol 最近探测正常" };
          if (check.id === "provider-krill") return { ...check, status, latencyMs: 447, detail: "gpt-5.6-sol 发布状态正常；延迟为 TTFT P99" };
          return { ...check, status };
        }),
      };
    });
    const incidents = kind === "healthy" ? [] : [{
      id: "demo-incident",
      startedAt: new Date(nowMs - 75 * 60 * 1000).toISOString(),
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

  document.addEventListener("pointerdown", (event) => {
    if (!state.activeTrack || state.activeTrack.element.contains(event.target) || $("track-tooltip").contains(event.target)) return;
    closeTrackTooltip();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || !state.activeTrack) return;
    event.preventDefault();
    closeTrackTooltip();
  });
  window.addEventListener("resize", () => {
    if (!state.activeTrack) return;
    const segment = state.activeTrack.element.children[state.activeTrack.selectedIndex];
    if (segment) positionTrackTooltip(state.activeTrack, segment);
  });
  window.addEventListener("scroll", (event) => {
    if ($("track-tooltip").contains(event.target)) return;
    closeTrackTooltip();
  }, true);
  $("track-tooltip").addEventListener("pointerenter", cancelTrackTooltipHide);
  $("track-tooltip").addEventListener("pointerleave", () => {
    if (state.activeTrack && !state.activeTrack.pinned) scheduleTrackTooltipHide(state.activeTrack);
  });
  $("theme-toggle").addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    applyTheme(next, true);
  });
  $("refresh-button").addEventListener("click", loadStatus);
  $("auto-refresh").addEventListener("change", startAutoRefresh);

  applyTheme(readTheme());
  loadStatus();
  startAutoRefresh();
})();
