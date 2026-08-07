package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"platform.local/common/pkg/models"
	platformconfig "platform.local/platform/config"
	platformdatabase "platform.local/platform/database"
	platformlogger "platform.local/platform/logger"
	platformserver "platform.local/platform/server"
	platformworker "platform.local/platform/worker"

	_ "go.uber.org/automaxprocs"

	"spatialhub_webservice/internal/backendclient"
	"spatialhub_webservice/internal/config"
	webhandler "spatialhub_webservice/internal/handler/webservice"
	"spatialhub_webservice/internal/middleware"
	"spatialhub_webservice/internal/worker"
)

func main() {
	gin.SetMode(gin.ReleaseMode)

	if err := platformlogger.Init("logs", "webservice"); err != nil {
		panic(fmt.Sprintf("failed to init logger: %v", err))
	}
	log := platformlogger.Logger

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	platformconfig.SetupTimezone(cfg.AppTimezone, log)

	deps := mustInitDependencies(cfg, log)
	defer deps.Close()

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.InjectUserContext())
	r.Use(cors.Default())

	registerRoutes(r, deps)

	serverAddr := fmt.Sprintf("%s:%s", cfg.AppHost, cfg.AppPort)
	srv := platformserver.NewHTTPServer(platformserver.HTTPServerConfig{
		Addr:    serverAddr,
		Handler: r,
	})

	platformserver.RunWithGracefulShutdown(srv, log, 10*time.Second)
}

type appDependencies struct {
	Cfg         *config.Config
	DB          *gorm.DB
	SQL         *sql.DB
	Redis       *goredis.Client
	AsynqServer *asynq.Server
	Scheduler   *worker.Scheduler
}

func mustInitDependencies(cfg *config.Config, log *logrus.Logger) *appDependencies {
	db, sqlDB, err := platformdatabase.ConnectWithPing(cfg.Database)
	if err != nil {
		log.Fatalf("failed to connect db: %v", err)
	}
	log.WithField("component", "startup").Info("Database connection successful")

	redisClient := goredis.NewClient(&cfg.Redis)
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("redis ping failed: %v", err)
	}

	asynqOpt := asynq.RedisClientOpt{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	}
	dispatchWorkerConcurrency := deriveDispatchWorkerConcurrency(db, log)
	asynqServer := platformworker.NewServer(platformworker.ServerConfig{
		RedisOpt:    asynqOpt,
		Concurrency: dispatchWorkerConcurrency,
		Queues: map[string]int{
			"spatialAI_public": 10,
		},
		RetryDelayFunc: func(n int, err error, task *asynq.Task) time.Duration {
			if task.Type() == "dispatch_model_calculation" {
				// Retry quickly when capacity is temporarily full.
				if err != nil && strings.Contains(strings.ToLower(err.Error()), "no webservice available") {
					return time.Duration(cfg.Dispatch.NoCapacityRetryMs) * time.Millisecond
				}
				delay := time.Duration(n*n) * time.Second
				if delay < 3*time.Second {
					delay = 3 * time.Second
				}
				if delay > 60*time.Second {
					delay = 60 * time.Second
				}
				return delay
			}
			return platformworker.DefaultRetryDelay(n, err, task)
		},
	})

	backendLifecycle := backendclient.New(cfg.Backend.URL, cfg.Backend.CallbackSecret)

	taskProcessor := worker.NewTaskProcessor(db, cfg.Dispatch.CpuThresholdPercent, backendLifecycle)
	mux := asynq.NewServeMux()
	mux.HandleFunc("dispatch_model_calculation", taskProcessor.ProcessTask)

	go func() {
		if err := asynqServer.Run(mux); err != nil {
			log.Fatalf("asynq server error: %v", err)
		}
	}()

	scheduler := worker.NewScheduler(db, time.Duration(cfg.Scheduler.StuckModelTimeoutMinutes)*time.Minute, backendLifecycle)
	scheduler.Start(time.Duration(cfg.Scheduler.IntervalSeconds) * time.Second)

	return &appDependencies{
		Cfg:         cfg,
		DB:          db,
		SQL:         sqlDB,
		Redis:       redisClient,
		AsynqServer: asynqServer,
		Scheduler:   scheduler,
	}
}

func deriveDispatchWorkerConcurrency(db *gorm.DB, log *logrus.Logger) int {
	// Read total dispatch capacity from Webservice Management data.
	type capacityRow struct {
		Total int64
	}

	var row capacityRow
	err := db.Model(&models.WebserviceInstance{}).
		Select("COALESCE(SUM(CASE WHEN auto_scaling THEN GREATEST(max_concurrency, 1) ELSE 1 END), 0) AS total").
		Where("status = ?", models.StatusActive).
		Scan(&row).Error

	cpuFallback := runtime.NumCPU() * 4
	if cpuFallback < 128 {
		cpuFallback = 128
	}

	if err != nil {
		log.WithError(err).Warnf("failed to derive dispatch worker concurrency from DB, using fallback=%d", cpuFallback)
		return cpuFallback
	}

	byCapacity := int(row.Total) * 2
	if byCapacity < 1 {
		byCapacity = 1
	}

	workers := byCapacity
	if workers < cpuFallback {
		workers = cpuFallback
	}
	if workers > 512 {
		workers = 512
	}

	log.WithFields(logrus.Fields{
		"active_configured_capacity": row.Total,
		"dispatch_workers":           workers,
	}).Info("dispatch concurrency initialized from webservice management capacity")

	return workers
}

func (d *appDependencies) Close() {
	if d.SQL != nil {
		_ = d.SQL.Close()
	}
	if d.Redis != nil {
		_ = d.Redis.Close()
	}
	if d.AsynqServer != nil {
		d.AsynqServer.Shutdown()
	}
	if d.Scheduler != nil {
		d.Scheduler.Stop()
	}
}

func registerRoutes(r *gin.Engine, deps *appDependencies) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		wsHandler := webhandler.NewWebserviceHandler(deps.DB)
		api.POST("/webservices", wsHandler.CreateWebservice)
		api.GET("/webservices", wsHandler.GetWebserviceList)
		api.GET("/webservices/available-static-dates", wsHandler.GetAvailableStaticDates)
		api.GET("/webservices/available-data-coverage", wsHandler.GetAvailableDataCoverage)
		api.GET("/webservices/:id", wsHandler.GetWebserviceByID)
		api.PUT("/webservices/:id", wsHandler.UpdateWebservice)
		api.DELETE("/webservices/:id", wsHandler.DeleteWebservice)
		api.POST("/webservices/:id/available", wsHandler.MarkAvailable)
		api.POST("/webservices/:id/unavailable", wsHandler.MarkUnavailable)
		api.POST("/webservices/:id/busy", wsHandler.MarkBusy)
		api.POST("/webservices/:id/idle", wsHandler.MarkIdle)
		api.POST("/webservices/:id/heartbeat", wsHandler.Heartbeat)
		api.GET("/webservices/:id/health", wsHandler.CheckHealth)
		api.GET("/webservices/:id/ping", wsHandler.PingWebservice)
		api.POST("/webservices/:id/request", wsHandler.SendRequest)
		api.GET("/webservices/summary", wsHandler.GetSummary)
	}

	internal := api.Group("/internal")
	{
		wsHandler := webhandler.NewWebserviceHandler(deps.DB)
		internal.POST("/webservices/:id/release", wsHandler.ReleaseInstance)
		internal.POST("/webservices/:id/cancel-session", wsHandler.CancelSession)
	}
}
