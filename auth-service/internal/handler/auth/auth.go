package authhandler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"platform.local/auth-service/internal/config"
	"platform.local/auth-service/internal/middleware"
	"platform.local/auth-service/internal/store"
	"platform.local/common/pkg/constants"
	"platform.local/common/pkg/httputil"
	utils "platform.local/common/pkg/utils"
	"platform.local/platform/auth"
	platformkeycloak "platform.local/platform/keycloak"
	platformlogger "platform.local/platform/logger"
	platformsession "platform.local/platform/session"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

const (
	keycloakUserURLFormat = "%s/admin/realms/%s/users/%s"
	authorizationPrefix   = "Bearer "
)

type AuthHandler struct {
	cfg                *config.Config
	serverAddr         string
	authClient         *auth.Client
	authStore          store.AuthStore
	sessionStore       platformsession.SessionStore
	adminTokenProvider *auth.AdminTokenProvider
	httpClient         *http.Client
	loginLockout       LoginLockout
	kc                 *platformkeycloak.Client
}

type LoginLockout interface {
	RecordFailedAttempt(identifier string) (shouldLock bool, lockedUntil time.Time, attemptsRemaining int)
	IsLocked(identifier string) (bool, time.Time)
	ResetAttempts(identifier string)
	GetAttemptCount(identifier string) int
}

type keycloakUserDetails struct {
	ID              string   `json:"id"`
	Email           string   `json:"email"`
	EmailVerified   bool     `json:"emailVerified"`
	RequiredActions []string `json:"requiredActions"`
}

func generateSecureSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("session_%d", time.Now().UnixNano())
	}
	return base64.URLEncoding.EncodeToString(b)
}

func (a *AuthHandler) setSecureCookie(c *gin.Context, name, value string, maxAge int, httpOnly bool) {
	httputil.SetAuthCookie(c, a.cookieOptions(), name, value, maxAge, httpOnly)
}

func (a *AuthHandler) setLoginCookies(c *gin.Context, sessionID, email string) {
	cookieMaxAge := a.cfg.SessionTTLMinutes * 60
	a.clearAuthCookies(c)
	a.setSecureCookie(c, "session_id", sessionID, cookieMaxAge, true)
	a.setSecureCookie(c, "user_email", email, cookieMaxAge, false)
	a.setSecureCookie(c, "csrf_token", middleware.GenerateCSRFToken(), cookieMaxAge, false)
}

func (a *AuthHandler) clearAuthCookies(c *gin.Context) {
	httputil.ClearAuthCookies(c, a.cookieOptions())
}

func (a *AuthHandler) cookieOptions() httputil.CookieOptions {
	return httputil.CookieOptions{
		Domain:       a.cfg.CookieDomain,
		IsProduction: a.cfg.AppEnv == "production",
	}
}

func New(cfg *config.Config, serverAddr string, authClient *auth.Client, authStore store.AuthStore, sessionStore platformsession.SessionStore, adminTokenProvider *auth.AdminTokenProvider, loginLockout LoginLockout) *AuthHandler {
	return &AuthHandler{
		cfg:                cfg,
		serverAddr:         serverAddr,
		authClient:         authClient,
		authStore:          authStore,
		sessionStore:       sessionStore,
		adminTokenProvider: adminTokenProvider,
		loginLockout:       loginLockout,
		httpClient:         utils.NewDefaultHTTPClient(),
		kc:                 platformkeycloak.NewClient(cfg.Auth.BaseURL, cfg.Auth.Realm, adminTokenProvider),
	}
}

// fetchUserGroupAndAccessLevel fetches user's primary group and determines access level
func (a *AuthHandler) fetchUserGroupAndAccessLevel(userID string) (accessLevel, groupID string) {
	userData, err := a.getUserDataFromKeycloak(userID)
	if err != nil {
		platformlogger.WithFields(map[string]any{
			"component": "auth",
			"user_id":   userID,
			"error":     err.Error(),
		}).Error("CRITICAL: Failed to fetch user data from Keycloak - defaulting to very_low access level")
		return constants.AccessLevelVeryLow, ""
	}

	accessLevel = a.extractAccessLevel(userData.Attributes)

	platformlogger.WithFields(map[string]any{
		"component":    "auth",
		"user_id":      userID,
		"access_level": accessLevel,
	}).Debug("Fetched user access level from Keycloak")

	if accessLevel == constants.AccessLevelManager {
		groupID = a.assignManagerToGroup(userID, userData)
	}

	return accessLevel, groupID
}

// getUserDataFromKeycloak fetches user data from Keycloak with retry logic
func (a *AuthHandler) getUserDataFromKeycloak(userID string) (*struct {
	Email      string              `json:"email"`
	Attributes map[string][]string `json:"attributes"`
}, error) {
	const maxRetries = 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		userData, err := a.doGetUserDataFromKeycloak(userID)
		if err == nil {
			return userData, nil
		}
		lastErr = err
		platformlogger.WithFields(map[string]any{
			"component": "auth",
			"user_id":   userID,
			"attempt":   attempt,
			"error":     err.Error(),
		}).Warn("Failed to fetch user data from Keycloak, retrying...")

		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt*100) * time.Millisecond)
		}
	}

	return nil, fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

// doGetUserDataFromKeycloak performs the actual Keycloak API call
func (a *AuthHandler) doGetUserDataFromKeycloak(userID string) (*struct {
	Email      string              `json:"email"`
	Attributes map[string][]string `json:"attributes"`
}, error) {
	adminToken, err := a.adminTokenProvider.GetToken()
	if err != nil {
		platformlogger.WithFields(map[string]any{"component": "auth", "user_id": userID}).Warn("failed to get admin token")
		return nil, err
	}

	userURL := fmt.Sprintf(keycloakUserURLFormat, a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, userID)
	req, err := http.NewRequest("GET", userURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authorizationPrefix+adminToken)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			a.adminTokenProvider.Invalidate()
		}
		return nil, fmt.Errorf("keycloak returned status %d", resp.StatusCode)
	}

	var userData struct {
		Email      string              `json:"email"`
		Attributes map[string][]string `json:"attributes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userData); err != nil {
		return nil, err
	}

	return &userData, nil
}

// extractAccessLevel extracts and normalizes the access level from user attributes
func (a *AuthHandler) extractAccessLevel(attributes map[string][]string) string {
	if levels, ok := attributes["access_level"]; ok && len(levels) > 0 {
		return strings.ToLower(levels[0])
	}
	platformlogger.WithFields(map[string]any{
		"component":  "auth",
		"attributes": attributes,
	}).Warn("access_level attribute not found in Keycloak user attributes - defaulting to very_low")
	return constants.AccessLevelVeryLow
}

// assignManagerToGroup assigns a manager to their default group or returns existing group
func (a *AuthHandler) assignManagerToGroup(userID string, userData *struct {
	Email      string              `json:"email"`
	Attributes map[string][]string `json:"attributes"`
}) string {
	managerName := userData.Email
	if names, ok := userData.Attributes["fullName"]; ok && len(names) > 0 {
		managerName = names[0]
	}

	defID, err := a.kc.EnsureManagerDefaultGroup(context.Background(), userID, userData.Email, managerName)
	if err == nil && defID != "" {
		return defID
	}

	return a.getManagerFallbackGroup(userID)
}

// getManagerFallbackGroup retrieves the first available group for a manager
func (a *AuthHandler) getManagerFallbackGroup(userID string) string {
	groups, err := a.kc.GetUserGroups(context.Background(), userID)
	if err != nil || len(groups) == 0 {
		return ""
	}

	groupID := groups[0].ID
	return groupID
}

func (a *AuthHandler) RefreshToken(c *gin.Context) {
	sessionData, ok := httputil.GetSessionOrAbort(c, a.sessionStore)
	if !ok {
		return
	}

	sessionID := httputil.GetSessionCookieOrEmpty(c)
	if sessionData.RefreshToken == "" {
		platformlogger.WithFields(map[string]any{"component": "auth", "session_id": sessionID}).Warn("no refresh token in session")
		httputil.Unauthorized(c, "No refresh token available")
		return
	}

	tokenSource := a.authClient.Oauth.TokenSource(context.Background(), &oauth2.Token{
		RefreshToken: sessionData.RefreshToken,
	})

	newToken, err := tokenSource.Token()
	if err != nil {
		platformlogger.WithFields(map[string]any{"component": "auth", "session_id": sessionID, "err": err}).Error("failed to refresh token")
		httputil.Unauthorized(c, "Failed to refresh token")
		return
	}

	sessionData.AccessToken = newToken.AccessToken
	if newToken.RefreshToken != "" {
		sessionData.RefreshToken = newToken.RefreshToken
	}
	sessionData.TokenExpiresAt = newToken.Expiry

	if err := a.sessionStore.SaveSession(c, sessionID, sessionData); err != nil {
		platformlogger.WithFields(map[string]any{"component": "auth", "session_id": sessionID, "err": err}).Error("failed to save refreshed session")
		httputil.InternalError(c, "Failed to save session")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Token refreshed successfully",
	})
}

// tokenRefreshThreshold is how far before expiry we proactively refresh the
// Keycloak access token. The frontend keep-alive pings every 4-5 minutes, so
// a 10-minute window gives us at least one chance to refresh before expiry.
const tokenRefreshThreshold = 10 * time.Minute

// KeepAlive refreshes the Keycloak access token proactively if it is close to
// expiring. The Redis session TTL is already extended by SessionRefreshMiddleware,
// so this handler only needs to handle the Keycloak token side.
func (a *AuthHandler) KeepAlive(c *gin.Context) {
	sessionData, ok := httputil.GetSessionOrAbort(c, a.sessionStore)
	if !ok {
		return
	}

	sessionID := httputil.GetSessionCookieOrEmpty(c)

	// If we know when the token expires and it's not close to expiring, skip refresh.
	if !sessionData.TokenExpiresAt.IsZero() && time.Until(sessionData.TokenExpiresAt) > tokenRefreshThreshold {
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	// Token is expiring soon (or we don't know when it expires) — refresh it.
	if sessionData.RefreshToken == "" {
		platformlogger.WithFields(map[string]any{
			"component":  "auth",
			"session_id": sessionID,
		}).Warn("keep-alive: no refresh token available")
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	tokenSource := a.authClient.Oauth.TokenSource(context.Background(), &oauth2.Token{
		RefreshToken: sessionData.RefreshToken,
	})

	newToken, err := tokenSource.Token()
	if err != nil {
		platformlogger.WithFields(map[string]any{
			"component":  "auth",
			"session_id": sessionID,
			"err":        err,
		}).Warn("keep-alive: failed to refresh Keycloak token")
		// Don't fail the keep-alive — the session is still valid in Redis.
		// The reactive refresh in the axios interceptor will handle it if needed.
		c.JSON(http.StatusOK, gin.H{"success": true})
		return
	}

	sessionData.AccessToken = newToken.AccessToken
	if newToken.RefreshToken != "" {
		sessionData.RefreshToken = newToken.RefreshToken
	}
	sessionData.TokenExpiresAt = newToken.Expiry

	if err := a.sessionStore.SaveSession(c, sessionID, sessionData); err != nil {
		platformlogger.WithFields(map[string]any{
			"component":  "auth",
			"session_id": sessionID,
			"err":        err,
		}).Error("keep-alive: failed to save refreshed session")
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (a *AuthHandler) GetTourStatus(c *gin.Context) {
	sessionData, ok := httputil.GetSessionOrAbort(c, a.sessionStore)
	if !ok {
		return
	}
	sessionID := httputil.GetSessionCookieOrEmpty(c)
	if !sessionData.ProductTourCompleted {
		if kcVal, kerr := a.fetchTourAttributeFromKeycloak(sessionID); kerr == nil && kcVal {
			sessionData.ProductTourCompleted = true
			_ = a.sessionStore.SaveSession(c, sessionID, sessionData)
		}
	}
	c.JSON(http.StatusOK, gin.H{"completed": sessionData.ProductTourCompleted})
}

func (a *AuthHandler) CompleteTour(c *gin.Context) {
	sessionData, ok := httputil.GetSessionOrAbort(c, a.sessionStore)
	if !ok {
		return
	}
	sessionID := httputil.GetSessionCookieOrEmpty(c)
	if sessionData.ProductTourCompleted {
		c.JSON(http.StatusOK, gin.H{"status": "already_completed"})
		return
	}
	sessionData.ProductTourCompleted = true
	if err := a.sessionStore.SaveSession(c, sessionID, sessionData); err != nil {
		httputil.InternalError(c, "failed to persist status")
		return
	}
	_ = a.updateKeycloakUserTourAttribute(sessionID, true)
	c.JSON(http.StatusOK, gin.H{"status": "completed"})
}

func (a *AuthHandler) Login(c *gin.Context) {
	var loginData struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&loginData); err != nil {
		httputil.BadRequestWithDetails(c, "invalid request body", err.Error())
		return
	}

	if a.checkLoginLockout(c, loginData.Email) {
		return
	}

	tokenResponse, errorHandled := a.authenticateWithKeycloak(c, loginData.Email, loginData.Password)
	if tokenResponse == nil {
		// Only call handleFailedLogin if the specific error wasn't already handled
		if !errorHandled {
			a.handleFailedLogin(c, loginData.Email)
		}
		return
	}

	userInfo, ok := a.fetchUserInfo(c, tokenResponse.AccessToken)
	if !ok {
		return
	}

	if !a.verifyEmailStatus(c, userInfo) {
		return
	}

	sessionID := a.createUserSession(c, userInfo, tokenResponse)
	if sessionID == "" {
		return
	}

	if a.loginLockout != nil {
		a.loginLockout.ResetAttempts(loginData.Email)
	}

	a.sendLoginSuccessResponse(c, userInfo, sessionID)
}

// checkLoginLockout verifies if account is locked; returns true if locked (and response sent)
func (a *AuthHandler) checkLoginLockout(c *gin.Context, email string) bool {
	if a.loginLockout == nil {
		return false
	}
	isLocked, lockedUntil := a.loginLockout.IsLocked(email)
	if !isLocked {
		return false
	}

	remainingTime := time.Until(lockedUntil)

	httputil.ErrorResponse(c, http.StatusTooManyRequests, fmt.Sprintf(
		"Account temporarily locked due to too many failed login attempts. Please try again in %d minutes.",
		int(remainingTime.Minutes())+1,
	))
	return true
}

// authenticateWithKeycloak performs Keycloak token authentication
// Returns: (tokenResponse, errorHandled)
// - On success: (tokenResponse, false)
// - On handled error: (nil, true) - specific error response already sent
// - On unhandled error: (nil, false) - caller should send generic error
func (a *AuthHandler) authenticateWithKeycloak(c *gin.Context, email, password string) (*struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}, bool) {
	tokenURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", a.cfg.Auth.BaseURL, a.cfg.Auth.Realm)
	formData := url.Values{}
	formData.Set("grant_type", "password")
	formData.Set("client_id", a.cfg.Auth.ClientID)
	formData.Set("client_secret", a.cfg.Auth.ClientSecret)
	formData.Set("username", email)
	formData.Set("password", password)
	formData.Set("scope", "openid profile email")

	encodedForm := formData.Encode()
	platformlogger.WithFields(map[string]any{
		"component":    "auth",
		"token_url":    tokenURL,
		"email":        email,
		"password_len": len(password),
		"encoded_len":  len(encodedForm),
	}).Debug("Keycloak token request")

	resp, err := a.httpClient.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(encodedForm))
	if err != nil {
		platformlogger.WithFields(map[string]any{"component": "auth", "error": err}).Error("failed to connect to auth server")
		httputil.BadGateway(c, "failed to connect to auth server")
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		handled := a.handleKeycloakAuthError(c, resp, email)
		return nil, handled
	}

	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		httputil.InternalError(c, "failed to parse token response")
		return nil, true // Error handled
	}

	return &tokenResponse, false // Success, no error to handle
}

// Returns true if the error was handled
func (a *AuthHandler) handleKeycloakAuthError(c *gin.Context, resp *http.Response, email string) bool {
	body, _ := io.ReadAll(resp.Body)

	platformlogger.WithFields(map[string]any{
		"component": "auth",
		"email":     email,
		"status":    resp.StatusCode,
		"body":      string(body),
	}).Warn("Keycloak auth error response")

	var errorResponse struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &errorResponse); err == nil {
		errorDescLower := strings.ToLower(errorResponse.ErrorDescription)

		if a.isDisabledAccountError(errorDescLower) {
			httputil.ErrorResponse(c, http.StatusForbidden, "Your account has been disabled. Please contact an administrator for assistance.")
			return true
		}

		if a.isUnverifiedEmailError(errorDescLower) {
			httputil.ErrorResponse(c, http.StatusForbidden, "Please verify your email address before signing in. Check your inbox for the verification email.")
			return true
		}
	}

	// Keycloak often returns generic "Invalid user credentials" for unverified accounts
	// Check if user exists and has VERIFY_EMAIL required action
	if a.checkUserRequiresEmailVerification(email) {
		httputil.ErrorResponse(c, http.StatusForbidden, "Please verify your email address before signing in. Check your inbox for the verification email.")
		return true
	}

	// Error not specifically handled
	return false
}

// checkUserRequiresEmailVerification checks if a user has unverified email or VERIFY_EMAIL required action
func (a *AuthHandler) checkUserRequiresEmailVerification(email string) bool {
	adminToken, err := a.adminTokenProvider.GetToken()
	if err != nil {
		platformlogger.WithFields(map[string]any{"component": "auth", "email": email, "error": err}).Warn("Failed to get admin token for email verification check")
		return false
	}

	// Find user by email
	checkUserURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s&exact=true", a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, url.QueryEscape(email))
	req, err := http.NewRequest("GET", checkUserURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", authorizationPrefix+adminToken)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var users []keycloakUserDetails
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil || len(users) == 0 {
		return false
	}

	user := users[0]

	// Check if email is not verified
	if !user.EmailVerified {
		platformlogger.WithFields(map[string]any{"component": "auth", "email": email}).Debug("User email not verified")
		return true
	}

	// Check if VERIFY_EMAIL is in required actions
	for _, action := range user.RequiredActions {
		if strings.EqualFold(action, "VERIFY_EMAIL") {
			platformlogger.WithFields(map[string]any{"component": "auth", "email": email}).Debug("User has VERIFY_EMAIL required action")
			return true
		}
	}

	return false
}

// isDisabledAccountError checks if error indicates disabled account
func (a *AuthHandler) isDisabledAccountError(errorDesc string) bool {
	return strings.Contains(errorDesc, "disabled") ||
		strings.Contains(errorDesc, "account is disabled") ||
		strings.Contains(errorDesc, "user is disabled")
}

// isUnverifiedEmailError checks if error indicates unverified email
func (a *AuthHandler) isUnverifiedEmailError(errorDesc string) bool {
	return (strings.Contains(errorDesc, "email") && strings.Contains(errorDesc, "verify")) ||
		strings.Contains(errorDesc, "not verified") ||
		strings.Contains(errorDesc, "account is not fully set up")
}

// handleFailedLogin handles failed authentication attempt with lockout logic
func (a *AuthHandler) handleFailedLogin(c *gin.Context, email string) {
	if a.loginLockout == nil {
		httputil.Unauthorized(c, "Invalid email or password")
		return
	}

	shouldLock, _, attemptsRemaining := a.loginLockout.RecordFailedAttempt(email)
	if shouldLock {
		httputil.ErrorResponse(c, http.StatusTooManyRequests, "Too many failed login attempts. Account locked for 15 minutes.")
		return
	}

	if attemptsRemaining <= 2 && attemptsRemaining > 0 {
		httputil.ErrorResponse(c, http.StatusUnauthorized, fmt.Sprintf(
			"Invalid email or password. %d attempts remaining before account lockout.",
			attemptsRemaining,
		))
		return
	}

	httputil.Unauthorized(c, "Invalid email or password")
}

// fetchUserInfo retrieves user information from Keycloak
func (a *AuthHandler) fetchUserInfo(c *gin.Context, accessToken string) (*struct {
	Sub            string         `json:"sub"`
	Email          string         `json:"email"`
	EmailVerified  bool           `json:"email_verified"`
	FullName       string         `json:"fullName"`
	RealmAccess    map[string]any `json:"realm_access"`
	ResourceAccess map[string]any `json:"resource_access"`
	AccessLevel    string         `json:"access_level"`
}, bool) {
	userInfoURL := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/userinfo", a.cfg.Auth.BaseURL, a.cfg.Auth.Realm)
	req, _ := http.NewRequest("GET", userInfoURL, nil)
	req.Header.Set("Authorization", authorizationPrefix+accessToken)

	userResp, err := a.httpClient.Do(req)
	if err != nil {
		platformlogger.WithFields(map[string]any{"component": "auth", "error": err}).Error("failed to get user info")
		httputil.BadGateway(c, "failed to get user info")
		return nil, false
	}
	defer func() { _ = userResp.Body.Close() }()

	if userResp.StatusCode != http.StatusOK {
		platformlogger.WithFields(map[string]any{
			"component":   "auth",
			"status_code": userResp.StatusCode,
		}).Error("failed to get user info from auth server")
		httputil.InternalError(c, "authentication error")
		return nil, false
	}

	var userInfo struct {
		Sub            string         `json:"sub"`
		Email          string         `json:"email"`
		EmailVerified  bool           `json:"email_verified"`
		FullName       string         `json:"fullName"`
		RealmAccess    map[string]any `json:"realm_access"`
		ResourceAccess map[string]any `json:"resource_access"`
		AccessLevel    string         `json:"access_level"`
	}

	if err := json.NewDecoder(userResp.Body).Decode(&userInfo); err != nil {
		httputil.InternalError(c, "failed to parse user info")
		return nil, false
	}

	return &userInfo, true
}

// verifyEmailStatus checks email verification status
func (a *AuthHandler) verifyEmailStatus(c *gin.Context, userInfo *struct {
	Sub            string         `json:"sub"`
	Email          string         `json:"email"`
	EmailVerified  bool           `json:"email_verified"`
	FullName       string         `json:"fullName"`
	RealmAccess    map[string]any `json:"realm_access"`
	ResourceAccess map[string]any `json:"resource_access"`
	AccessLevel    string         `json:"access_level"`
}) bool {
	if userInfo.EmailVerified {
		return true
	}

	// Always check admin API as fallback since OIDC token may have stale email_verified claim
	fallbackVerified, err := a.isEmailVerifiedViaAdmin(userInfo.Sub)
	if err != nil {
		platformlogger.WithFields(map[string]any{"component": "auth", "email": userInfo.Email, "err": err}).Warn("admin verification fallback failed")
		// On admin API error, allow login to avoid blocking verified users due to transient errors
		return true
	}
	if fallbackVerified {
		userInfo.EmailVerified = true
		return true
	}

	httputil.ErrorResponse(c, http.StatusForbidden, "Please verify your email address before signing in. Check your inbox for the verification email.")
	return false
}

// createUserSession creates a new session for authenticated user
func (a *AuthHandler) createUserSession(c *gin.Context, userInfo *struct {
	Sub            string         `json:"sub"`
	Email          string         `json:"email"`
	EmailVerified  bool           `json:"email_verified"`
	FullName       string         `json:"fullName"`
	RealmAccess    map[string]any `json:"realm_access"`
	ResourceAccess map[string]any `json:"resource_access"`
	AccessLevel    string         `json:"access_level"`
}, tokenResponse *struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}) string {
	userID := userInfo.Sub
	accessLevel, groupID := a.fetchUserGroupAndAccessLevel(userID)

	sessionID := generateSecureSessionID()
	existingSession, _ := a.sessionStore.GetSession(c, sessionID)

	tokenExpiresAt := time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second)

	var sessionData *platformsession.SessionData
	if existingSession != nil {
		existingSession.UserID = userID
		existingSession.AccessToken = tokenResponse.AccessToken
		existingSession.RefreshToken = tokenResponse.RefreshToken
		existingSession.TokenExpiresAt = tokenExpiresAt
		existingSession.AccessLevel = accessLevel
		existingSession.GroupID = groupID
		existingSession.UserInfoData = &platformsession.UserInfoData{
			Email:    userInfo.Email,
			FullName: userInfo.FullName,
		}
		sessionData = existingSession
	} else {
		sessionData = &platformsession.SessionData{
			UserID: userID,
			UserInfoData: &platformsession.UserInfoData{
				Email:    userInfo.Email,
				FullName: userInfo.FullName,
			},
			AccessToken:          tokenResponse.AccessToken,
			RefreshToken:         tokenResponse.RefreshToken,
			TokenExpiresAt:       tokenExpiresAt,
			AccessLevel:          accessLevel,
			GroupID:              groupID,
			ProductTourCompleted: false,
		}
	}

	if err := a.sessionStore.SaveSession(c, sessionID, sessionData); err != nil {
		httputil.InternalError(c, "failed to create session")
		return ""
	}

	a.setLoginCookies(c, sessionID, userInfo.Email)

	return sessionID
}

// sendLoginSuccessResponse sends successful login response
func (a *AuthHandler) sendLoginSuccessResponse(c *gin.Context, userInfo *struct {
	Sub            string         `json:"sub"`
	Email          string         `json:"email"`
	EmailVerified  bool           `json:"email_verified"`
	FullName       string         `json:"fullName"`
	RealmAccess    map[string]any `json:"realm_access"`
	ResourceAccess map[string]any `json:"resource_access"`
	AccessLevel    string         `json:"access_level"`
}, sessionID string) {
	accessLevel, _ := a.fetchUserGroupAndAccessLevel(userInfo.Sub)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Login successful",
		"user": gin.H{
			"id":           userInfo.Sub,
			"email":        userInfo.Email,
			"name":         userInfo.FullName,
			"access_level": accessLevel,
		},
		"session": gin.H{
			"timeout_minutes": a.cfg.SessionTTLMinutes,
		},
		"authenticated": true,
	})
}

func (a *AuthHandler) Logout(c *gin.Context) {
	for _, sessionID := range httputil.GetSessionCookieValues(c) {
		_ = a.sessionStore.DeleteSession(c, sessionID)
	}

	a.clearAuthCookies(c)
	httputil.SuccessMessage(c, "logged out")
}

func (a *AuthHandler) fetchKeycloakUser(userID string, target interface{}) error {
	adminToken, err := a.adminTokenProvider.GetToken()
	if err != nil {
		return err
	}
	urlStr := fmt.Sprintf(keycloakUserURLFormat, a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, url.PathEscape(userID))
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, urlStr, nil)
	req.Header.Set("Authorization", authorizationPrefix+adminToken)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			a.adminTokenProvider.Invalidate()
		}
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("keycloak get user failed: %s", string(body))
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (a *AuthHandler) fetchTourAttributeFromKeycloak(userID string) (bool, error) {
	var userRep struct {
		Attributes map[string][]string `json:"attributes"`
	}
	if err := a.fetchKeycloakUser(userID, &userRep); err != nil {
		return false, err
	}
	if userRep.Attributes == nil {
		return false, nil
	}
	vals := userRep.Attributes["product_tour_completed"]
	if len(vals) == 0 {
		return false, nil
	}
	v := vals[0]
	return v == "true" || v == "1" || v == "yes", nil
}

func (a *AuthHandler) updateKeycloakUserTourAttribute(userID string, completed bool) error {
	adminToken, err := a.adminTokenProvider.GetToken()
	if err != nil {
		return err
	}
	getURL := fmt.Sprintf(keycloakUserURLFormat, a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, url.PathEscape(userID))
	getReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, getURL, nil)
	getReq.Header.Set("Authorization", authorizationPrefix+adminToken)
	getResp, err := a.httpClient.Do(getReq)
	if err != nil {
		return err
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode != http.StatusOK {
		if getResp.StatusCode == http.StatusUnauthorized {
			a.adminTokenProvider.Invalidate()
		}
		body, _ := io.ReadAll(getResp.Body)
		return fmt.Errorf("keycloak fetch for update failed: %s", string(body))
	}
	var userRep map[string]any
	if err := json.NewDecoder(getResp.Body).Decode(&userRep); err != nil {
		return err
	}
	attrs, ok := userRep["attributes"].(map[string]any)
	if !ok || attrs == nil {
		attrs = map[string]any{}
	}
	val := "false"
	if completed {
		val = "true"
	}
	attrs["product_tour_completed"] = []string{val}
	userRep["attributes"] = attrs
	payload, err := json.Marshal(userRep)
	if err != nil {
		return err
	}
	putReq, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, getURL, bytes.NewBuffer(payload))
	putReq.Header.Set("Authorization", authorizationPrefix+adminToken)
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := a.httpClient.Do(putReq)
	if err != nil {
		return err
	}
	defer func() { _ = putResp.Body.Close() }()
	if putResp.StatusCode != http.StatusNoContent {
		if putResp.StatusCode == http.StatusUnauthorized {
			a.adminTokenProvider.Invalidate()
		}
		body, _ := io.ReadAll(putResp.Body)
		return fmt.Errorf("keycloak update failed: %s", string(body))
	}
	return nil
}
func (a *AuthHandler) isEmailVerifiedViaAdmin(userID string) (bool, error) {
	var details keycloakUserDetails
	if err := a.fetchKeycloakUser(userID, &details); err != nil {
		return false, err
	}
	return details.EmailVerified, nil
}
