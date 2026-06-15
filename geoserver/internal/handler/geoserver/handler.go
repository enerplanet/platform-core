package geoserver

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"spatialhub_geoserver/internal/services"
)

// Handler exposes HTTP endpoints for GeoServer control plane operations consumed by the backend API.
type Handler struct {
	service *services.GeoServerService
}

// NewHandler wires a GeoServer service backed by the shared database connection.
func NewHandler(svc *services.GeoServerService) *Handler {
	return &Handler{service: svc}
}

// ConfigureResult triggers GeoServer layer creation for the provided model result ID.
func (h *Handler) ConfigureResult(c *gin.Context) {
	resultID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := h.service.ConfigureResult(c.Request.Context(), resultID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "configured"})
}

// DeleteLayer removes GeoServer resources for the model result.
func (h *Handler) DeleteLayer(c *gin.Context) {
	resultID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := h.service.DeleteResultLayer(c.Request.Context(), resultID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Status(http.StatusNoContent)
}

// GetBounds returns the cached bounding box for a GeoServer layer.
func (h *Handler) GetBounds(c *gin.Context) {
	resultID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	bounds, err := h.service.GetBoundsForResult(resultID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"bounds": bounds})
}

type sampleDistributionRequest struct {
	SampleCount int `json:"sample_count"`
}

type sampleGridRequest struct {
	SampleCount int `json:"sample_count"`
}

// SampleDistribution computes a raster distribution by sampling GeoServer pixels.
func (h *Handler) SampleDistribution(c *gin.Context) {
	resultID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req sampleDistributionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.SampleCount <= 0 {
		req.SampleCount = 2000
	}

	res, err := h.service.SampleDistributionDetailed(c.Request.Context(), resultID, req.SampleCount)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"distribution":  res.Distribution,
		"valid_samples": res.ValidSamples,
		"total_samples": res.TotalSamples,
	})
}

// SampleGrid returns positioned raster samples for frontend geo charts.
func (h *Handler) SampleGrid(c *gin.Context) {
	resultID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req sampleGridRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.SampleCount <= 0 {
		req.SampleCount = 625
	}

	res, err := h.service.SampleGridDetailed(c.Request.Context(), resultID, req.SampleCount)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, res)
}

func parseUintParam(c *gin.Context, key string) (uint, bool) {
	value := c.Param(key)
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return 0, false
	}
	return uint(parsed), true
}
