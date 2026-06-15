package authhandler

import (
	"errors"
	"net/http"

	"platform.local/auth-service/internal/store"
	"platform.local/common/pkg/httputil"
	platformsession "platform.local/platform/session"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

func (a *AuthHandler) Callback(c *gin.Context) {
	callbackData, err := newCallbackData(c)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	if err = callbackData.verify(c, a.authStore); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("grant_type", "authorization_code"),
	}
	var oauth2Token *oauth2.Token
	oauth2Token, err = a.authClient.Oauth.Exchange(c, callbackData.authzCode, opts...)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	var oidcToken *oidcToken
	oidcToken, err = newOIDCToken(oauth2Token, a.authClient.OIDC)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	var userInfoClaims *userInfoClaims
	userInfoClaims, err = oidcToken.getClaims(c)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	sessionID := generateSecureSessionID()

	accessLevel, groupID := a.fetchUserGroupAndAccessLevel(userInfoClaims.Sub)

	sessionData := platformsession.SessionData{
		UserID:         userInfoClaims.Sub,
		AccessToken:    oauth2Token.AccessToken,
		RefreshToken:   oauth2Token.RefreshToken,
		TokenExpiresAt: oauth2Token.Expiry,
		UserInfoData: &platformsession.UserInfoData{
			Email:    userInfoClaims.Email,
			FullName: userInfoClaims.FullName,
		},
		AccessLevel: accessLevel,
		GroupID:     groupID,
	}
	if err = a.sessionStore.SaveSession(c, sessionID, &sessionData); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	a.setLoginCookies(c, sessionID, userInfoClaims.Email)

	c.Redirect(http.StatusTemporaryRedirect, "/success-login")
}

type callbackData struct {
	stateID   string
	authzCode string
}

func newCallbackData(c *gin.Context) (*callbackData, error) {
	stateID := c.Query("state")
	if stateID == "" {
		return nil, errors.New("stateID is required")
	}
	authorizationCode := c.Query("code")
	if authorizationCode == "" {
		return nil, errors.New("authorizationCode is required")
	}
	return &callbackData{
		stateID:   stateID,
		authzCode: authorizationCode,
	}, nil
}
func (c *callbackData) verify(ctx *gin.Context, authStore store.AuthStore) error {
	stateIDData, err := authStore.GetState(ctx, c.stateID)
	if err != nil {
		return err
	}
	if stateIDData != c.stateID {
		return errors.New("invalid stateID")
	}
	if err = authStore.DeleteState(ctx, stateIDData); err != nil {
		return err
	}
	return nil
}

type oidcToken struct {
	rawIDToken string
	verifier   *oidc.IDTokenVerifier
}

func newOIDCToken(oauthToken *oauth2.Token,
	verifier *oidc.IDTokenVerifier) (*oidcToken, error) {
	rawIDToken, ok := oauthToken.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("no id_token in response")
	}
	return &oidcToken{
		rawIDToken: rawIDToken,
		verifier:   verifier,
	}, nil
}

type userInfoClaims struct {
	Email    string `json:"email"`
	FullName string `json:"fullName"`
	Sub      string `json:"sub"`
}

func (o *oidcToken) getClaims(c *gin.Context) (*userInfoClaims, error) {
	idToken, err := o.verifier.Verify(c, o.rawIDToken)
	if err != nil {
		return nil, errors.New("failed to verify ID token")
	}
	userInfoClaims := &userInfoClaims{}
	if err := idToken.Claims(&userInfoClaims); err != nil {
		return nil, errors.New("failed to extract claims")
	}
	return userInfoClaims, nil
}
