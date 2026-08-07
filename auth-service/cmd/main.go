package main

import (
	"context"
	"fmt"
	"time"

	"platform.local/auth-service/internal/config"
	authhandler "platform.local/auth-service/internal/handler/auth"
	"platform.local/auth-service/internal/middleware"
	authredis "platform.local/auth-service/internal/store/redis"
	pkgauth "platform.local/platform/auth"
	platformconfig "platform.local/platform/config"
	platformdatabase "platform.local/platform/database"
	platformemail "platform.local/platform/email"
	platformlogger "platform.local/platform/logger"
	platformsecurity "platform.local/platform/security"
	platformserver "platform.local/platform/server"
	redisstore "platform.local/platform/session/redis"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

func main() {
	gin.SetMode(gin.ReleaseMode)

	if err := platformlogger.Init("logs", "auth-service"); err != nil {
		panic(fmt.Sprintf("failed to init logger: %v", err))
	}
	log := platformlogger.Logger

	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("failed to load and parse config : %v", err)
		return
	}

	platformconfig.SetupTimezone(cfg.AppTimezone, log)
	serverAddr := fmt.Sprintf("%s:%s", cfg.AppHost, cfg.AppPort)

	appDeps := initializeInfrastructure(cfg, log)
	defer appDeps.Close()

	r := setupGinEngine(cfg, log)
	authHandler := initializeAuthHandler(cfg, serverAddr, appDeps)

	configureAuthRoutes(r, cfg, appDeps, authHandler)

	srv := platformserver.NewHTTPServer(platformserver.HTTPServerConfig{
		Addr:    serverAddr,
		Handler: r,
	})

	platformserver.RunWithGracefulShutdown(srv, log, 5*time.Second)
}

type AppDependencies struct {
	RedisClient        *goredis.Client
	AuthClient         *pkgauth.Client
	AdminTokenProvider *pkgauth.AdminTokenProvider
	EmailService       *platformemail.EmailService
	Cfg                *config.Config
}

func (d *AppDependencies) Close() {
	if d.RedisClient != nil {
		_ = d.RedisClient.Close()
	}
}

func initializeInfrastructure(cfg *config.Config, log *logrus.Logger) *AppDependencies {
	ctx := context.Background()
	authOptions := []pkgauth.Option{
		pkgauth.WithClientSecret(cfg.Auth.ClientSecret),
		pkgauth.WithRealmKeycloak(cfg.Auth.Realm),
	}
	authClient, err := pkgauth.New(
		ctx,
		cfg.Auth.BaseURL,
		cfg.Auth.ClientID,
		cfg.Auth.RedirectURL,
		authOptions...,
	)
	if err != nil {
		log.Fatalf("Failed to initialize auth client : %v", err)
	}

	redisClient, err := platformdatabase.ConnectRedis(ctx, cfg.RedisConfig)
	if err != nil {
		log.Fatalf("failed to connect to Redis: %v", err)
	}
	log.WithField("component", "startup").Info("Redis connection successful")

	adminTokenProvider := pkgauth.NewAdminTokenProvider(
		cfg.Auth.BaseURL,
		cfg.Auth.Realm,
		cfg.Auth.ClientID,
		cfg.Auth.ClientSecret,
	)

	emailService := platformemail.NewEmailService(platformemail.SMTPConfig{
		Host:      cfg.Email.SMTPHost,
		Port:      cfg.Email.SMTPPort,
		Username:  cfg.Email.SMTPUsername,
		Password:  cfg.Email.SMTPPassword,
		FromEmail: cfg.Email.SMTPFromEmail,
		FromName:  cfg.Email.SMTPFromName,
		UseTLS:    cfg.Email.SMTPUseTLS,
	})

	return &AppDependencies{
		RedisClient:        redisClient,
		AuthClient:         authClient,
		AdminTokenProvider: adminTokenProvider,
		EmailService:       emailService,
		Cfg:                cfg,
	}
}

func setupGinEngine(cfg *config.Config, log *logrus.Logger) *gin.Engine {
	trusted := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

	r, err := platformserver.InitGin(trusted, platformsecurity.SecurityHeaders())
	if err != nil {
		log.Warnf("Failed to set trusted proxies: %v", err)
	}

	r.LoadHTMLGlob("internal/handler/auth/templates/*/*.tmpl")

	middleware.SetCSRFConfig(&middleware.CSRFConfig{
		CookieDomain:        cfg.CookieDomain,
		CookieMaxAge:        cfg.SessionTTLMinutes * 60,
		IsProduction:        cfg.AppEnv == "production",
		EnableRotation:      true,
		RotationGracePeriod: 5 * time.Second,
	})

	middleware.SetSessionCookieMaxAge(cfg.SessionTTLMinutes * 60)
	middleware.SetSessionCookieDomain(cfg.CookieDomain)
	middleware.SetSessionCookieIsProduction(cfg.AppEnv == "production")

	return r
}

func initializeAuthHandler(cfg *config.Config, serverAddr string, deps *AppDependencies) *authhandler.AuthHandler {
	sessionStore := redisstore.NewSessionRedisManager(deps.RedisClient, cfg.SessionTTLMinutes)
	authStore := authredis.NewAuthRedisManager(deps.RedisClient)
	loginLockout := middleware.DefaultLoginLockout()

	return authhandler.New(cfg,
		serverAddr,
		deps.AuthClient,
		authStore,
		sessionStore,
		deps.AdminTokenProvider,
		loginLockout,
	)
}

func configureAuthRoutes(r *gin.Engine, cfg *config.Config, deps *AppDependencies, authHandler *authhandler.AuthHandler) {
	sessionStore := redisstore.NewSessionRedisManager(deps.RedisClient, cfg.SessionTTLMinutes)
	sessionValidator := middleware.SessionValidationMiddleware(sessionStore)
	authRateLimiter := middleware.AuthRateLimiter()
	strictRateLimiter := middleware.StrictAuthRateLimiter()

	api := r.Group("/api")
	{
		// CSRF token endpoint
		api.GET("/csrf-token", func(c *gin.Context) {
			csrfToken := middleware.GenerateCSRFToken()

			csrfConfig := &middleware.CSRFConfig{
				CookieDomain: cfg.CookieDomain,
				CookieMaxAge: cfg.SessionTTLMinutes * 60,
				IsProduction: cfg.AppEnv == "production",
			}
			middleware.SetCSRFTokenCookie(c, csrfToken, csrfConfig)

			c.JSON(200, gin.H{"csrf_token": csrfToken})
		})

		api.POST("/login", authRateLimiter.Middleware(), authHandler.Login)
		api.POST("/register", authRateLimiter.Middleware(), authHandler.Register)
		api.GET("/callback-auth", authHandler.Callback)
		api.POST("/logout", authHandler.Logout)
		api.POST("/auth/resend-verification", strictRateLimiter.Middleware(), authHandler.ResendVerificationEmail)
		api.POST("/auth/forgot-password", strictRateLimiter.Middleware(), authHandler.ForgotPassword)

		sessionRefresh := middleware.SessionRefreshMiddleware(sessionStore)
		protected := api.Group("")
		protected.Use(sessionValidator, sessionRefresh)
		{
			protected.POST("/auth/refresh-token", authHandler.RefreshToken)
			protected.POST("/auth/change-password", authHandler.ChangePassword)
			protected.GET("/auth/tour-status", authHandler.GetTourStatus)
			protected.POST("/auth/complete-tour", authHandler.CompleteTour)
			protected.GET("/auth/keep-alive", authHandler.KeepAlive)
		}
	}

	// Internal endpoints for backend validation
	internal := r.Group("/internal")
	{
		internal.GET("/validate-session", func(c *gin.Context) {
			sessionData, sessionID, err := middleware.ValidateSession(c, sessionStore)
			if err != nil {
				c.JSON(401, gin.H{"error": err.Error()})
				return
			}

			if err := sessionStore.RefreshSessionTTL(c.Request.Context(), sessionID); err != nil {
				platformlogger.WithFields(map[string]interface{}{
					"component":  "validate_session",
					"session_id": sessionID,
					"error":      err,
				}).Warn("Failed to refresh session TTL")
			}

			c.JSON(200, gin.H{
				"success": true,
				"user": gin.H{
					"id":           sessionData.UserID,
					"email":        sessionData.UserInfoData.Email,
					"name":         sessionData.UserInfoData.FullName,
					"access_level": sessionData.AccessLevel,
					"group_id":     sessionData.GroupID,
				},
			})
		})

		// Delete all sessions for a user
		internal.DELETE("/sessions/user/:userId", func(c *gin.Context) {
			userID := c.Param("userId")
			if userID == "" {
				c.JSON(400, gin.H{"error": "User ID required"})
				return
			}

			if err := sessionStore.DeleteSessionsByUser(c.Request.Context(), userID); err != nil {
				c.JSON(500, gin.H{"error": "Failed to delete sessions"})
				return
			}

			c.JSON(200, gin.H{"success": true, "message": "Sessions deleted"})
		})

		// Update session group_id
		internal.PATCH("/sessions/:sessionId/group", func(c *gin.Context) {
			sessionID := c.Param("sessionId")
			if sessionID == "" {
				c.JSON(400, gin.H{"error": "Session ID required"})
				return
			}

			var req struct {
				GroupID string `json:"group_id"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(400, gin.H{"error": "Invalid request body"})
				return
			}

			sessionData, err := sessionStore.GetSession(c.Request.Context(), sessionID)
			if err != nil || sessionData == nil {
				c.JSON(404, gin.H{"error": "Session not found"})
				return
			}

			sessionData.GroupID = req.GroupID
			if err := sessionStore.SaveSession(c.Request.Context(), sessionID, sessionData); err != nil {
				c.JSON(500, gin.H{"error": "Failed to update session"})
				return
			}

			c.JSON(200, gin.H{"success": true, "message": "Session updated"})
		})
	}

	r.GET("/api/health", platformserver.HealthHandler("auth-service"))
}
