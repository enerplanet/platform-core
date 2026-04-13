package proxy

import (
	"io"
	"net/http"
	"net/url"
	"os"

	"platform.local/platform/logger"

	"github.com/gin-gonic/gin"
)

type GeoServerProxyHandler struct {
	geoserverBaseURL string
}

func NewGeoServerProxyHandler() *GeoServerProxyHandler {
	baseURL := os.Getenv("GEOSERVER_BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8280/geoserver"
	}
	return &GeoServerProxyHandler{
		geoserverBaseURL: baseURL,
	}
}

func (h *GeoServerProxyHandler) ProxyWMS(c *gin.Context) {
	log := logger.ForComponent("geoserver-proxy")

	workspace := c.Param("workspace")
	if workspace == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspace parameter required"})
		return
	}

	// Parse base URL and properly construct target URL with escaped path
	baseURL, err := url.Parse(h.geoserverBaseURL)
	if err != nil {
		log.Errorf("invalid geoserver base URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "invalid proxy configuration"})
		return
	}

	// Safely append path segments
	baseURL.Path = baseURL.Path + "/" + url.PathEscape(workspace) + "/wms"

	// Add query parameters
	queryParams := c.Request.URL.Query()
	if len(queryParams) > 0 {
		baseURL.RawQuery = queryParams.Encode()
	}

	req, err := http.NewRequest(c.Request.Method, baseURL.String(), c.Request.Body)
	if err != nil {
		log.Errorf("failed to create proxy request: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create proxy request"})
		return
	}

	for key, values := range c.Request.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("proxy request failed: %v", err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "geoserver request failed"})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	headersToSkip := map[string]bool{
		"Access-Control-Allow-Origin":      true,
		"Access-Control-Allow-Credentials": true,
		"Access-Control-Allow-Methods":     true,
		"Access-Control-Allow-Headers":     true,
		"Access-Control-Expose-Headers":    true,
	}

	for key, values := range resp.Header {
		if headersToSkip[key] {
			continue
		}
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}

	c.Writer.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		log := logger.ForComponent("geoserver-proxy")
		log.Errorf("failed to stream response body: %v", err)
	}
}
