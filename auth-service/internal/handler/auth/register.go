package authhandler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"platform.local/common/pkg/httputil"
	utils "platform.local/common/pkg/utils"
	platformlogger "platform.local/platform/logger"

	"github.com/gin-gonic/gin"
)

// UserRegistration represents the registration payload
type UserRegistration struct {
	Name                 string `json:"name"`
	Email                string `json:"email"`
	Password             string `json:"password"`
	PasswordConfirmation string `json:"password_confirmation"`
	Organization         string `json:"organization"`
	Position             string `json:"position"`
	Phone                string `json:"phone"`
	AccessLevel          string `json:"access_level"` // very_low | intermediate | expert
}

func (u *UserRegistration) Validate() map[string]string {
	errors := make(map[string]string)
	if u.Name == "" {
		errors["name"] = "name is required"
	}
	if u.Email == "" {
		errors["email"] = "email is required"
	}
	if u.Password == "" {
		errors["password"] = "password is required"
	}
	if u.AccessLevel == "" {
		u.AccessLevel = "very_low" // default
	}
	return errors
}

func (u *UserRegistration) ToKeycloakPayload() map[string]any {
	attributes := map[string][]string{
		"fullName":     {u.Name},
		"organization": {u.Organization},
		"position":     {u.Position},
		"phone":        {u.Phone},
		"access_level": {u.AccessLevel},
	}

	return map[string]any{
		"username":        u.Email,
		"email":           u.Email,
		"enabled":         true,
		"emailVerified":   false,
		"attributes":      attributes,
		"requiredActions": []string{"VERIFY_EMAIL"},
	}
}

func (a *AuthHandler) Register(c *gin.Context) {
	var user UserRegistration
	if err := c.ShouldBindJSON(&user); err != nil {
		httputil.BadRequestWithDetails(c, "invalid request body", err.Error())
		return
	}

	if !a.validateRegistrationInput(c, &user) {
		return
	}

	adminToken, err := a.adminTokenProvider.GetToken()
	if err != nil {
		httputil.BadGateway(c, "failed to get admin token for registration")
		return
	}

	if exists := a.checkUserExists(c, user.Email, adminToken); exists {
		return
	}

	userID, ok := a.createKeycloakUser(c, &user, adminToken)
	if !ok {
		return
	}

	// Set password separately to properly handle special characters
	if err := a.setUserPassword(userID, user.Password, adminToken); err != nil {
		platformlogger.WithFields(map[string]interface{}{
			"component": "auth",
			"user_id":   userID,
			"error":     err,
		}).Error("Failed to set user password")
		httputil.InternalError(c, "failed to set user password")
		return
	}

	a.sendVerificationEmail(userID, adminToken)

	a.sendRegistrationSuccessResponse(c, &user)
}

// validateRegistrationInput validates user registration input including password requirements
func (a *AuthHandler) validateRegistrationInput(c *gin.Context, user *UserRegistration) bool {
	if errors := user.Validate(); len(errors) > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"errors": errors})
		return false
	}

	passwordErrors := utils.ValidatePassword(user.Password)
	if len(passwordErrors) > 0 {
		errMap := make(map[string]string)
		for _, err := range passwordErrors {
			errMap[err.Field] = err.Message
		}
		c.JSON(http.StatusBadRequest, gin.H{"errors": errMap})
		return false
	}

	if isWeak, reason := utils.IsPasswordWeak(user.Password); isWeak {
		c.JSON(http.StatusBadRequest, gin.H{
			"errors": map[string]string{
				"password": reason,
			},
		})
		return false
	}

	if matchErr := utils.ValidatePasswordMatch(user.Password, user.PasswordConfirmation); matchErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"errors": map[string]string{
				matchErr.Field: matchErr.Message,
			},
		})
		return false
	}

	return true
}

// checkUserExists checks if a user with the given email already exists
func (a *AuthHandler) checkUserExists(c *gin.Context, email, adminToken string) bool {
	checkUserURL := fmt.Sprintf("%s/admin/realms/%s/users?email=%s&exact=true", a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, email)
	checkReq, _ := http.NewRequest("GET", checkUserURL, nil)
	checkReq.Header.Set("Authorization", authorizationPrefix+adminToken)

	checkResp, err := a.httpClient.Do(checkReq)
	if err != nil {
		httputil.BadGateway(c, "failed to check existing user")
		return true
	}
	defer func() { _ = checkResp.Body.Close() }()

	if checkResp.StatusCode == http.StatusOK {
		var existingUsers []map[string]any
		if err := json.NewDecoder(checkResp.Body).Decode(&existingUsers); err == nil && len(existingUsers) > 0 {
			httputil.ConflictWithData(c, gin.H{
				"errors": map[string]string{
					"email": "An account with this email already exists",
				},
			})
			return true
		}
	}

	return false
}

// createKeycloakUser creates a new user in Keycloak and returns the user ID
func (a *AuthHandler) createKeycloakUser(c *gin.Context, user *UserRegistration, adminToken string) (string, bool) {
	createURL := fmt.Sprintf("%s/admin/realms/%s/users", a.cfg.Auth.BaseURL, a.cfg.Auth.Realm)
	userPayload := user.ToKeycloakPayload()
	jsonData, _ := json.Marshal(userPayload)

	req, _ := http.NewRequest("POST", createURL, bytes.NewReader(jsonData))
	req.Header.Set("Authorization", authorizationPrefix+adminToken)
	req.Header.Set("Content-Type", "application/json")

	createResp, err := a.httpClient.Do(req)
	if err != nil {
		httputil.BadGateway(c, "failed to create user")
		return "", false
	}
	defer func() { _ = createResp.Body.Close() }()

	if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusNoContent {
		if !a.handleUserCreationError(c, createResp) {
			return "", false
		}
	}

	userID := a.extractUserIDFromResponse(createResp)
	return userID, true
}

// handleUserCreationError handles errors during user creation
func (a *AuthHandler) handleUserCreationError(c *gin.Context, createResp *http.Response) bool {
	body, _ := io.ReadAll(createResp.Body)
	bodyStr := string(body)

	if createResp.StatusCode == http.StatusConflict || bytes.Contains(body, []byte("User exists with same")) || bytes.Contains(body, []byte("already exists")) {
		httputil.ConflictWithData(c, gin.H{
			"errors": map[string]string{
				"email": "An account with this email already exists",
			},
		})
		return false
	}

	c.JSON(createResp.StatusCode, gin.H{"error": "user creation failed", "details": bodyStr})
	return false
}

// extractUserIDFromResponse extracts user ID from Keycloak response Location header
func (a *AuthHandler) extractUserIDFromResponse(resp *http.Response) string {
	userLocation := resp.Header.Get("Location")
	if userLocation == "" {
		return ""
	}

	parts := bytes.Split([]byte(userLocation), []byte("/"))
	if len(parts) > 0 {
		return string(parts[len(parts)-1])
	}
	return ""
}

// sendVerificationEmail sends verification email to the newly registered user
func (a *AuthHandler) sendVerificationEmail(userID, adminToken string) {
	if userID == "" {
		return
	}

	sendVerificationEmailURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/send-verify-email",
		a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, userID)

	params := url.Values{}
	params.Add("client_id", a.cfg.Auth.ClientID)
	params.Add("lifespan", "86400")
	params.Add("redirect_uri", a.cfg.FrontendURL+"/login?verified=true")
	sendVerificationEmailURL = sendVerificationEmailURL + "?" + params.Encode()

	verifyReq, _ := http.NewRequest("PUT", sendVerificationEmailURL, nil)
	verifyReq.Header.Set("Authorization", authorizationPrefix+adminToken)

	verifyResp, err := a.httpClient.Do(verifyReq)
	if err != nil {
		platformlogger.WithFields(map[string]interface{}{
			"component": "auth",
			"user_id":   userID,
			"error":     err,
		}).Warn("Failed to send verification email")
		return
	}
	defer func() { _ = verifyResp.Body.Close() }()
}

func (a *AuthHandler) setUserPassword(userID, password, adminToken string) error {
	resetPasswordURL := fmt.Sprintf("%s/admin/realms/%s/users/%s/reset-password",
		a.cfg.Auth.BaseURL, a.cfg.Auth.Realm, userID)

	payload := map[string]any{
		"type":      "password",
		"value":     password,
		"temporary": false,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal password payload: %w", err)
	}

	req, err := http.NewRequest("PUT", resetPasswordURL, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", authorizationPrefix+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to set password: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("keycloak returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// sendRegistrationSuccessResponse sends successful registration response
func (a *AuthHandler) sendRegistrationSuccessResponse(c *gin.Context, user *UserRegistration) {
	httputil.Created(c, gin.H{
		"message": "User registered successfully. Please check your email to verify your account.",
		"user": gin.H{
			"email":        user.Email,
			"name":         user.Name,
			"organization": user.Organization,
			"position":     user.Position,
			"access_level": user.AccessLevel,
		},
	})
}
