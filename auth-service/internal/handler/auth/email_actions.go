package authhandler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"platform.local/common/pkg/httputil"
	platformlogger "platform.local/platform/logger"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

var errUserNotFound = errors.New("user not found")

const (
	msgPasswordResetEmailSent = "If an account exists with this email, a password reset link has been sent"
)

// executeKeycloakAdminRequest performs an HTTP request to Keycloak Admin API with the admin token
func (a *AuthHandler) executeKeycloakAdminRequest(c *gin.Context, log *logrus.Entry, method, url string, body []byte, adminToken string, errorMessage string) bool {
	var req *http.Request
	if body != nil {
		req, _ = http.NewRequest(method, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, url, nil)
	}
	req.Header.Set("Authorization", authorizationPrefix+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Errorf("http request failed: %v", err)
		httputil.InternalError(c, errorMessage)
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Errorf("keycloak returned status %d: %s", resp.StatusCode, string(respBody))
		return false
	}

	return true
}

// validateEmailAndGetUserID validates the email request and fetches the admin token and user ID
func (a *AuthHandler) validateEmailAndGetUserID(c *gin.Context, log *logrus.Entry) (string, string, bool) {
	var request struct {
		Email string `json:"email" binding:"required"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		httputil.BadRequest(c, "email is required")
		return "", "", false
	}

	adminToken, err := a.getAdminAccessToken()
	if err != nil {
		log.Errorf("failed to get admin token: %v", err)
		httputil.InternalError(c, "failed to authenticate with auth server")
		return "", "", false
	}

	userID, err := a.findUserIDByEmail(request.Email, adminToken)
	if err != nil {
		return "", "", false
	}

	return userID, adminToken, true
}

func (a *AuthHandler) ResendVerificationEmail(c *gin.Context) {
	log := platformlogger.ForComponent("auth:resend_verification")

	userID, adminToken, ok := a.validateEmailAndGetUserID(c, log)
	if !ok {
		log.Warn(errUserNotFound.Error())
		httputil.NotFound(c, errUserNotFound.Error())
		return
	}

	log.Infof("sending verification email for user: %s", userID)

	sendVerificationEmailURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/send-verify-email",
		a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, userID)

	params := url.Values{}
	params.Add("client_id", a.cfg.Auth.ClientID)
	params.Add("lifespan", "86400")
	params.Add("redirect_uri", a.cfg.FrontendURL+"/login?verified=true")
	sendVerificationEmailURL = sendVerificationEmailURL + "?" + params.Encode()

	if !a.executeKeycloakAdminRequest(c, log, "PUT", sendVerificationEmailURL, nil, adminToken, "failed to send verification email") {
		httputil.InternalError(c, "failed to send verification email")
		return
	}

	log.Infof("verification email sent successfully for user: %s", userID)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Verification email sent successfully",
	})
}

func (a *AuthHandler) ForgotPassword(c *gin.Context) {
	log := platformlogger.ForComponent("auth:forgot_password")

	userID, adminToken, ok := a.validateEmailAndGetUserID(c, log)
	if !ok {
		log.Warn(errUserNotFound.Error())
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": msgPasswordResetEmailSent,
		})
		return
	}

	log.Infof("sending password reset email for user: %s", userID)

	executeActionsURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/execute-actions-email",
		a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, userID)

	params := url.Values{}
	params.Add("client_id", a.cfg.Auth.ClientID)
	params.Add("lifespan", "86400")
	executeActionsURL = executeActionsURL + "?" + params.Encode()

	actionsPayload := []string{"UPDATE_PASSWORD"}
	jsonData, _ := json.Marshal(actionsPayload)

	if !a.executeKeycloakAdminRequest(c, log, "PUT", executeActionsURL, jsonData, adminToken, "failed to send password reset email") {
		// Still return success message for security
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": msgPasswordResetEmailSent,
		})
		return
	}

	log.Infof("password reset email sent successfully for user: %s", userID)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": msgPasswordResetEmailSent,
	})
}

// getAdminAccessToken gets an admin access token using client credentials
func (a *AuthHandler) getAdminAccessToken() (string, error) {
	if a.adminTokenProvider == nil {
		return "", fmt.Errorf("admin token provider not initialized")
	}
	return a.adminTokenProvider.GetToken()
}

// findUserIDByEmail finds a user's ID in Keycloak by email
func (a *AuthHandler) findUserIDByEmail(email, adminToken string) (string, error) {
	searchURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s&exact=true",
		a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, email)

	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("Authorization", authorizationPrefix+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to search user: status %d", resp.StatusCode)
	}

	var users []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", err
	}

	if len(users) == 0 {
		return "", errUserNotFound
	}

	return users[0].ID, nil
}

