package handlers

import (
	"fmt"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"platform.local/common/pkg/httputil"
	"platform.local/common/pkg/models"
	"spatialhub_webservice/internal/services"
	"spatialhub_webservice/internal/store"
)

const (
	errInvalidID    = "Invalid ID"
	timestampFormat = "2006-01-02T15:04:05Z07:00"
)

type WebserviceUserDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func validateHost(hostStr string) error {
	if hostStr == "" {
		return fmt.Errorf("host is required")
	}

	// Allow valid IP addresses
	if ip := net.ParseIP(hostStr); ip != nil {
		return nil
	}

	// Allow valid hostnames (e.g. sim-haproxy, my-service.local)
	if len(hostStr) > 253 {
		return fmt.Errorf("hostname too long")
	}
	for _, label := range strings.Split(hostStr, ".") {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("invalid hostname format")
		}
		for i, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || (c == '-' && i > 0 && i < len(label)-1)) {
				return fmt.Errorf("invalid hostname format")
			}
		}
	}
	return nil
}

func (h *WebserviceHandler) buildWebserviceInstance(payload struct {
	Name           *string `json:"name"`
	IP             string  `json:"ip"`
	Port           int     `json:"port"`
	Protocol       string  `json:"protocol"`
	Endpoint       *string `json:"endpoint"`
	AutoScaling    *bool   `json:"auto_scaling"`
	MaxConcurrency *int    `json:"max_concurrency"`
}, userCtx *httputil.UserContext) models.WebserviceInstance {
	ws := models.WebserviceInstance{
		Name:      payload.Name,
		IP:        payload.IP,
		Port:      payload.Port,
		Protocol:  payload.Protocol,
		Endpoint:  payload.Endpoint,
		Status:    models.StatusActive,
		Available: true,
	}
	if payload.AutoScaling != nil {
		ws.AutoScaling = *payload.AutoScaling
	}
	if payload.MaxConcurrency != nil {
		ws.MaxConcurrency = *payload.MaxConcurrency
	}

	// If auto_scaling is disabled, enforce max_concurrency = 1
	if !ws.AutoScaling && ws.MaxConcurrency != 1 {
		ws.MaxConcurrency = 1
	}

	if userCtx.UserID != "" {
		ws.CreatedByID = &userCtx.UserID
	}
	if userCtx.Name != "" {
		ws.CreatedByName = &userCtx.Name
	}
	if userCtx.Email != "" {
		ws.CreatedByEmail = &userCtx.Email
	}

	return ws
}

// requireExpertAndGetID is a helper that validates expert access and parses ID parameter
func requireExpertAndGetID(c *gin.Context) (uint, bool) {
	userCtx, ok := httputil.GetUserContext(c)
	if !ok {
		return 0, false
	}

	if !httputil.RequireExpertAccessFromContext(userCtx, c) {
		return 0, false
	}

	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return 0, false
	}

	return id, true
}

type WebserviceResponseDTO struct {
	ID                 uint               `json:"id"`
	Name               *string            `json:"name"`
	IP                 string             `json:"ip"`
	Port               int                `json:"port"`
	Protocol           string             `json:"protocol"`
	Endpoint           *string            `json:"endpoint"`
	Status             string             `json:"status"`
	Available          bool               `json:"available"`
	Busy               bool               `json:"busy"`
	AutoScaling        bool               `json:"auto_scaling"`
	MaxConcurrency     int                `json:"max_concurrency"`
	CurrentConcurrency int                `json:"current_concurrency"`
	CpuUsage           *float64           `json:"cpu_usage"`
	MemoryUsage        *float64           `json:"memory_usage"`
	User               *WebserviceUserDTO `json:"user"`
	LastCheck          string             `json:"last_check"`
	LastHeartbeat      *string            `json:"last_heartbeat"`
	CreatedAt          string             `json:"created_at"`
	UpdatedAt          string             `json:"updated_at"`
}

func toWebserviceDTO(ws *models.WebserviceInstance) WebserviceResponseDTO {
	dto := WebserviceResponseDTO{
		ID:                 ws.ID,
		Name:               ws.Name,
		IP:                 ws.IP,
		Port:               ws.Port,
		Protocol:           ws.Protocol,
		Endpoint:           ws.Endpoint,
		Status:             ws.Status,
		Available:          ws.Available,
		Busy:               ws.Busy,
		AutoScaling:        ws.AutoScaling,
		MaxConcurrency:     ws.MaxConcurrency,
		CurrentConcurrency: ws.CurrentConcurrency,
		CpuUsage:           ws.CpuUsage,
		MemoryUsage:        ws.MemoryUsage,
		LastCheck:          ws.LastCheck.Format(timestampFormat),
		CreatedAt:          ws.CreatedAt.Format(timestampFormat),
		UpdatedAt:          ws.UpdatedAt.Format(timestampFormat),
	}

	if ws.LastHeartbeat != nil {
		hb := ws.LastHeartbeat.Format(timestampFormat)
		dto.LastHeartbeat = &hb
	}

	if ws.CreatedByID != nil || ws.CreatedByName != nil || ws.CreatedByEmail != nil {
		dto.User = &WebserviceUserDTO{
			ID:    "",
			Name:  "",
			Email: "",
		}
		if ws.CreatedByID != nil {
			dto.User.ID = *ws.CreatedByID
		}
		if ws.CreatedByName != nil {
			dto.User.Name = *ws.CreatedByName
		}
		if ws.CreatedByEmail != nil {
			dto.User.Email = *ws.CreatedByEmail
		}
	}

	return dto
}

type WebserviceHandler struct {
	service *services.WebserviceService
}

func NewWebserviceHandler(db *gorm.DB) *WebserviceHandler {
	return &WebserviceHandler{
		service: services.NewWebserviceService(db),
	}
}

// CreateWebservice creates a new webservice instance
func (h *WebserviceHandler) CreateWebservice(c *gin.Context) {
	userCtx, ok := httputil.GetUserContext(c)
	if !ok {
		return
	}

	if !httputil.RequireExpertAccessFromContext(userCtx, c) {
		return
	}

	var payload struct {
		Name           *string `json:"name"`
		IP             string  `json:"ip"`
		Port           int     `json:"port"`
		Protocol       string  `json:"protocol"`
		Endpoint       *string `json:"endpoint"`
		AutoScaling    *bool   `json:"auto_scaling"`
		MaxConcurrency *int    `json:"max_concurrency"`
	}

	if err := c.ShouldBindJSON(&payload); err != nil {
		httputil.BadRequest(c, "Invalid request")
		return
	}

	if err := validateHost(payload.IP); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	ws := h.buildWebserviceInstance(payload, userCtx)

	result, err := h.service.Create(c.Request.Context(), &ws)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.Created(c, toWebserviceDTO(result))
}

// GetWebserviceByID returns a webservice by ID
func (h *WebserviceHandler) GetWebserviceByID(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}
	result, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, toWebserviceDTO(result))
}

// GetWebserviceList lists webservices with optional filters
func (h *WebserviceHandler) GetWebserviceList(c *gin.Context) {
	pagination := httputil.ParsePagination(c, nil)

	filters := store.WebserviceFilters{
		Status:    c.Query("status"),
		Available: c.Query("available"),
		Busy:      c.Query("busy"),
		Search:    c.Query("search"),
		Page:      pagination.Page,
		PerPage:   pagination.PerPage,
	}

	list, err := h.service.List(c.Request.Context(), filters)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}

	if items, ok := list["items"].([]models.WebserviceInstance); ok {
		dtoList := make([]WebserviceResponseDTO, len(items))
		for i, ws := range items {
			dtoList[i] = toWebserviceDTO(&ws)
		}
		list["items"] = dtoList
	}

	httputil.SuccessResponse(c, list)
}

// UpdateWebservice updates a webservice
func (h *WebserviceHandler) UpdateWebservice(c *gin.Context) {
	id, ok := requireExpertAndGetID(c)
	if !ok {
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		httputil.BadRequest(c, "Invalid request")
		return
	}

	if val, exists := updates["max_concurrency"]; exists {
		switch v := val.(type) {
		case float64:
			updates["max_concurrency"] = int(v)
		case int:
			updates["max_concurrency"] = v
		}
	}

	// If auto_scaling is being disabled, enforce max_concurrency = 1
	if autoScaling, ok := updates["auto_scaling"].(bool); ok && !autoScaling {
		updates["max_concurrency"] = 1
	}

	if raw, exists := updates["capabilities"]; exists {
		if raw == nil {
			updates["capabilities"] = []string{}
		} else if arr, ok := raw.([]interface{}); ok && len(arr) == 0 {
			updates["capabilities"] = []string{}
		}
	}
	updated, err := h.service.Update(c.Request.Context(), id, updates)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, toWebserviceDTO(updated))
}

// DeleteWebservice deletes a webservice
func (h *WebserviceHandler) DeleteWebservice(c *gin.Context) {
	id, ok := requireExpertAndGetID(c)
	if !ok {
		return
	}

	if err := h.service.Delete(c.Request.Context(), id); err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.NoContent(c)
}

// MarkAvailable marks a webservice available
func (h *WebserviceHandler) MarkAvailable(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	updates := map[string]interface{}{
		"available":     true,
		"status_reason": "manually set available",
	}

	updated, err := h.service.Update(c.Request.Context(), id, updates)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, toWebserviceDTO(updated))
}

// MarkUnavailable marks a webservice unavailable
func (h *WebserviceHandler) MarkUnavailable(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	updates := map[string]interface{}{
		"available":     false,
		"status_reason": "manually set unavailable",
	}

	updated, err := h.service.Update(c.Request.Context(), id, updates)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, toWebserviceDTO(updated))
}

// MarkBusy marks a webservice busy (legacy, no longer used with concurrency tracking)
func (h *WebserviceHandler) MarkBusy(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	updates := map[string]interface{}{
		"busy":          true,
		"status_reason": "manually set busy",
	}

	updated, err := h.service.Update(c.Request.Context(), id, updates)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, toWebserviceDTO(updated))
}

// MarkIdle marks a webservice idle (legacy, no longer used with concurrency tracking)
func (h *WebserviceHandler) MarkIdle(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	updates := map[string]interface{}{
		"busy":          false,
		"status_reason": "manually set idle",
	}

	updated, err := h.service.Update(c.Request.Context(), id, updates)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, toWebserviceDTO(updated))
}

func (h *WebserviceHandler) updateStatus(c *gin.Context, status, reason string) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}
	updated, err := h.service.UpdateStatus(c.Request.Context(), id, status, reason)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, toWebserviceDTO(updated))
}

// CheckHealth returns health status for a webservice
func (h *WebserviceHandler) CheckHealth(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}
	okHealth, err := h.service.CheckHealth(c.Request.Context(), id)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, gin.H{"healthy": okHealth})
}

// PingWebservice pings a webservice and returns details
func (h *WebserviceHandler) PingWebservice(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}
	okPing, data, err := h.service.Ping(c.Request.Context(), id)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, gin.H{"available": okPing, "details": data})
}

// SendRequest sends an arbitrary JSON request to a webservice
func (h *WebserviceHandler) SendRequest(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	ws, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}

	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		httputil.BadRequest(c, "Invalid JSON payload")
		return
	}

	endpoint, ok := payload["endpoint"].(string)
	if !ok || endpoint == "" {
		httputil.BadRequest(c, "Missing 'endpoint' field")
		return
	}
	data := payload["data"]

	result, err := h.service.SendJSONRequest(c.Request.Context(), ws, endpoint, data)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}

	httputil.SuccessResponse(c, gin.H{"response": result})
}

// GetSummary returns summary statistics for webservices
func (h *WebserviceHandler) GetSummary(c *gin.Context) {
	summary, err := h.service.GetSummary(c.Request.Context())
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, summary)
}

func (h *WebserviceHandler) GetAvailableStaticDates(c *gin.Context) {
	dates, err := h.service.GetAvailableStaticDates(c.Request.Context())
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, gin.H{"dates": dates})
}

func (h *WebserviceHandler) GetAvailableDataCoverage(c *gin.Context) {
	coverage, err := h.service.GetAvailableDataCoverage(c.Request.Context())
	if err != nil {
		httputil.HandleError(c, err)
		return
	}
	httputil.SuccessResponse(c, coverage)
}

// Heartbeat updates last_heartbeat for a webservice instance
func (h *WebserviceHandler) Heartbeat(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	err := h.service.UpdateHeartbeat(c.Request.Context(), id)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}

	httputil.SuccessResponse(c, gin.H{"message": "heartbeat received"})
}

// ReleaseInstance handles internal release requests coming from the backend.
func (h *WebserviceHandler) ReleaseInstance(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	if err := h.service.ReleaseInstance(c.Request.Context(), id); err != nil {
		httputil.HandleError(c, err)
		return
	}

	httputil.SuccessResponse(c, gin.H{"message": "webservice released"})
}

// CancelSession handles internal cancel session requests from the backend.
func (h *WebserviceHandler) CancelSession(c *gin.Context) {
	id, ok := httputil.ParseUintParam(c, "id", errInvalidID)
	if !ok {
		return
	}

	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || strings.TrimSpace(payload.SessionID) == "" {
		httputil.BadRequest(c, "session_id is required")
		return
	}

	ws, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		httputil.HandleError(c, err)
		return
	}

	if err := h.service.CancelSession(c.Request.Context(), ws, payload.SessionID); err != nil {
		httputil.HandleError(c, err)
		return
	}

	httputil.SuccessResponse(c, gin.H{"message": "session cancelled"})
}
