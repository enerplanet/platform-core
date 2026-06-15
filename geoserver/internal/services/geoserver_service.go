package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"platform.local/common/pkg/models"
	"platform.local/platform/logger"

	"gorm.io/gorm"
)

const (
	riskStyleClassified   = "fire_risk_classified"
	riskStyleModeratePlus = "fire_risk_moderate_plus"
	riskStyleHighPlus     = "fire_risk_high_plus"
	riskStyleVeryLow      = "fire_risk_level_1"
	riskStyleLow          = "fire_risk_level_2"
	riskStyleModerate     = "fire_risk_level_3"
	riskStyleHigh         = "fire_risk_level_4"
	riskStyleVeryHigh     = "fire_risk_level_5"
)

type riskStyleDefinition struct {
	name          string
	title         string
	visibleValues []int
}

var riskStyleDefinitions = []riskStyleDefinition{
	{name: riskStyleClassified, title: "Fire Risk - contextual classified overlay", visibleValues: []int{1, 2, 3, 4, 5}},
	{name: riskStyleModeratePlus, title: "Fire Risk - moderate and above", visibleValues: []int{3, 4, 5}},
	{name: riskStyleHighPlus, title: "Fire Risk - high and above", visibleValues: []int{4, 5}},
	{name: riskStyleVeryLow, title: "Fire Risk - very low", visibleValues: []int{1}},
	{name: riskStyleLow, title: "Fire Risk - low", visibleValues: []int{2}},
	{name: riskStyleModerate, title: "Fire Risk - moderate", visibleValues: []int{3}},
	{name: riskStyleHigh, title: "Fire Risk - high", visibleValues: []int{4}},
	{name: riskStyleVeryHigh, title: "Fire Risk - very high", visibleValues: []int{5}},
}

type GeoServerService struct {
	db              *gorm.DB
	client          *http.Client
	baseURL         string
	username        string
	password        string
	workspace       string
	containerMount  string
	riskStylesMu    sync.Mutex
	riskStylesReady bool
}

func NewGeoServerService(db *gorm.DB) *GeoServerService {
	log := logger.ForComponent("geoserver")

	baseURL := os.Getenv("GEOSERVER_BASE_URL")
	if baseURL == "" {
		// Default to the docker-compose service name instead of localhost
		baseURL = "http://geoserver:8080/geoserver"
	}

	username := os.Getenv("GEOSERVER_USERNAME")
	if username == "" {
		username = "admin"
	}

	password := os.Getenv("GEOSERVER_PASSWORD")
	if password == "" {
		password = "geoserver"
	}

	containerMount := os.Getenv("GEOSERVER_CONTAINER_MOUNT")
	if containerMount == "" {
		// Use a path relative to GeoServer data_dir so "file:data/..." works without external resource permissions
		containerMount = "data/results"
	}

	log.Infof("GeoServer service initialized: base_url=%s username=%s container_mount=%s", baseURL, username, containerMount)

	return &GeoServerService{
		db:             db,
		client:         &http.Client{Timeout: 30 * time.Second},
		baseURL:        baseURL,
		username:       username,
		password:       password,
		workspace:      "fire_risk",
		containerMount: containerMount,
	}
}

// ConfigureResult provisions GeoServer resources for a model result ID.
func (s *GeoServerService) ConfigureResult(ctx context.Context, resultID uint) error {
	result, err := s.fetchResult(resultID)
	if err != nil {
		return err
	}
	return s.CreateLayer(ctx, result)
}

// DeleteResultLayer removes GeoServer artifacts tied to a result.
func (s *GeoServerService) DeleteResultLayer(ctx context.Context, resultID uint) error {
	result, err := s.fetchResult(resultID)
	if err != nil {
		return err
	}
	return s.DeleteLayer(ctx, result)
}

// GetBoundsForResult fetches cached bounds for the GeoServer layer belonging to the result.
func (s *GeoServerService) GetBoundsForResult(resultID uint) (map[string]interface{}, error) {
	result, err := s.fetchResult(resultID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureRiskStylesReady(); err != nil {
		logger.ForComponent("geoserver").Warnf("failed to provision risk styles while reading bounds result_id=%d err=%v", resultID, err)
	}
	return s.GetLayerBounds(result)
}

// SampleResult is the full output of a sampling pass: bucketed pixel counts
// plus the number of valid (non-nodata) and total attempted samples so the
// caller can compute the analyzed-area fraction.
type SampleResult struct {
	Distribution map[string]int `json:"distribution"`
	ValidSamples int            `json:"valid_samples"`
	TotalSamples int            `json:"total_samples"`
}

// GridSample is one valid raster sample point from the GeoServer layer.
type GridSample struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Value  float64 `json:"value"`
	Level  string  `json:"level"`
	Row    int     `json:"row"`
	Column int     `json:"column"`
}

// GridSampleResult contains geographically positioned raster samples for
// frontend heatmap and choropleth visualizations.
type GridSampleResult struct {
	Bounds       map[string]interface{} `json:"bounds"`
	GridSize     int                    `json:"grid_size"`
	Samples      []GridSample           `json:"samples"`
	ValidSamples int                    `json:"valid_samples"`
	TotalSamples int                    `json:"total_samples"`
}

// SampleDistribution builds a raster distribution by sampling GeoServer pixels.
// Deprecated: callers should prefer SampleDistributionDetailed which also
// reports how many of the sampled pixels were valid (non-nodata).
func (s *GeoServerService) SampleDistribution(ctx context.Context, resultID uint, sampleCount int) (map[string]int, error) {
	res, err := s.SampleDistributionDetailed(ctx, resultID, sampleCount)
	if err != nil {
		return nil, err
	}
	return res.Distribution, nil
}

// SampleDistributionDetailed returns the bucketed distribution alongside the
// valid- and total-sample counts. The valid/total ratio approximates the
// fraction of the layer bounding box that contains real (non-nodata) pixels,
// which is required to compute an accurate analyzed surface area.
func (s *GeoServerService) SampleDistributionDetailed(ctx context.Context, resultID uint, sampleCount int) (*SampleResult, error) {
	_ = ctx
	result, err := s.fetchResult(resultID)
	if err != nil {
		return nil, err
	}
	dist, valid, total, err := s.sampleLayerDistribution(result, sampleCount)
	if err != nil {
		return nil, err
	}
	return &SampleResult{Distribution: dist, ValidSamples: valid, TotalSamples: total}, nil
}

// SampleGridDetailed returns positioned raster samples. It is intentionally
// smaller than the metrics sample by default because every cell requires a
// GeoServer GetFeatureInfo request.
func (s *GeoServerService) SampleGridDetailed(ctx context.Context, resultID uint, sampleCount int) (*GridSampleResult, error) {
	result, err := s.fetchResult(resultID)
	if err != nil {
		return nil, err
	}
	return s.sampleLayerGrid(ctx, result, sampleCount)
}

// doGeoServerRequest performs an HTTP request with basic auth and error handling
func (s *GeoServerService) doGeoServerRequest(method, url string, body io.Reader, acceptedCodes ...int) error {
	return s.doGeoServerRequestWithHeaders(method, url, body, nil, acceptedCodes...)
}

// doGeoServerRequestWithHeaders performs an HTTP request with basic auth, custom headers, and error handling
func (s *GeoServerService) doGeoServerRequestWithHeaders(method, url string, body io.Reader, headers map[string]string, acceptedCodes ...int) error {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return err
	}

	req.SetBasicAuth(s.username, s.password)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	// Check if status code is in accepted list
	if len(acceptedCodes) > 0 {
		accepted := false
		for _, code := range acceptedCodes {
			if resp.StatusCode == code {
				accepted = true
				break
			}
		}
		if !accepted {
			respBody, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("geoserver error: %d %s", resp.StatusCode, string(respBody))
		}
	} else if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		errStr := string(respBody)

		// Handle "already exists" errors which might come as 500 or 409
		if resp.StatusCode == http.StatusConflict ||
			strings.Contains(errStr, "already exists") ||
			strings.Contains(errStr, "already exist") {
			return fmt.Errorf("resource already exists: %s", errStr)
		}

		return fmt.Errorf("geoserver error: %d %s", resp.StatusCode, errStr)
	}

	return nil
}

// doJSONRequest sends a JSON request to GeoServer with the provided payload and validates the response
func (s *GeoServerService) doJSONRequest(method, url string, payload interface{}, acceptedCodes ...int) error {
	body, _ := json.Marshal(payload)
	headers := map[string]string{"Content-Type": "application/json"}
	return s.doGeoServerRequestWithHeaders(method, url, bytes.NewReader(body), headers, acceptedCodes...)
}

func (s *GeoServerService) CreateLayer(ctx context.Context, result *models.ModelResult) error {
	log := logger.ForComponent("geoserver")

	if result.ExtractionStatus != models.ResultExtractionCompleted {
		return fmt.Errorf("extraction not completed")
	}

	s.db.Model(result).Update("geoserver_status", models.ResultGeoserverProcessing)

	if err := s.ensureWorkspaceExists(); err != nil {
		log.Warnf("failed to create workspace, may already exist: %v", err)
	}

	if err := s.ensureRiskStylesReady(); err != nil {
		log.Warnf("failed to provision risk styles result_id=%d err=%v", result.ID, err)
	}

	layerName := fmt.Sprintf("model_%d", result.ModelID)
	storeName := fmt.Sprintf("%s_store", layerName)

	relativePath := result.TifFilePath
	if strings.Contains(relativePath, "storage/data/") {
		parts := strings.Split(relativePath, "storage/data/")
		if len(parts) > 1 {
			relativePath = parts[1]
		}
	}

	containerPath := fmt.Sprintf("%s/%s", s.containerMount, relativePath)
	log.Debugf("creating layer result_id=%d path=%s", result.ID, containerPath)

	// Try to create coverage store
	if err := s.createCoverageStore(storeName, containerPath); err != nil {
		// If it already exists, try to delete and recreate
		if strings.Contains(err.Error(), "resource already exists") || strings.Contains(err.Error(), "already exists") {
			log.Warnf("store %s already exists, deleting and recreating...", storeName)

			// Delete existing layer and store first
			_ = s.deleteLayer(layerName)
			_ = s.deleteCoverageStore(storeName)

			// Retry creation
			if err := s.createCoverageStore(storeName, containerPath); err != nil {
				log.Errorf("failed to recreate coverage store result_id=%d err=%v", result.ID, err)
				s.db.Model(result).Updates(map[string]interface{}{
					"error_message": err.Error(),
				})
				return err
			}
		} else {
			log.Errorf("failed to create coverage store result_id=%d err=%v", result.ID, err)
			s.db.Model(result).Updates(map[string]interface{}{
				"error_message": err.Error(),
			})
			return err
		}
	}

	if err := s.publishLayer(storeName, layerName); err != nil {
		log.Errorf("failed to publish layer result_id=%d err=%v", result.ID, err)
		s.db.Model(result).Updates(map[string]interface{}{
			"geoserver_status": models.ResultGeoserverFailed,
			"error_message":    err.Error(),
		})
		return err
	}

	if err := s.applyStyleToLayer(layerName); err != nil {
		log.Warnf("failed to apply style to layer result_id=%d err=%v", result.ID, err)
	}

	s.db.Model(result).Updates(map[string]interface{}{
		"geoserver_workspace":  s.workspace,
		"geoserver_layer_name": layerName,
		"geoserver_store_name": storeName,
		"geoserver_status":     models.ResultGeoserverConfigured,
	})

	log.Debugf("layer created result_id=%d layer=%s", result.ID, layerName)
	return nil
}

func (s *GeoServerService) createCoverageStore(storeName, filePath string) error {
	log := logger.ForComponent("geoserver")
	url := fmt.Sprintf("%s/rest/workspaces/%s/coveragestores", s.baseURL, s.workspace)

	fileURL := fmt.Sprintf("file:%s", filePath)

	payload := map[string]interface{}{
		"coverageStore": map[string]interface{}{
			"name":      storeName,
			"type":      "GeoTIFF",
			"enabled":   true,
			"workspace": map[string]string{"name": s.workspace},
			"url":       fileURL,
		},
	}

	err := s.doJSONRequest(http.MethodPost, url, payload)
	if err != nil {
		log.Errorf("failed to create coverage store: store=%s path=%s err=%v", storeName, filePath, err)
	}
	return err
}

func (s *GeoServerService) publishLayer(storeName, layerName string) error {
	log := logger.ForComponent("geoserver")
	url := fmt.Sprintf("%s/rest/workspaces/%s/coveragestores/%s/coverages", s.baseURL, s.workspace, storeName)

	payload := map[string]interface{}{
		"coverage": map[string]interface{}{
			"name":        layerName,
			"title":       layerName,
			"description": "Fire risk assessment layer",
			"enabled":     true,
			"srs":         "EPSG:4326",
		},
	}

	err := s.doJSONRequest(http.MethodPost, url, payload)
	if err != nil {
		log.Errorf("failed to publish layer: store=%s layer=%s err=%v", storeName, layerName, err)
	}
	return err
}

func (s *GeoServerService) DeleteLayer(ctx context.Context, result *models.ModelResult) error {
	log := logger.ForComponent("geoserver")

	if result.GeoserverLayerName == "" {
		return nil
	}

	if err := s.deleteLayer(result.GeoserverLayerName); err != nil {
		log.Warnf("failed to delete layer result_id=%d layer=%s err=%v", result.ID, result.GeoserverLayerName, err)
	}

	if result.GeoserverStoreName != "" {
		if err := s.deleteCoverageStore(result.GeoserverStoreName); err != nil {
			log.Warnf("failed to delete store result_id=%d store=%s err=%v", result.ID, result.GeoserverStoreName, err)
		}
	}

	return nil
}

// deleteResource is a helper function to perform DELETE requests to GeoServer
func (s *GeoServerService) deleteResource(url string) error {
	return s.doGeoServerRequest(http.MethodDelete, url, nil, http.StatusOK, http.StatusNoContent, http.StatusNotFound)
}

func (s *GeoServerService) deleteLayer(layerName string) error {
	url := fmt.Sprintf("%s/rest/layers/%s:%s", s.baseURL, s.workspace, layerName)
	return s.deleteResource(url)
}

func (s *GeoServerService) deleteCoverageStore(storeName string) error {
	url := fmt.Sprintf("%s/rest/workspaces/%s/coveragestores/%s?recurse=true", s.baseURL, s.workspace, storeName)
	return s.deleteResource(url)
}

func (s *GeoServerService) GetWMSUrl(result *models.ModelResult) string {
	if result.GeoserverStatus != models.ResultGeoserverConfigured {
		return ""
	}
	appURL := os.Getenv("APP_URL")
	if appURL == "" {
		appURL = "http://localhost:8000"
	}
	return fmt.Sprintf("%s/api/geoserver-proxy/%s/wms", appURL, s.workspace)
}

func (s *GeoServerService) GetLayerName(result *models.ModelResult) string {
	if result.GeoserverLayerName == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", s.workspace, result.GeoserverLayerName)
}

func (s *GeoServerService) ensureWorkspaceExists() error {
	url := fmt.Sprintf("%s/rest/workspaces", s.baseURL)

	payload := map[string]interface{}{
		"workspace": map[string]string{
			"name": s.workspace,
		},
	}

	// Accept Created, OK, Conflict (already exists), and UnprocessableEntity (already exists)
	return s.doJSONRequest(http.MethodPost, url, payload, http.StatusCreated, http.StatusOK, http.StatusConflict, http.StatusUnprocessableEntity)
}

func (s *GeoServerService) applyStyleToLayer(layerName string) error {
	styleName := riskStyleClassified
	url := fmt.Sprintf("%s/rest/layers/%s:%s", s.baseURL, s.workspace, layerName)

	payload := map[string]interface{}{
		"layer": map[string]interface{}{
			"defaultStyle": map[string]string{
				"name": styleName,
			},
		},
	}

	return s.doJSONRequest(http.MethodPut, url, payload, http.StatusOK)
}

func (s *GeoServerService) ensureRiskStyles() error {
	var errs []string
	for _, def := range riskStyleDefinitions {
		if err := s.upsertSLDStyle(def.name, buildRiskStyleSLD(def)); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", def.name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("risk style provisioning failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (s *GeoServerService) ensureRiskStylesReady() error {
	s.riskStylesMu.Lock()
	defer s.riskStylesMu.Unlock()

	if s.riskStylesReady {
		return nil
	}
	if err := s.ensureRiskStyles(); err != nil {
		return err
	}
	s.riskStylesReady = true
	return nil
}

func (s *GeoServerService) upsertSLDStyle(styleName, sld string) error {
	stylePath := url.PathEscape(styleName)
	updateURL := fmt.Sprintf("%s/rest/workspaces/%s/styles/%s", s.baseURL, s.workspace, stylePath)
	headers := map[string]string{"Content-Type": "application/vnd.ogc.sld+xml"}

	if err := s.doGeoServerRequestWithHeaders(http.MethodPut, updateURL, strings.NewReader(sld), headers, http.StatusOK, http.StatusCreated, http.StatusNoContent); err == nil {
		return nil
	}

	createURL := fmt.Sprintf("%s/rest/workspaces/%s/styles?name=%s", s.baseURL, s.workspace, url.QueryEscape(styleName))
	if err := s.doGeoServerRequestWithHeaders(http.MethodPost, createURL, strings.NewReader(sld), headers, http.StatusCreated, http.StatusOK); err != nil {
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "409") {
			return s.doGeoServerRequestWithHeaders(http.MethodPut, updateURL, strings.NewReader(sld), headers, http.StatusOK, http.StatusCreated, http.StatusNoContent)
		}
		return err
	}
	return nil
}

type riskColorMapEntry struct {
	quantity int
	label    string
	color    string
	opacity  float64
}

func buildRiskStyleSLD(def riskStyleDefinition) string {
	// Classes render at full strength; transparency is applied once, client-side, by the opacity slider.
	entries := []riskColorMapEntry{
		{quantity: 0, label: "No data", color: "#000000", opacity: 0},
		{quantity: 1, label: "Very Low", color: "#2563eb", opacity: 1},
		{quantity: 2, label: "Low", color: "#16a34a", opacity: 1},
		{quantity: 3, label: "Moderate", color: "#eab308", opacity: 1},
		{quantity: 4, label: "High", color: "#ea580c", opacity: 1},
		{quantity: 5, label: "Very High", color: "#dc2626", opacity: 1},
	}

	visibleValues := make(map[int]bool, len(def.visibleValues))
	for _, value := range def.visibleValues {
		visibleValues[value] = true
	}

	var colorMap strings.Builder
	for _, entry := range entries {
		opacity := entry.opacity
		if entry.quantity > 0 && !visibleValues[entry.quantity] {
			opacity = 0
		}
		colorMap.WriteString(fmt.Sprintf(
			`          <ColorMapEntry color="%s" quantity="%d" label="%s" opacity="%.2f"/>`+"\n",
			entry.color,
			entry.quantity,
			entry.label,
			opacity,
		))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<StyledLayerDescriptor version="1.0.0"
  xmlns="http://www.opengis.net/sld"
  xmlns:ogc="http://www.opengis.net/ogc"
  xmlns:xlink="http://www.w3.org/1999/xlink"
  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"
  xsi:schemaLocation="http://www.opengis.net/sld StyledLayerDescriptor.xsd">
  <NamedLayer>
    <Name>%s</Name>
    <UserStyle>
      <Title>%s</Title>
      <FeatureTypeStyle>
        <Rule>
          <RasterSymbolizer>
            <Opacity>1.0</Opacity>
            <ColorMap type="values">
%s            </ColorMap>
          </RasterSymbolizer>
        </Rule>
      </FeatureTypeStyle>
    </UserStyle>
  </NamedLayer>
</StyledLayerDescriptor>
`, def.name, def.title, colorMap.String())
}

func (s *GeoServerService) GetLayerBounds(result *models.ModelResult) (map[string]interface{}, error) {
	if result.GeoserverLayerName == "" {
		return nil, fmt.Errorf("no layer name")
	}

	// Try coverage endpoint first
	if bounds := s.tryGetCoverageBounds(result); bounds != nil {
		return bounds, nil
	}

	// Fallback to layer endpoint
	return s.getLayerBounds(result)
}

func (s *GeoServerService) tryGetCoverageBounds(result *models.ModelResult) map[string]interface{} {
	if result.GeoserverStoreName == "" {
		return nil
	}

	covURL := fmt.Sprintf("%s/rest/workspaces/%s/coveragestores/%s/coverages/%s.json",
		s.baseURL, s.workspace, result.GeoserverStoreName, result.GeoserverLayerName)

	coverage, err := s.fetchCoverageInfo(covURL)
	if err != nil {
		return nil
	}

	return extractBoundsFromCoverage(coverage)
}

func (s *GeoServerService) fetchCoverageInfo(url string) (map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(s.username, s.password)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var cov map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&cov); err != nil {
		return nil, err
	}

	return cov, nil
}

func extractBoundsFromCoverage(cov map[string]interface{}) map[string]interface{} {
	coverage, ok := cov["coverage"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Try latLonBoundingBox first (EPSG:4326)
	if bounds := tryExtractLatLonBounds(coverage); bounds != nil {
		return bounds
	}

	// Fallback to nativeBoundingBox
	return tryExtractNativeBounds(coverage)
}

func tryExtractLatLonBounds(coverage map[string]interface{}) map[string]interface{} {
	llb, ok := coverage["latLonBoundingBox"].(map[string]interface{})
	if !ok {
		return nil
	}

	bounds := parseBBox(llb)
	if bounds == nil {
		return nil
	}

	bounds["crs"] = "EPSG:4326"
	return bounds
}

func tryExtractNativeBounds(coverage map[string]interface{}) map[string]interface{} {
	nbb, ok := coverage["nativeBoundingBox"].(map[string]interface{})
	if !ok {
		return nil
	}

	bounds := parseBBox(nbb)
	if bounds == nil {
		return nil
	}

	if crs := extractCRS(nbb["crs"]); crs != "" {
		bounds["crs"] = crs
	}

	return bounds
}

func (s *GeoServerService) getLayerBounds(result *models.ModelResult) (map[string]interface{}, error) {
	url := fmt.Sprintf("%s/rest/layers/%s:%s.json", s.baseURL, s.workspace, result.GeoserverLayerName)

	layerInfo, err := s.fetchLayerInfo(url)
	if err != nil {
		return nil, err
	}

	bboxMap := extractBBoxFromLayer(layerInfo)
	if bboxMap == nil {
		return nil, fmt.Errorf("no bounding box info found for layer")
	}

	bounds := parseBBox(bboxMap)
	if bounds == nil {
		return nil, fmt.Errorf("failed to parse bounding box")
	}

	if crs := extractCRS(bboxMap["crs"]); crs != "" {
		bounds["crs"] = crs
	}

	return bounds, nil
}

func (s *GeoServerService) fetchLayerInfo(url string) (map[string]interface{}, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(s.username, s.password)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get layer info: %d", resp.StatusCode)
	}

	var layerInfo map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&layerInfo); err != nil {
		return nil, err
	}

	return layerInfo, nil
}

func extractBBoxFromLayer(layerInfo map[string]interface{}) map[string]interface{} {
	layer, ok := layerInfo["layer"].(map[string]interface{})
	if !ok {
		return nil
	}

	resource, ok := layer["resource"].(map[string]interface{})
	if !ok {
		return nil
	}

	nbb, ok := resource["nativeBoundingBox"].(map[string]interface{})
	if !ok {
		return nil
	}

	return nbb
}

// parseBBox extracts minx/miny/maxx/maxy from a GeoServer bbox JSON object
func parseBBox(obj map[string]interface{}) map[string]interface{} {
	get := func(key string) (float64, bool) {
		if v, ok := obj[key]; ok {
			switch t := v.(type) {
			case float64:
				return t, true
			case json.Number:
				if f, err := t.Float64(); err == nil {
					return f, true
				}
			case string:
				if f, err := strconv.ParseFloat(t, 64); err == nil {
					return f, true
				}
			}
		}
		return 0, false
	}
	minx, ok1 := get("minx")
	miny, ok2 := get("miny")
	maxx, ok3 := get("maxx")
	maxy, ok4 := get("maxy")
	if !(ok1 && ok2 && ok3 && ok4) {
		return nil
	}
	return map[string]interface{}{
		"minx": minx,
		"miny": miny,
		"maxx": maxx,
		"maxy": maxy,
	}
}

// extractCRS normalizes various GeoServer CRS representations
func extractCRS(crsField interface{}) string {
	switch t := crsField.(type) {
	case string:
		return t
	case map[string]interface{}:
		if v, ok := t["$"].(string); ok {
			return v
		}
		if v, ok := t["value"].(string); ok {
			return v
		}
	}
	return ""
}

func (s *GeoServerService) fetchResult(resultID uint) (*models.ModelResult, error) {
	var result models.ModelResult
	if err := s.db.First(&result, resultID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("result not found: %w", err)
		}
		return nil, err
	}
	return &result, nil
}

func (s *GeoServerService) sampleLayerDistribution(result *models.ModelResult, sampleCount int) (map[string]int, int, int, error) {
	log := logger.ForComponent("geoserver")

	distribution := map[string]int{
		"very_low":  0,
		"low":       0,
		"moderate":  0,
		"high":      0,
		"very_high": 0,
	}

	bounds, err := s.GetLayerBounds(result)
	if err != nil {
		return nil, 0, 0, err
	}

	minx, ok1 := bounds["minx"].(float64)
	maxx, ok2 := bounds["maxx"].(float64)
	miny, ok3 := bounds["miny"].(float64)
	maxy, ok4 := bounds["maxy"].(float64)
	if !(ok1 && ok2 && ok3 && ok4) {
		return nil, 0, 0, fmt.Errorf("invalid bounds data")
	}

	if sampleCount <= 0 {
		sampleCount = 2000
	}

	gridSize := int(math.Sqrt(float64(sampleCount)))
	if gridSize < 1 {
		gridSize = 1
	}

	stepX := (maxx - minx) / float64(gridSize)
	stepY := (maxy - miny) / float64(gridSize)

	totalAttempted := gridSize * gridSize
	validSamples := 0

	for i := 0; i < gridSize; i++ {
		for j := 0; j < gridSize; j++ {
			x := minx + float64(i)*stepX + stepX/2
			y := miny + float64(j)*stepY + stepY/2

			value, err := s.getPixelValue(result, x, y)
			if err != nil {
				continue
			}
			if value == 0 {
				continue
			}

			level := riskLevelFromScore(value)
			distribution[level]++
			validSamples++
		}
	}

	log.Debugf("sampleLayerDistribution result_id=%d samples=%d valid=%d total=%d", result.ID, sampleCount, validSamples, totalAttempted)
	return distribution, validSamples, totalAttempted, nil
}

func (s *GeoServerService) sampleLayerGrid(ctx context.Context, result *models.ModelResult, sampleCount int) (*GridSampleResult, error) {
	log := logger.ForComponent("geoserver")

	bounds, err := s.GetLayerBounds(result)
	if err != nil {
		return nil, err
	}

	minx, ok1 := bounds["minx"].(float64)
	maxx, ok2 := bounds["maxx"].(float64)
	miny, ok3 := bounds["miny"].(float64)
	maxy, ok4 := bounds["maxy"].(float64)
	if !(ok1 && ok2 && ok3 && ok4) {
		return nil, fmt.Errorf("invalid bounds data")
	}

	if sampleCount <= 0 {
		sampleCount = 625
	}

	gridSize := int(math.Sqrt(float64(sampleCount)))
	if gridSize < 1 {
		gridSize = 1
	}

	stepX := (maxx - minx) / float64(gridSize)
	stepY := (maxy - miny) / float64(gridSize)

	totalAttempted := gridSize * gridSize
	samples := make([]GridSample, 0, totalAttempted)

	for row := 0; row < gridSize; row++ {
		for column := 0; column < gridSize; column++ {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			x := minx + float64(column)*stepX + stepX/2
			y := miny + float64(row)*stepY + stepY/2

			value, err := s.getPixelValue(result, x, y)
			if err != nil {
				continue
			}
			if value == 0 {
				continue
			}

			samples = append(samples, GridSample{
				X:      x,
				Y:      y,
				Value:  value,
				Level:  riskLevelFromScore(value),
				Row:    row,
				Column: column,
			})
		}
	}

	log.Debugf("sampleLayerGrid result_id=%d samples=%d valid=%d total=%d", result.ID, sampleCount, len(samples), totalAttempted)
	return &GridSampleResult{
		Bounds:       bounds,
		GridSize:     gridSize,
		Samples:      samples,
		ValidSamples: len(samples),
		TotalSamples: totalAttempted,
	}, nil
}

func (s *GeoServerService) getPixelValue(result *models.ModelResult, x, y float64) (float64, error) {
	log := logger.ForComponent("geoserver")
	url := fmt.Sprintf("%s/wms?SERVICE=WMS&VERSION=1.1.0&REQUEST=GetFeatureInfo"+
		"&LAYERS=%s:%s&QUERY_LAYERS=%s:%s"+
		"&STYLES=&BBOX=%f,%f,%f,%f&WIDTH=101&HEIGHT=101"+
		"&X=50&Y=50&SRS=EPSG:4326&INFO_FORMAT=application/json",
		s.baseURL,
		s.workspace, result.GeoserverLayerName,
		s.workspace, result.GeoserverLayerName,
		x-0.01, y-0.01, x+0.01, y+0.01)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(s.username, s.password)

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var featureInfo map[string]interface{}
	if err := json.Unmarshal(body, &featureInfo); err != nil {
		return 0, err
	}

	features, ok := featureInfo["features"].([]interface{})
	if !ok || len(features) == 0 {
		return 0, fmt.Errorf("no features returned")
	}

	feature, ok := features[0].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("invalid feature payload")
	}

	properties, ok := feature["properties"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("no properties in feature")
	}

	for key, val := range properties {
		keyLower := strings.ToLower(key)
		if keyLower == "geometry" || keyLower == "x" || keyLower == "y" ||
			keyLower == "lon" || keyLower == "lat" || keyLower == "the_geom" {
			continue
		}

		switch v := val.(type) {
		case float64:
			return s.normalizePixelValue(v), nil
		case string:
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				return s.normalizePixelValue(parsed), nil
			}
		}
	}

	log.Warnf("getPixelValue at (%f,%f): no numeric property found", x, y)
	return 0, fmt.Errorf("no numeric property found")
}

func (s *GeoServerService) normalizePixelValue(value float64) float64 {
	return value
}

func riskLevelFromScore(value float64) string {
	rounded := math.Round(value)

	switch int(rounded) {
	case 0, 1:
		return "very_low"
	case 2:
		return "low"
	case 3:
		return "moderate"
	case 4:
		return "high"
	case 5:
		return "very_high"
	default:
		if rounded < 1.5 {
			return "very_low"
		} else if rounded < 2.5 {
			return "low"
		} else if rounded < 3.5 {
			return "moderate"
		} else if rounded < 4.5 {
			return "high"
		}
		return "very_high"
	}
}
