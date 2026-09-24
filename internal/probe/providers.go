package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shilianmalaxiangguo/status-cpa/internal/model"
)

const (
	targetModelName                      = "gpt-5.6-sol"
	jimuAITargetModelName                = "gpt-5.5"
	ciiiTargetMonitorID                  = 13
	ciiiTargetMonitorKey                 = "13"
	ciiiTargetMonitorName                = "Ciii-codex gpt-5.6-sol"
	openAIConversationsComponentID       = "01JMXBNJXGV1T5GT2M9XA83XNG"
	maxModelSourceMessageRunes           = 240
	maxModelSourceBody             int64 = 2 << 20
)

type ModelSourceKind string

const (
	ModelSourceAIInput ModelSourceKind = "ai-input"
	ModelSourcePIPIO   ModelSourceKind = "pipio"
	ModelSourceKrill   ModelSourceKind = "krill"
	ModelSourceCIII    ModelSourceKind = "ciii"
	ModelSourceJiMuAI  ModelSourceKind = "jimu-ai"
	ModelSourceOpenAI  ModelSourceKind = "openai"
)

type ModelSource struct {
	ID          string
	Name        string
	Model       string
	URL         string
	MetadataURL string
	Kind        ModelSourceKind
}

type modelSourceHTTPError struct {
	status int
}

func (e modelSourceHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d", e.status)
}

func (c *Collector) probeModelSource(ctx context.Context, source ModelSource, now time.Time) model.Check {
	if source.Model == "" {
		source.Model = targetModelName
	}
	check := model.Check{
		ID:       source.ID,
		Name:     source.Name,
		Protocol: "model",
		Target:   source.URL,
		Status:   model.Unknown,
		Detail:   source.Model + " 状态尚未读取",
	}

	switch source.Kind {
	case ModelSourceAIInput:
		return c.probeAIInput(ctx, source, now, check)
	case ModelSourcePIPIO:
		return c.probePIPIO(ctx, source, check)
	case ModelSourceKrill:
		return c.probeKrill(ctx, source, now, check)
	case ModelSourceCIII:
		return c.probeCIII(ctx, source, now, check)
	case ModelSourceJiMuAI:
		return c.probeJiMuAI(ctx, source, now, check)
	case ModelSourceOpenAI:
		return c.probeOpenAI(ctx, source, check)
	default:
		check.FailureCode = "unsupported_source"
		check.Detail = "不支持的模型状态源"
		return check
	}
}

func (c *Collector) probeAIInput(ctx context.Context, source ModelSource, now time.Time, check model.Check) model.Check {
	targetModelName := source.Model
	payload := struct {
		GeneratedAt int64 `json:"generated_at"`
		Services    []struct {
			Model     string  `json:"model"`
			UptimePct float64 `json:"uptime_pct"`
			Last      struct {
				Timestamp int64   `json:"ts"`
				OK        *bool   `json:"ok"`
				LatencyMS float64 `json:"latency_ms"`
			} `json:"last"`
		} `json:"services"`
	}{}
	if err := c.getModelSourceJSON(ctx, source.URL, &payload); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}

	var service *struct {
		Model     string  `json:"model"`
		UptimePct float64 `json:"uptime_pct"`
		Last      struct {
			Timestamp int64   `json:"ts"`
			OK        *bool   `json:"ok"`
			LatencyMS float64 `json:"latency_ms"`
		} `json:"last"`
	}
	matches := 0
	for i := range payload.Services {
		if payload.Services[i].Model == targetModelName {
			service = &payload.Services[i]
			matches++
		}
	}
	if matches != 1 || service == nil || service.Last.OK == nil {
		return invalidModelSource(check, "AI INPUT 没有唯一、完整的 "+targetModelName+" 状态")
	}
	generatedAt := time.Unix(payload.GeneratedAt, 0)
	lastAt := time.Unix(service.Last.Timestamp, 0)
	if !freshModelSourceTime(generatedAt, now, 3*time.Minute) || !freshModelSourceTime(lastAt, now, 3*time.Minute) {
		check.FailureCode = "source_stale"
		check.Detail = targetModelName + " 状态超过 3 分钟未更新"
		return check
	}
	if service.Last.LatencyMS > 0 {
		check.LatencyMS = service.Last.LatencyMS
	}
	if *service.Last.OK {
		check.Status = model.Healthy
		check.Detail = targetModelName + " 最近探测正常"
		return check
	}
	check.Status = model.Critical
	check.FailureCode = "reported_outage"
	check.Detail = targetModelName + " 最近探测失败"
	return check
}

func (c *Collector) probePIPIO(ctx context.Context, source ModelSource, check model.Check) model.Check {
	targetModelName := source.Model
	payload := struct {
		Success bool `json:"success"`
		Data    []struct {
			Monitors []struct {
				Name       string `json:"name"`
				Status     *int   `json:"status"`
				Heartbeats []*int `json:"heartbeats"`
			} `json:"monitors"`
		} `json:"data"`
	}{}
	if err := c.getModelSourceJSON(ctx, source.URL, &payload); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}
	if !payload.Success {
		return invalidModelSource(check, "PIPIO 状态源未返回成功")
	}

	var monitor *struct {
		Name       string `json:"name"`
		Status     *int   `json:"status"`
		Heartbeats []*int `json:"heartbeats"`
	}
	matches := 0
	for i := range payload.Data {
		for j := range payload.Data[i].Monitors {
			candidate := &payload.Data[i].Monitors[j]
			if candidate.Name == targetModelName {
				monitor = candidate
				matches++
			}
		}
	}
	if matches != 1 || monitor == nil || monitor.Status == nil || len(monitor.Heartbeats) == 0 {
		return invalidModelSource(check, "PIPIO 没有唯一、完整的 "+targetModelName+" 状态")
	}
	latest := monitor.Heartbeats[len(monitor.Heartbeats)-1]
	if latest == nil || *latest != *monitor.Status {
		return invalidModelSource(check, "PIPIO 当前状态与最新心跳不一致")
	}

	switch *monitor.Status {
	case 1:
		check.Status = model.Healthy
		check.Detail = targetModelName + " 发布状态正常；未提供模型延迟和更新时间"
	case 2:
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = targetModelName + " 发布状态确认中；未提供模型延迟和更新时间"
	case 3:
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = targetModelName + " 发布状态维护中；未提供模型延迟和更新时间"
	case 0:
		check.Status = model.Critical
		check.FailureCode = "reported_outage"
		check.Detail = targetModelName + " 发布状态中断；未提供模型延迟和更新时间"
	default:
		check.FailureCode = "unsupported_status"
		check.Detail = targetModelName + " 处于维护、等待或未知状态"
	}
	return check
}

func (c *Collector) probeKrill(ctx context.Context, source ModelSource, now time.Time, check model.Check) model.Check {
	targetModelName := source.Model
	payload := struct {
		Success bool `json:"success"`
		Code    int  `json:"code"`
		Data    struct {
			Channels []struct {
				ChannelKey   string `json:"channel_key"`
				ModelName    string `json:"model_name"`
				CurrentState *int   `json:"current_status"`
				History      []struct {
					State     *int   `json:"s"`
					Timestamp string `json:"ts"`
				} `json:"history"`
			} `json:"channels"`
			Performance []struct {
				ChannelKey string   `json:"channel_key"`
				TTFTP99MS  *float64 `json:"ttft_p99_ms"`
			} `json:"perf"`
		} `json:"data"`
	}{}
	if err := c.getModelSourceJSON(ctx, source.URL, &payload); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}
	if !payload.Success || payload.Code != 0 {
		return invalidModelSource(check, "KRILL 状态源未返回成功")
	}

	var targetChannelKey string
	switch targetModelName {
	case "gpt-6-astra":
		targetChannelKey = "openai_gpt_6_astra"
	case "gpt-5.6-sol":
		targetChannelKey = "openai_gpt_5_6_sol"
	case "gpt-5.6-terra":
		targetChannelKey = "openai_gpt_5_6_terra"
	default:
		return invalidModelSource(check, "KRILL 未配置 "+targetModelName+" 渠道")
	}
	var channel *struct {
		ChannelKey   string `json:"channel_key"`
		ModelName    string `json:"model_name"`
		CurrentState *int   `json:"current_status"`
		History      []struct {
			State     *int   `json:"s"`
			Timestamp string `json:"ts"`
		} `json:"history"`
	}
	matches := 0
	for i := range payload.Data.Channels {
		candidate := &payload.Data.Channels[i]
		if candidate.ChannelKey == targetChannelKey && candidate.ModelName == targetModelName {
			channel = candidate
			matches++
		}
	}
	if matches != 1 || channel == nil || channel.CurrentState == nil || len(channel.History) == 0 {
		return invalidModelSource(check, "KRILL 没有唯一、完整的 "+targetModelName+" 状态")
	}
	var latestAt time.Time
	var latestState int
	for _, sample := range channel.History {
		if sample.State == nil {
			return invalidModelSource(check, "KRILL 模型历史状态格式无效")
		}
		sampleAt, err := time.ParseInLocation("2006-01-02 15:04:05", sample.Timestamp, time.UTC)
		if err != nil {
			return invalidModelSource(check, "KRILL 模型历史时间格式无效")
		}
		if latestAt.IsZero() || sampleAt.After(latestAt) {
			latestAt = sampleAt
			latestState = *sample.State
			continue
		}
		if sampleAt.Equal(latestAt) && *sample.State != latestState {
			return invalidModelSource(check, "KRILL 最新模型历史状态相互冲突")
		}
	}
	if !freshModelSourceTime(latestAt, now, 15*time.Minute) {
		check.FailureCode = "source_stale"
		check.Detail = targetModelName + " 状态超过 15 分钟未更新"
		return check
	}
	if latestState != *channel.CurrentState {
		return invalidModelSource(check, "KRILL 当前状态与最新历史不一致")
	}

	perfMatches := 0
	for i := range payload.Data.Performance {
		performance := payload.Data.Performance[i]
		if performance.ChannelKey == targetChannelKey {
			perfMatches++
			if performance.TTFTP99MS != nil && *performance.TTFTP99MS > 0 {
				check.LatencyMS = *performance.TTFTP99MS
			}
		}
	}
	if perfMatches > 1 {
		check.LatencyMS = 0
	}

	switch *channel.CurrentState {
	case 1:
		check.Status = model.Healthy
		check.Detail = targetModelName + " 发布状态正常；延迟为 TTFT P99"
	case 2:
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = targetModelName + " 发布状态降级；延迟为 TTFT P99"
	case 0:
		check.Status = model.Critical
		check.FailureCode = "reported_outage"
		check.Detail = targetModelName + " 发布状态中断"
	default:
		check.FailureCode = "unsupported_status"
		check.Detail = targetModelName + " 返回未知状态"
	}
	return check
}

func (c *Collector) probeCIII(ctx context.Context, source ModelSource, now time.Time, check model.Check) model.Check {
	metadata := struct {
		PublicGroupList []struct {
			MonitorList []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"monitorList"`
		} `json:"publicGroupList"`
	}{}
	if strings.TrimSpace(source.MetadataURL) == "" {
		return invalidModelSource(check, "CIII 状态源未配置模型元数据")
	}
	if err := c.getModelSourceJSON(ctx, source.MetadataURL, &metadata); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}
	metadataMatches := 0
	for i := range metadata.PublicGroupList {
		for j := range metadata.PublicGroupList[i].MonitorList {
			monitor := metadata.PublicGroupList[i].MonitorList[j]
			if monitor.ID == ciiiTargetMonitorID && monitor.Name == ciiiTargetMonitorName && monitor.Type == "keyword" {
				metadataMatches++
			}
		}
	}
	if metadataMatches != 1 {
		return invalidModelSource(check, "CIII 没有唯一匹配的 "+targetModelName+" 监控项")
	}

	type heartbeat struct {
		Status *int     `json:"status"`
		Time   string   `json:"time"`
		Ping   *float64 `json:"ping"`
	}
	payload := struct {
		HeartbeatList map[string][]heartbeat `json:"heartbeatList"`
	}{}
	if err := c.getModelSourceJSON(ctx, source.URL, &payload); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}

	heartbeats, ok := payload.HeartbeatList[ciiiTargetMonitorKey]
	if !ok || len(heartbeats) == 0 {
		return invalidModelSource(check, "CIII 没有完整的 "+targetModelName+" 心跳")
	}
	var latest *heartbeat
	var latestAt time.Time
	for i := range heartbeats {
		candidate := &heartbeats[i]
		if candidate.Status == nil {
			return invalidModelSource(check, "CIII 模型心跳状态格式无效")
		}
		candidateAt, err := time.ParseInLocation("2006-01-02 15:04:05", candidate.Time, time.UTC)
		if err != nil {
			return invalidModelSource(check, "CIII 模型心跳时间格式无效")
		}
		if latest == nil || candidateAt.After(latestAt) {
			latest = candidate
			latestAt = candidateAt
			continue
		}
		if candidateAt.Equal(latestAt) && *candidate.Status != *latest.Status {
			return invalidModelSource(check, "CIII 最新模型心跳状态相互冲突")
		}
	}
	if latest == nil {
		return invalidModelSource(check, "CIII 没有完整的 "+targetModelName+" 心跳")
	}
	if !freshModelSourceTime(latestAt, now, 3*time.Minute) {
		check.FailureCode = "source_stale"
		check.Detail = targetModelName + " 状态超过 3 分钟未更新"
		return check
	}

	switch *latest.Status {
	case 1:
		if latest.Ping == nil || *latest.Ping < 0 {
			return invalidModelSource(check, "CIII 最新正常心跳没有有效延迟")
		}
		check.Status = model.Healthy
		check.LatencyMS = *latest.Ping
		check.Detail = targetModelName + " 最近探测正常"
	case 2:
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = targetModelName + " 最近探测确认中"
	case 3:
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = targetModelName + " 最近探测维护中"
	case 0:
		check.Status = model.Critical
		check.FailureCode = "reported_outage"
		check.Detail = targetModelName + " 最近探测失败"
	default:
		check.FailureCode = "unsupported_status"
		check.Detail = targetModelName + " 返回未知状态"
	}
	return check
}

func (c *Collector) probeJiMuAI(ctx context.Context, source ModelSource, now time.Time, check model.Check) model.Check {
	check.Detail = jimuAITargetModelName + " 状态尚未读取"
	metadata := struct {
		PublicGroupList []struct {
			MonitorList []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"monitorList"`
		} `json:"publicGroupList"`
	}{}
	if strings.TrimSpace(source.MetadataURL) == "" {
		return invalidModelSource(check, "JiMu-Ai 状态源未配置模型元数据")
	}
	if err := c.getModelSourceJSON(ctx, source.MetadataURL, &metadata); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}

	monitorID := 0
	monitorType := ""
	matches := 0
	monitorIDCounts := make(map[int]int)
	for i := range metadata.PublicGroupList {
		for j := range metadata.PublicGroupList[i].MonitorList {
			monitor := metadata.PublicGroupList[i].MonitorList[j]
			monitorIDCounts[monitor.ID]++
			if monitor.Name == jimuAITargetModelName {
				monitorID = monitor.ID
				monitorType = monitor.Type
				matches++
			}
		}
	}
	if matches == 0 {
		return invalidModelSource(check, "JiMu-Ai 尚未公开 "+jimuAITargetModelName+" 探针")
	}
	if matches > 1 {
		return invalidModelSource(check, "JiMu-Ai 发布了多个 "+jimuAITargetModelName+" 探针")
	}
	if monitorID <= 0 {
		return invalidModelSource(check, "JiMu-Ai 的 "+jimuAITargetModelName+" 探针 ID 无效")
	}
	if monitorIDCounts[monitorID] != 1 {
		return invalidModelSource(check, "JiMu-Ai 的 "+jimuAITargetModelName+" 探针 ID 绑定不唯一")
	}
	if monitorType != "http" {
		return invalidModelSource(check, "JiMu-Ai 的 "+jimuAITargetModelName+" 探针类型不是 HTTP")
	}

	type heartbeat struct {
		Status  *int     `json:"status"`
		Time    string   `json:"time"`
		Message string   `json:"msg"`
		Ping    *float64 `json:"ping"`
	}
	payload := struct {
		HeartbeatList map[string][]heartbeat `json:"heartbeatList"`
	}{}
	if err := c.getModelSourceJSON(ctx, source.URL, &payload); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}

	heartbeats, ok := payload.HeartbeatList[strconv.Itoa(monitorID)]
	if !ok || len(heartbeats) == 0 {
		return invalidModelSource(check, "JiMu-Ai 没有完整的 "+jimuAITargetModelName+" 心跳")
	}
	var latest *heartbeat
	var latestAt time.Time
	for i := range heartbeats {
		candidate := &heartbeats[i]
		if candidate.Status == nil {
			return invalidModelSource(check, "JiMu-Ai 模型心跳状态格式无效")
		}
		candidateAt, err := time.ParseInLocation("2006-01-02 15:04:05", candidate.Time, time.UTC)
		if err != nil {
			return invalidModelSource(check, "JiMu-Ai 模型心跳时间格式无效")
		}
		if latest == nil || candidateAt.After(latestAt) {
			latest = candidate
			latestAt = candidateAt
			continue
		}
		if candidateAt.Equal(latestAt) {
			samePing := candidate.Ping == nil && latest.Ping == nil
			if candidate.Ping != nil && latest.Ping != nil {
				samePing = *candidate.Ping == *latest.Ping
			}
			if *candidate.Status != *latest.Status || !samePing || candidate.Message != latest.Message {
				return invalidModelSource(check, "JiMu-Ai 最新模型心跳内容相互冲突")
			}
		}
	}
	if latest == nil {
		return invalidModelSource(check, "JiMu-Ai 没有完整的 "+jimuAITargetModelName+" 心跳")
	}
	if !freshModelSourceTime(latestAt, now, 7*time.Minute) {
		check.FailureCode = "source_stale"
		check.Detail = jimuAITargetModelName + " 状态超过 7 分钟未更新"
		return check
	}

	messageSuffix := boundedModelSourceMessageSuffix(latest.Message)
	switch *latest.Status {
	case 1:
		if latest.Ping == nil || *latest.Ping < 0 {
			return invalidModelSource(check, "JiMu-Ai 最新正常心跳没有有效延迟")
		}
		check.Status = model.Healthy
		check.LatencyMS = *latest.Ping
		check.Detail = jimuAITargetModelName + " 最近探测正常"
	case 2:
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = jimuAITargetModelName + " 最近探测确认中" + messageSuffix
	case 3:
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = jimuAITargetModelName + " 最近探测维护中" + messageSuffix
	case 0:
		check.Status = model.Critical
		check.FailureCode = "reported_outage"
		check.Detail = jimuAITargetModelName + " 最近探测失败" + messageSuffix
	default:
		check.FailureCode = "unsupported_status"
		check.Detail = jimuAITargetModelName + " 返回未知状态"
	}
	return check
}

func (c *Collector) probeOpenAI(ctx context.Context, source ModelSource, check model.Check) model.Check {
	payload := struct {
		Components []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"components"`
	}{}
	if err := c.getModelSourceJSON(ctx, source.URL, &payload); err != nil {
		return unreadableModelSource(check, source.Name, err)
	}

	var component *struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	matches := 0
	for i := range payload.Components {
		candidate := &payload.Components[i]
		if candidate.ID == openAIConversationsComponentID && candidate.Name == "Conversations" {
			component = candidate
			matches++
		}
	}
	if matches != 1 || component == nil {
		return invalidModelSource(check, "OpenAI 官方状态中没有唯一的 Conversations 组件")
	}

	switch strings.ToLower(strings.TrimSpace(component.Status)) {
	case "operational":
		check.Status = model.Healthy
		check.Detail = "Conversations 官方聚合状态正常；非 " + targetModelName + " 单模型探测"
	case "degraded_performance", "partial_outage", "under_maintenance":
		check.Status = model.Degraded
		check.FailureCode = "reported_degradation"
		check.Detail = "Conversations 官方聚合状态降级；非 " + targetModelName + " 单模型探测"
	case "full_outage", "major_outage":
		check.Status = model.Critical
		check.FailureCode = "reported_outage"
		check.Detail = "Conversations 官方聚合状态中断；非 " + targetModelName + " 单模型探测"
	default:
		check.FailureCode = "unsupported_status"
		check.Detail = "Conversations 官方组件返回未知状态"
	}
	return check
}

func (c *Collector) getModelSourceJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "status-cpa/1.0")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return modelSourceHTTPError{status: resp.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("response is not JSON")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelSourceBody+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxModelSourceBody {
		return errors.New("response body exceeds limit")
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func unreadableModelSource(check model.Check, name string, err error) model.Check {
	check.FailureCode = "source_unreadable"
	var httpErr modelSourceHTTPError
	if errors.As(err, &httpErr) {
		check.Detail = fmt.Sprintf("%s 状态源返回 HTTP %d", name, httpErr.status)
		return check
	}
	check.Detail = "无法读取 " + name + " 模型状态源"
	return check
}

func invalidModelSource(check model.Check, detail string) model.Check {
	check.FailureCode = "source_invalid"
	check.Detail = detail
	return check
}

func boundedModelSourceMessageSuffix(value string) string {
	message := strings.Join(strings.Fields(value), " ")
	if message == "" {
		return ""
	}
	runes := []rune(message)
	if len(runes) > maxModelSourceMessageRunes {
		message = string(runes[:maxModelSourceMessageRunes]) + "..."
	}
	return "：" + message
}

func freshModelSourceTime(value, now time.Time, maxAge time.Duration) bool {
	return !value.IsZero() && !value.Before(now.Add(-maxAge)) && !value.After(now.Add(time.Minute))
}
