package worker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"platform.local/common/pkg/models"
	"platform.local/platform/logger"
	"spatialhub_webservice/internal/services"
	"spatialhub_webservice/internal/store"
)

type Scheduler struct {
	db                *gorm.DB
	wsService         *services.WebserviceService
	ticker            *time.Ticker
	done              chan bool
	stuckModelTimeout time.Duration
}

func valueOrZero(val *float64) float64 {
	if val == nil {
		return 0
	}
	return *val
}

func stringOrEmpty(val *string) string {
	if val == nil {
		return ""
	}
	return *val
}

func NewScheduler(db *gorm.DB, stuckModelTimeout time.Duration) *Scheduler {
	if stuckModelTimeout <= 0 {
		stuckModelTimeout = 2 * time.Hour
	}
	return &Scheduler{
		db:                db,
		wsService:         services.NewWebserviceService(db),
		done:              make(chan bool),
		stuckModelTimeout: stuckModelTimeout,
	}
}

func (s *Scheduler) Start(interval time.Duration) {
	log := logger.ForComponent("scheduler")
	log.Infof("starting scheduler with interval=%v", interval)

	s.ticker = time.NewTicker(interval)

	go func() {
		s.runTasks()

		for {
			select {
			case <-s.ticker.C:
				s.runTasks()
			case <-s.done:
				log.Info("scheduler stopped")
				return
			}
		}
	}()
}

func (s *Scheduler) Stop() {
	if s.ticker != nil {
		s.ticker.Stop()
	}
	s.done <- true
}

func (s *Scheduler) runTasks() {
	log := logger.ForComponent("scheduler")

	if err := s.checkWebserviceStatuses(); err != nil {
		log.Errorf("failed to check webservices: %v", err)
	}

	if err := s.checkModelsOnOfflineWebservices(); err != nil {
		log.Errorf("failed to check models on offline webservices: %v", err)
	}

	if err := s.fixStuckConcurrency(); err != nil {
		log.Errorf("failed to fix stuck concurrency: %v", err)
	}

	if err := s.checkStuckModels(); err != nil {
		log.Errorf("failed to check stuck models: %v", err)
	}
}


func (s *Scheduler) checkWebserviceStatuses() error {
	ctx := context.Background()
	log := logger.ForComponent("scheduler")

	wsService := s.wsService

	filters := store.WebserviceFilters{Page: 1, PerPage: 1000}
	result, err := wsService.List(ctx, filters)
	if err != nil {
		return err
	}

	instances, ok := result["items"].([]models.WebserviceInstance)
	if !ok {
		return nil
	}

	var onlineCount atomic.Int64
	var offlineCount atomic.Int64

	// Ping webservices concurrently with bounded parallelism.
	const maxConcurrentPings = 10
	sem := make(chan struct{}, maxConcurrentPings)
	var wg sync.WaitGroup

	for _, instance := range instances {
		wg.Add(1)
		sem <- struct{}{} // acquire slot

		go func(inst models.WebserviceInstance) {
			defer wg.Done()
			defer func() { <-sem }() // release slot

			isOnline, _, pingErr := wsService.Ping(ctx, inst.ID)

			updates := map[string]interface{}{
				"last_check": time.Now(),
			}
			if pingErr != nil || !isOnline {
				updates["status"] = models.StatusInactive
				updates["available"] = false
				updates["status_reason"] = "scheduler: ping failed"
				updates["cpu_usage"] = nil
				updates["memory_usage"] = nil
				offlineCount.Add(1)
			} else {
				updates["status"] = models.StatusActive
				updates["available"] = true
				updates["status_reason"] = "scheduler: online"

				cpuUsage, memUsage, err := wsService.FetchResourceUsage(ctx, inst.ID)
				if err == nil {
					if cpuUsage != nil {
						updates["cpu_usage"] = *cpuUsage
					}
					if memUsage != nil {
						updates["memory_usage"] = *memUsage
					}
					log.Debugf("scheduler: webservice id=%d name=%s is ONLINE (concurrency: %d/%d, cpu: %.1f%%, mem: %.1f%%)",
						inst.ID, stringOrEmpty(inst.Name), inst.CurrentConcurrency, inst.MaxConcurrency,
						valueOrZero(cpuUsage), valueOrZero(memUsage))
				} else {
					log.Debugf("scheduler: webservice id=%d name=%s is ONLINE (concurrency: %d/%d, resource usage unavailable)",
						inst.ID, stringOrEmpty(inst.Name), inst.CurrentConcurrency, inst.MaxConcurrency)
				}
				onlineCount.Add(1)
			}

			if err := s.db.Model(&models.WebserviceInstance{}).Where("id = ?", inst.ID).Updates(updates).Error; err != nil {
				log.Errorf("failed to update webservice status id=%d err=%v", inst.ID, err)
			}
		}(instance)
	}

	wg.Wait()

	log.Infof("scheduler: online=%d offline=%d", onlineCount.Load(), offlineCount.Load())

	return nil
}

func (s *Scheduler) checkModelsOnOfflineWebservices() error {
	ctx := context.Background()
	log := logger.ForComponent("scheduler")

	var runningModels []models.Model
	if err := s.db.Where("status IN (?) AND webservice_id IS NOT NULL",
		[]string{models.ModelStatusRunning, models.ModelStatusQueue}).
		Find(&runningModels).Error; err != nil {
		return err
	}

	if len(runningModels) == 0 {
		return nil
	}

	for _, model := range runningModels {
		var ws models.WebserviceInstance
		if err := s.db.First(&ws, *model.WebserviceID).Error; err != nil {
			log.Warnf("model using non-existent webservice model_id=%d webservice_id=%d",
				model.ID, *model.WebserviceID)
			continue
		}

		if ws.Status != models.StatusActive {
			log.Warnf("failing model on offline webservice model_id=%d webservice_id=%d webservice_status=%s",
				model.ID, ws.ID, ws.Status)

			now := time.Now().UTC()
			errorMessage := fmt.Sprintf("Calculation interrupted - webservice %d went offline", ws.ID)
			if err := s.db.Model(&model).Updates(map[string]interface{}{
				"status":                   models.ModelStatusFailed,
				"webservice_id":            nil,
				"calculation_completed_at": now,
				"updated_at":               now,
				"results": map[string]interface{}{
					"error": errorMessage,
				},
			}).Error; err != nil {
				log.Errorf("failed to mark model as failed model_id=%d err=%v", model.ID, err)
				continue
			}

			if err := s.wsService.ReleaseInstance(ctx, ws.ID); err != nil {
				log.Errorf("failed to release webservice model_id=%d webservice_id=%d err=%v",
					model.ID, ws.ID, err)
			} else {
				log.Infof("released webservice for model on offline service model_id=%d webservice_id=%d",
					model.ID, ws.ID)
			}
		}
	}

	return nil
}

func (s *Scheduler) fixStuckConcurrency() error {
	log := logger.ForComponent("scheduler")

	var webservices []models.WebserviceInstance
	if err := s.db.Find(&webservices).Error; err != nil {
		return err
	}

	for _, ws := range webservices {
		// Reconcile DB counter with actual queued/running models.
		var actualCount int64
		s.db.Model(&models.Model{}).
			Where("webservice_id = ? AND status IN (?)", ws.ID, []string{models.ModelStatusQueue, models.ModelStatusRunning}).
			Count(&actualCount)

		if int(actualCount) != ws.CurrentConcurrency {
			log.Warnf("webservice id=%d has inconsistent concurrency: DB shows %d, actual models: %d - fixing",
				ws.ID, ws.CurrentConcurrency, actualCount)

			if err := s.db.Model(&models.WebserviceInstance{}).
				Where("id = ?", ws.ID).
				Update("current_concurrency", actualCount).Error; err != nil {
				log.Errorf("failed to fix concurrency for webservice id=%d err=%v", ws.ID, err)
				continue
			}

			log.Infof("fixed concurrency for webservice id=%d: %d -> %d",
				ws.ID, ws.CurrentConcurrency, actualCount)
		}
	}

	return nil
}

func (s *Scheduler) checkStuckModels() error {
	ctx := context.Background()
	log := logger.ForComponent("scheduler")

	// Timeout is based on calculation start time, not last update timestamp.
	timeout := s.stuckModelTimeout
	cutoffTime := time.Now().Add(-timeout)

	var stuckModels []models.Model
	if err := s.db.Where("status = ? AND calculation_started_at IS NOT NULL AND calculation_started_at < ?", models.ModelStatusRunning, cutoffTime).
		Find(&stuckModels).Error; err != nil {
		return err
	}

	if len(stuckModels) == 0 {
		return nil
	}

	log.Warnf("found %d stuck models (running > %v)", len(stuckModels), timeout)

	for _, model := range stuckModels {
		runningSince := model.UpdatedAt
		if model.CalculationStartedAt != nil {
			runningSince = *model.CalculationStartedAt
		}
		log.Warnf("marking stuck model as failed model_id=%d webservice_id=%v age=%v",
			model.ID, model.WebserviceID, time.Since(runningSince))

		now := time.Now().UTC()
		errorMessage := fmt.Sprintf("Calculation timed out - exceeded maximum running time (%v)", timeout)
		if err := s.db.Model(&model).Updates(map[string]interface{}{
			"status":                   models.ModelStatusFailed,
			"webservice_id":            nil,
			"calculation_completed_at": now,
			"updated_at":               now,
			"results": map[string]interface{}{
				"error": errorMessage,
			},
		}).Error; err != nil {
			log.Errorf("failed to update stuck model model_id=%d err=%v", model.ID, err)
			continue
		}

		if model.WebserviceID != nil {
			if err := s.wsService.ReleaseInstance(ctx, *model.WebserviceID); err != nil {
				log.Errorf("failed to release webservice model_id=%d webservice_id=%d err=%v",
					model.ID, *model.WebserviceID, err)
			} else {
				log.Infof("released webservice for stuck model model_id=%d webservice_id=%d",
					model.ID, *model.WebserviceID)
			}
		}
	}

	return nil
}
