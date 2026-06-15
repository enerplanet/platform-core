package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	apperrors "platform.local/common/pkg/errors"
	"platform.local/common/pkg/models"
	"platform.local/platform/logger"
	"spatialhub_webservice/internal/store"
)

const (
	endpointStatus = "/status"

	// maxResponseBytes limits the size of response bodies read from
	// webservice instances to prevent unbounded memory allocation.
	maxResponseBytes = 10 * 1024 * 1024 // 10 MB
)

// sharedTransport is reused across all WebserviceService instances so that
// idle TCP connections are pooled and reused across scheduler ticks and
// concurrent dispatch workers.
var sharedTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 20,
	IdleConnTimeout:     90 * time.Second,
}

type WebserviceService struct {
	client *http.Client
	repo   *store.WebserviceRepository
	db     *gorm.DB
}

func NewWebserviceService(db *gorm.DB) *WebserviceService {
	return &WebserviceService{
		client: &http.Client{
			Timeout:   0, // no timeout – match old enerplanet behaviour; model fails only on file verification or stuck-model scheduler
			Transport: sharedTransport,
		},
		repo: store.NewWebserviceRepository(db),
		db:   db,
	}
}

func normalizeOutboundIP(ip string) string {
	if ip == "0.0.0.0" || ip == "127.0.0.1" || ip == "localhost" {
		if _, err := os.Stat("/.dockerenv"); err == nil {
			return "host.docker.internal"
		}
		if os.Getenv("DOCKER_HOST") != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			return "host.docker.internal"
		}
		return "127.0.0.1"
	}
	return ip
}

func ensureEndpoint(endpoint string, def string) string {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return def
	}
	if !strings.HasPrefix(ep, "/") {
		ep = "/" + ep
	}
	return ep
}

func buildURL(ws *models.WebserviceInstance, endpoint string) string {
	ip := normalizeOutboundIP(ws.IP)
	return fmt.Sprintf("%s://%s:%d%s", ws.Protocol, ip, ws.Port, endpoint)
}

func (s *WebserviceService) doJSON(ctx context.Context, method, url string, payload interface{}) (int, http.Header, []byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("failed to encode JSON: %w", err)
		}
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		logger.ForComponent("webservice").Errorf("request failed url=%s err=%v", url, err)
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("failed to read response: %w", err)
	}
	return resp.StatusCode, resp.Header, respBytes, nil
}

func (s *WebserviceService) Create(ctx context.Context, ws *models.WebserviceInstance) (*models.WebserviceInstance, error) {
	if err := s.repo.Create(ws); err != nil {
		return nil, err
	}
	return ws, nil
}

func (s *WebserviceService) Get(ctx context.Context, id uint) (*models.WebserviceInstance, error) {
	ws, err := s.repo.Get(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.New("NOT_FOUND", fmt.Sprintf("webservice %d not found", id), 404)
		}
		return nil, err
	}
	return ws, nil
}

func (s *WebserviceService) List(ctx context.Context, f store.WebserviceFilters) (map[string]interface{}, error) {
	list, total, err := s.repo.List(f)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"items": list, "total": total, "page": f.Page, "per_page": f.PerPage}, nil
}

func (s *WebserviceService) Update(ctx context.Context, id uint, updates map[string]interface{}) (*models.WebserviceInstance, error) {
	if err := s.repo.Update(id, updates); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *WebserviceService) Delete(ctx context.Context, id uint) error {
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	return nil
}

func (s *WebserviceService) UpdateStatus(ctx context.Context, id uint, status string, reason string) (*models.WebserviceInstance, error) {
	updates := map[string]interface{}{"status_reason": reason}

	if err := s.repo.UpdateStatus(id, status, updates); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func (s *WebserviceService) ReserveAvailableInstanceTx(ctx context.Context, tx *gorm.DB, cpuThreshold float64) (*models.WebserviceInstance, error) {
	log := logger.ForComponent("webservice")
	var instance models.WebserviceInstance

	// Lock one eligible row and reserve a slot atomically.
	// Skip instances whose last-reported CPU usage exceeds the threshold.
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("status = ? AND available = ? AND current_concurrency < max_concurrency",
			models.StatusActive, true)

	if cpuThreshold > 0 && cpuThreshold < 100 {
		query = query.Where("cpu_usage IS NULL OR cpu_usage < ?", cpuThreshold)
	}

	if err := query.Order("current_concurrency ASC, id ASC").
		First(&instance).Error; err != nil {
		if cpuThreshold > 0 && cpuThreshold < 100 {
			log.Infof("no available webservice found with capacity (cpu_threshold=%.0f%%): %v", cpuThreshold, err)
		} else {
			log.Infof("no available webservice found with capacity: %v", err)
		}
		return nil, err
	}

	updates := map[string]interface{}{
		"current_concurrency": gorm.Expr("current_concurrency + 1"),
		"updated_at":          time.Now(),
	}

	if err := tx.Model(&models.WebserviceInstance{}).
		Where("id = ?", instance.ID).
		Updates(updates).Error; err != nil {
		log.Errorf("failed to increment concurrency for webservice id=%d err=%v", instance.ID, err)
		return nil, err
	}

	instance.CurrentConcurrency++
	return &instance, nil
}

func (s *WebserviceService) CheckHealth(ctx context.Context, id uint) (bool, error) {
	_, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}

	healthy, _, _ := s.Ping(ctx, id)

	now := time.Now()
	updates := map[string]interface{}{
		"last_check": now,
	}

	if healthy {
		updates["status"] = models.StatusActive
		updates["available"] = true
		updates["last_heartbeat"] = now
	} else {
		updates["status"] = models.StatusInactive
		updates["available"] = false
	}

	_ = s.repo.Update(id, updates)
	return healthy, nil
}

func (s *WebserviceService) Ping(ctx context.Context, id uint) (bool, map[string]interface{}, error) {
	ws, err := s.Get(ctx, id)
	if err != nil {
		return false, nil, err
	}

	healthURL := buildURL(ws, "/health")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		available := s.simpleTCPPing(ws)
		return available, nil, nil
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req = req.WithContext(pingCtx)
	resp, err := s.client.Do(req)
	if err != nil {
		available := s.simpleTCPPing(ws)
		return available, nil, nil
	}
	defer func() { _ = resp.Body.Close() }()

	var healthData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&healthData); err != nil {
		return resp.StatusCode == 200, map[string]interface{}{"status": resp.Status}, nil
	}

	return resp.StatusCode == 200, healthData, nil
}

func (s *WebserviceService) simpleTCPPing(ws *models.WebserviceInstance) bool {
	timeout := 3 * time.Second
	ip := normalizeOutboundIP(ws.IP)
	address := net.JoinHostPort(ip, strconv.Itoa(ws.Port))
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	return true
}

func (s *WebserviceService) CheckIfBusy(ctx context.Context, id uint) (bool, error) {
	ws, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}

	log := logger.ForComponent("webservice")
	url := buildURL(ws, endpointStatus)

	respData, err := fetchStatusData(ctx, s.client, url, ws.ID, log)
	if err != nil {
		return ws.Busy, nil
	}

	busy, err := parseStatusResponse(respData, ws.ID, log)
	if err != nil {
		log.Warnf("CheckIfBusy: no valid response, using current DB state id=%d busy=%v", ws.ID, ws.Busy)
		return ws.Busy, nil
	}

	return busy, err
}

func fetchStatusData(ctx context.Context, client *http.Client, url string, wsID uint, log *logrus.Entry) ([]byte, error) {
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(statusCtx, http.MethodGet, url, nil)
	if err != nil {
		log.Warnf("CheckIfBusy: failed to create request id=%d err=%v", wsID, err)
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			log.Debugf("CheckIfBusy: request timeout (webservice may be busy) id=%d url=%s", wsID, url)
		} else {
			log.Warnf("CheckIfBusy: request failed id=%d url=%s err=%v", wsID, url, err)
		}
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Warnf("CheckIfBusy: failed to close response body id=%d err=%v", wsID, cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		log.Warnf("CheckIfBusy: non-200 response id=%d status=%d", wsID, resp.StatusCode)
		return nil, fmt.Errorf("non-200 status")
	}

	data, _ := io.ReadAll(resp.Body)
	if len(data) == 0 {
		log.Warnf("CheckIfBusy: empty response body from endpoint id=%d endpoint=%s", wsID, endpointStatus)
		return nil, fmt.Errorf("empty response")
	}

	return data, nil
}

func parseStatusResponse(data []byte, wsID uint, log *logrus.Entry) (bool, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Warnf("CheckIfBusy: JSON parse failed id=%d err=%v raw_data=%s", wsID, err, string(data))
		return false, err
	}

	switch v := raw.(type) {
	case map[string]any:
		return checkMapStatus(v, wsID, log)
	case []any:
		return checkArrayStatus(v)
	}

	return false, fmt.Errorf("unexpected response type")
}

func checkMapStatus(v map[string]any, wsID uint, log *logrus.Entry) (bool, error) {
	if statusStr, ok := v["status"].(string); ok {
		busy, recognized := parseStatusString(statusStr)
		if recognized {
			if statusStr == "offline" {
				log.Warnf("CheckIfBusy: webservice is OFFLINE id=%d", wsID)
				return false, fmt.Errorf("status offline")
			}
			return busy, nil
		}
	}

	if active, ok := v["active"].(bool); ok {
		return active, nil
	}

	if jobsArr, ok := v["jobs"].([]any); ok {
		return len(jobsArr) > 0, nil
	}

	if jobsMap, ok := v["jobs"].(map[string]any); ok {
		return checkJobsMap(jobsMap), nil
	}

	return false, fmt.Errorf("no valid status field")
}

func parseStatusString(statusStr string) (busy bool, recognized bool) {
	switch strings.ToLower(strings.TrimSpace(statusStr)) {
	case "calculating", "running", "busy", "processing":
		return true, true
	case "online", "idle", "ready", "available":
		return false, true
	case "offline":
		return false, true
	}
	return false, false
}

func checkJobsMap(jobsMap map[string]any) bool {
	for _, job := range jobsMap {
		if isJobRunning(job) {
			return true
		}
	}
	return false
}

func checkArrayStatus(v []any) (bool, error) {
	if len(v) == 0 {
		return false, nil
	}
	for _, item := range v {
		if isJobRunning(item) {
			return true, nil
		}
	}
	return false, nil
}

func isJobRunning(job any) bool {
	if jobData, ok := job.(map[string]any); ok {
		if dur, ok := jobData["Duration"].(float64); ok && dur == 0 {
			return true
		}
		if status, ok := jobData["status"].(string); ok && strings.EqualFold(status, "running") {
			return true
		}
		if end, ok := jobData["EndTime"].(string); !ok || end == "" {
			return true
		}
	}
	return false
}

func (s *WebserviceService) GetSummary(ctx context.Context) (map[string]interface{}, error) {
	var total int64
	s.db.Model(&models.WebserviceInstance{}).Count(&total)

	var active int64
	s.db.Model(&models.WebserviceInstance{}).Where("status = ?", models.StatusActive).Count(&active)

	var available int64
	s.db.Model(&models.WebserviceInstance{}).
		Where("status = ? AND available = ?", models.StatusActive, true).
		Count(&available)

	return map[string]interface{}{
		"total":     total,
		"active":    active,
		"available": available,
	}, nil
}

func (s *WebserviceService) GetAvailableStaticDates(ctx context.Context) ([]string, error) {
	var instance models.WebserviceInstance
	if err := s.db.
		Where("status = ?", models.StatusActive).
		Order("available DESC, current_concurrency ASC, id ASC").
		First(&instance).Error; err != nil {
		return nil, err
	}

	url := buildURL(&instance, "/available-static-dates")
	status, _, respBytes, err := s.doJSON(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("request failed with status %d: %s", status, string(respBytes))
	}

	var result struct {
		Dates []string `json:"dates"`
	}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("invalid available static dates response: %w", err)
	}
	return result.Dates, nil
}

func (s *WebserviceService) GetAvailableDataCoverage(ctx context.Context) (map[string]interface{}, error) {
	var instance models.WebserviceInstance
	if err := s.db.
		Where("status = ?", models.StatusActive).
		Order("available DESC, current_concurrency ASC, id ASC").
		First(&instance).Error; err != nil {
		return nil, err
	}

	url := buildURL(&instance, "/available-data-coverage")
	status, _, respBytes, err := s.doJSON(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("request failed with status %d: %s", status, string(respBytes))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("invalid available data coverage response: %w", err)
	}
	return result, nil
}

func (s *WebserviceService) ReleaseInstance(ctx context.Context, id uint) error {
	log := logger.ForComponent("webservice")

	updates := map[string]interface{}{
		"current_concurrency": gorm.Expr("GREATEST(current_concurrency - 1, 0)"),
		"updated_at":          time.Now(),
	}

	if err := s.repo.Update(id, updates); err != nil {
		log.Errorf("failed to release webservice id=%d err=%v", id, err)
		return err
	}

	log.Debugf("successfully released webservice id=%d (decremented concurrency)", id)
	return nil
}

func (s *WebserviceService) MarkIdle(ctx context.Context, id uint) error {
	updates := map[string]interface{}{
		"busy":       false,
		"available":  true,
		"updated_at": time.Now(),
	}
	return s.repo.Update(id, updates)
}

func (s *WebserviceService) UpdateHeartbeat(ctx context.Context, id uint) error {
	now := time.Now()
	updates := map[string]interface{}{
		"last_heartbeat": now,
		"updated_at":     now,
	}
	return s.repo.Update(id, updates)
}

func (s *WebserviceService) SendJSONRequest(
	ctx context.Context,
	ws *models.WebserviceInstance,
	endpoint string,
	payload interface{},
) (map[string]interface{}, error) {

	ep := ensureEndpoint(endpoint, "/")
	url := buildURL(ws, ep)
	status, _, respBytes, err := s.doJSON(ctx, http.MethodPost, url, payload)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("request failed with status %d: %s", status, string(respBytes))
	}
	var result map[string]interface{}
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	return result, nil
}

func (s *WebserviceService) FetchResourceUsage(ctx context.Context, id uint) (cpuUsage, memUsage *float64, err error) {
	ws, err := s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	log := logger.ForComponent("webservice")
	url := buildURL(ws, endpointStatus)

	respData, err := fetchStatusData(ctx, s.client, url, ws.ID, log)
	if err != nil {
		return nil, nil, err
	}

	var raw map[string]any
	if err := json.Unmarshal(respData, &raw); err != nil {
		return nil, nil, err
	}

	if cpu, ok := raw["cpu_usage"].(float64); ok {
		cpuUsage = &cpu
	} else if cpuPercent, ok := raw["cpu_percent"].(float64); ok {
		cpuUsage = &cpuPercent
	} else if cpuStr, ok := raw["cpu"].(float64); ok {
		cpuUsage = &cpuStr
	}

	if mem, ok := raw["memory_usage"].(float64); ok {
		memUsage = &mem
	} else if memPercent, ok := raw["memory_percent"].(float64); ok {
		memUsage = &memPercent
	} else if memStr, ok := raw["memory"].(float64); ok {
		memUsage = &memStr
	} else if ram, ok := raw["ram"].(float64); ok {
		memUsage = &ram
	}

	log.Debugf("fetched resource usage for webservice id=%d cpu=%v mem=%v", id, cpuUsage, memUsage)
	return cpuUsage, memUsage, nil
}

func (s *WebserviceService) SendCalculationRequest(
	ctx context.Context,
	ws *models.WebserviceInstance,
	endpoint string,
	payload interface{},
) (map[string]interface{}, error) {

	log := logger.ForComponent("webservice")
	ep := ensureEndpoint(endpoint, "/calliope/start")
	url := buildURL(ws, ep)

	log.Debugf("📤 Sending calculation request to %s", url)

	status, headers, respBytes, err := s.doJSON(ctx, http.MethodPost, url, payload)
	if err != nil {
		log.Errorf("request failed url=%s err=%v", url, err)
		return nil, err
	}
	if status != http.StatusOK && status != http.StatusAccepted {
		log.Errorf("request failed url=%s status=%d body=%s", url, status, string(respBytes))
		return nil, fmt.Errorf("request failed with status %d: %s", status, string(respBytes))
	}

	log.Debugf("📥 Response status=%d", status)

	var result map[string]interface{}
	if len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, &result); err != nil {
			log.Debugf("non-JSON response (%d bytes)", len(respBytes))
			result = map[string]interface{}{"raw_response": string(respBytes)}
		}
	} else {
		result = map[string]interface{}{}
	}

	if sessionID := headers.Get("X-Session-ID"); sessionID != "" {
		result["session_id"] = sessionID
	}
	if callbackURL := headers.Get("X-Callback-URL"); callbackURL != "" {
		result["callback_url"] = callbackURL
	}

	return result, nil
}

func (s *WebserviceService) CancelSession(ctx context.Context, ws *models.WebserviceInstance, sessionID string) error {
	log := logger.ForComponent("webservice")

	if sessionID == "" {
		return nil
	}

	url := buildURL(ws, fmt.Sprintf("/cancel/%s", sessionID))

	status, _, respBytes, err := s.doJSON(ctx, http.MethodDelete, url, nil)
	if err != nil {
		log.Warnf("failed to cancel session url=%s err=%v", url, err)
		return err
	}

	if status != http.StatusOK && status != http.StatusNotFound {
		log.Warnf("cancel session returned status=%d body=%s", status, string(respBytes))
		return fmt.Errorf("cancel failed with status %d", status)
	}

	log.Infof("cancelled session session_id=%s webservice_id=%d", sessionID, ws.ID)
	return nil
}
