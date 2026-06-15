package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	platformconfig "platform.local/platform/config"
	platformdatabase "platform.local/platform/database"
	platformlogger "platform.local/platform/logger"
	platformserver "platform.local/platform/server"

	"spatialhub_geoserver/internal/config"
	geohandler "spatialhub_geoserver/internal/handler/geoserver"
	proxyhandler "spatialhub_geoserver/internal/handler/proxy"
	"spatialhub_geoserver/internal/services"
)

func main() {
	gin.SetMode(gin.ReleaseMode)

	if err := platformlogger.Init("logs", "geoserver-service"); err != nil {
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
	Cfg *config.Config
	DB  *gorm.DB
	SQL *sql.DB
}

func mustInitDependencies(cfg *config.Config, log *logrus.Logger) *appDependencies {
	db, sqlDB, err := platformdatabase.ConnectWithPing(cfg.Database)
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	log.WithField("component", "startup").Info("Database connection successful")

	return &appDependencies{Cfg: cfg, DB: db, SQL: sqlDB}
}

func (d *appDependencies) Close() {
	if d.SQL != nil {
		_ = d.SQL.Close()
	}
}

func registerRoutes(r *gin.Engine, deps *appDependencies) {
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	api := r.Group("/api")
	{
		proxy := proxyhandler.NewGeoServerProxyHandler()
		api.Any("/geoserver-proxy/:workspace/wms", proxy.ProxyWMS)
	}

	internal := api.Group("/internal/geoserver")
	{
		svc := services.NewGeoServerService(deps.DB)
		handler := geohandler.NewHandler(svc)
		internal.POST("/results/:id/configure", handler.ConfigureResult)
		internal.DELETE("/results/:id/layer", handler.DeleteLayer)
		internal.GET("/results/:id/bounds", handler.GetBounds)
		internal.POST("/results/:id/sample-distribution", handler.SampleDistribution)
		internal.POST("/results/:id/sample-grid", handler.SampleGrid)
	}
}
