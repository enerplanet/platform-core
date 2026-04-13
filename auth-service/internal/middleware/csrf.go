package middleware

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	httpsScheme    = "https"
	csrfHeaderName = "X-CSRF-Token"
)

// CSRFConfig holds CSRF middleware configuration
type CSRFConfig struct {
	CookieDomain        string
	CookieMaxAge        int
	IsProduction        bool
	EnableRotation      bool          // Enable per-request token rotation
	RotationGracePeriod time.Duration // Grace period for accepting old tokens
}

var csrfConfig *CSRFConfig

func SetCSRFConfig(config *CSRFConfig) {
	csrfConfig = config
}

func CSRFMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isReadOnlyMethod(c.Request.Method) {
			c.Next()
			return
		}

		if err := validateCSRFToken(c); err != nil {
			c.Abort()
			return
		}

		c.Set("csrf_old_token", c.GetHeader(csrfHeaderName))
		c.Next()

		rotateCSRFTokenIfNeeded(c)
	}
}

func isReadOnlyMethod(method string) bool {
	return method == "GET" || method == "HEAD" || method == "OPTIONS"
}

func validateCSRFToken(c *gin.Context) error {
	csrfCookie, err := c.Cookie("csrf_token")
	if err != nil || csrfCookie == "" {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "CSRF token missing",
			"code":  "CSRF_TOKEN_MISSING",
		})
		return fmt.Errorf("csrf token missing in cookie")
	}

	csrfHeader := c.GetHeader(csrfHeaderName)
	if csrfHeader == "" {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "CSRF token missing in request header",
			"code":  "CSRF_TOKEN_MISSING",
		})
		return fmt.Errorf("csrf token missing in header")
	}

	if csrfCookie != csrfHeader {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "CSRF token validation failed",
			"code":  "CSRF_TOKEN_INVALID",
		})
		return fmt.Errorf("csrf token mismatch")
	}

	return nil
}

func rotateCSRFTokenIfNeeded(c *gin.Context) {
	if csrfConfig == nil || !csrfConfig.EnableRotation || c.Writer.Status() >= 400 {
		return
	}

	newToken := GenerateCSRFToken()
	domain := getCookieDomain()
	isSecure := determineSecure(c)

	// HttpOnly is intentionally set to false for CSRF tokens
	// because JavaScript must read this cookie to include it in the X-CSRF-Token header.
	// This is required for the double-submit cookie CSRF protection pattern.
	cookie := &http.Cookie{
		Name:     "csrf_token",
		Value:    newToken,
		MaxAge:   csrfConfig.CookieMaxAge,
		Path:     "/",
		Domain:   domain,
		Secure:   isSecure,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(c.Writer, cookie)
	c.Header(csrfHeaderName, newToken)
}

func getCookieDomain() string {
	domain := csrfConfig.CookieDomain
	if domain == "localhost" || domain == "" {
		return ""
	}
	return domain
}

func determineSecure(c *gin.Context) bool {
	return csrfConfig.IsProduction ||
		c.Request.Header.Get("X-Forwarded-Proto") == httpsScheme ||
		c.Request.TLS != nil
}

func GenerateCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("CSRF: Failed to generate secure random token: " + err.Error())
	}
	return base64.URLEncoding.EncodeToString(b)
}

func SetCSRFTokenCookie(c *gin.Context, token string, config *CSRFConfig) {
	isSecure := config.IsProduction ||
		c.Request.Header.Get("X-Forwarded-Proto") == httpsScheme ||
		c.Request.TLS != nil

	domain := config.CookieDomain
	if domain == "localhost" || domain == "" {
		domain = ""
	}

	// HttpOnly is intentionally set to false for CSRF tokens
	// because JavaScript must read this cookie to include it in the X-CSRF-Token header.
	// This is required for the double-submit cookie CSRF protection pattern.
	cookie := &http.Cookie{
		Name:     "csrf_token",
		Value:    token,
		MaxAge:   config.CookieMaxAge,
		Path:     "/",
		Domain:   domain,
		Secure:   isSecure,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(c.Writer, cookie)
}
