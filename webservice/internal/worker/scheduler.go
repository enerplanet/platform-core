package worker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"platform.local/common/pkg/contracts"
	"platform.local/common/pkg/models"
	"platform.local/platform/logger"
	"spatialhub_webservice/internal/backendclient"
	"spatialhub_webservice/internal/services"
	"spatialhub_webservice/internal/store"
)

type Scheduler struct {
	db                *gorm.DB
	wsService         *services.WebserviceService
	backend           backendclient.Lifecycle
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

func NewScheduler(db *gorm.DB, stuckModelTimeout time.Duration, backend backendclient.Lifecycle) *Scheduler {
	if stuckModelTimeout <= 0 {
		stuckModelTimeout = 2 * time.Hour
	}
	return &Scheduler{
		db:                db,
		wsService:         services.NewWebserviceService(db),
		backend:           backend,
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

// checkModelsOnOfflineWebservices fails models whose compute instance went offline.
func (s *Scheduler) checkModelsOnOfflineWebservices() error {
	ctx := context.Background()
	log := logger.ForComponent("scheduler")

	active, err := s.backend.ActiveModels(ctx)
	if err != nil {
		return err
	}

	for _, m := range active {
		if m.WebserviceID == nil {
			continue
		}
		var ws models.WebserviceInstance
		if err := s.db.First(&ws, *m.WebserviceID).Error; err != nil {
			log.Warnf("model using non-existent webservice model_id=%d webservice_id=%d",
				m.ModelID, *m.WebserviceID)
			continue
		}

		if ws.Status != models.StatusActive {
			log.Warnf("failing model on offline webservice model_id=%d webservice_id=%d webservice_status=%s",
				m.ModelID, ws.ID, ws.Status)

			errorMessage := fmt.Sprintf("Calculation interrupted - webservice %d went offline", ws.ID)
			if err := s.backend.MarkFailed(ctx, m.ModelID, errorMessage); err != nil {
				log.Errorf("failed to mark model as failed model_id=%d err=%v", m.ModelID, err)
				continue
			}

			if err := s.wsService.ReleaseInstance(ctx, ws.ID); err != nil {
				log.Errorf("failed to release webservice model_id=%d webservice_id=%d err=%v",
					m.ModelID, ws.ID, err)
			} else {
				log.Infof("released webservice for model on offline service model_id=%d webservice_id=%d",
					m.ModelID, ws.ID)
			}
		}
	}

	return nil
}

// fixStuckConcurrency corrects each instance's concurrency counter against the backend's active-model count.
func (s *Scheduler) fixStuckConcurrency() error {
	log := logger.ForComponent("scheduler")

	active, err := s.backend.ActiveModels(context.Background())
	if err != nil {
		return err
	}
	counts := make(map[uint]int)
	for _, m := range active {
		if m.WebserviceID != nil {
			counts[*m.WebserviceID]++
		}
	}

	var webservices []models.WebserviceInstance
	if err := s.db.Find(&webservices).Error; err != nil {
		return err
	}

	for _, ws := range webservices {
		actualCount := counts[ws.ID]
		if actualCount != ws.CurrentConcurrency {
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

// checkStuckModels fails models that have been running past the timeout.
func (s *Scheduler) checkStuckModels() error {
	ctx := context.Background()
	log := logger.ForComponent("scheduler")

	timeout := s.stuckModelTimeout
	cutoffTime := time.Now().Add(-timeout)

	active, err := s.backend.ActiveModels(ctx)
	if err != nil {
		return err
	}

	for _, m := range active {
		if m.Status != contracts.StatusRunning {
			continue
		}
		if m.CalculationStartedAt == nil || !m.CalculationStartedAt.Before(cutoffTime) {
			continue
		}
		log.Warnf("marking stuck model as failed model_id=%d webservice_id=%v age=%v",
			m.ModelID, m.WebserviceID, time.Since(*m.CalculationStartedAt))

		errorMessage := fmt.Sprintf("Calculation timed out - exceeded maximum running time (%v)", timeout)
		if err := s.backend.MarkFailed(ctx, m.ModelID, errorMessage); err != nil {
			log.Errorf("failed to update stuck model model_id=%d err=%v", m.ModelID, err)
			continue
		}

		if m.WebserviceID != nil {
			if err := s.wsService.ReleaseInstance(ctx, *m.WebserviceID); err != nil {
				log.Errorf("failed to release webservice model_id=%d webservice_id=%d err=%v",
					m.ModelID, *m.WebserviceID, err)
			} else {
				log.Infof("released webservice for stuck model model_id=%d webservice_id=%d",
					m.ModelID, *m.WebserviceID)
			}
		}
	}

	return nil
}
