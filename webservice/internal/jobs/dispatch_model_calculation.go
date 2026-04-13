package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"platform.local/common/pkg/models"
	"platform.local/common/pkg/utils"
	"platform.local/platform/logger"

	"spatialhub_webservice/internal/services"
)

const (
	TypeDispatchModelCalculation = "dispatch_model_calculation"
)

type DispatchModelCalculationPayload struct {
	ModelID uint                   `json:"model_id"`
	UserID  string                 `json:"user_id"`
	Payload map[string]interface{} `json:"payload"`
}

func HandleDispatchModelCalculation(ctx context.Context, t *asynq.Task, db *gorm.DB, cpuThreshold float64) error {
	log := logger.ForComponent("job")

	payload, err := parsePayload(t)
	if err != nil {
		return err
	}

	model, err := loadModel(db, payload.ModelID, log)
	if err != nil {
		return err
	}

	if !isModelQueued(&model, payload.ModelID, log) {
		return nil
	}

	wsService := services.NewWebserviceService(db)
	instance, err := reserveInstanceAndUpdateModel(ctx, db, &model, payload.ModelID, cpuThreshold, log)
	if err != nil {
		return err
	}

	if err := sendCalculationAndHandleResponse(ctx, db, wsService, &model, instance, payload.ModelID, payload.Payload, log); err != nil {
		return err
	}

	return nil
}

func parsePayload(t *asynq.Task) (*DispatchModelCalculationPayload, error) {
	var payload DispatchModelCalculationPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
	}
	return &payload, nil
}

func loadModel(db *gorm.DB, modelID uint, log *logrus.Entry) (models.Model, error) {
	var model models.Model
	if err := db.First(&model, modelID).Error; err != nil {
		return model, fmt.Errorf("model not found: %w", err)
	}
	return model, nil
}

func isModelQueued(model *models.Model, modelID uint, log *logrus.Entry) bool {
	if model.Status != models.ModelStatusQueue {
		log.Infof("model no longer queued, skipping model_id=%d current_status=%s", modelID, model.Status)
		return false
	}
	return true
}

func reserveInstanceAndUpdateModel(ctx context.Context, db *gorm.DB, model *models.Model, modelID uint, cpuThreshold float64, log *logrus.Entry) (*models.WebserviceInstance, error) {
	var instance *models.WebserviceInstance

	err := db.Transaction(func(tx *gorm.DB) error {
		wsServiceInTx := services.NewWebserviceService(tx)

		reservedInstance, err := wsServiceInTx.ReserveAvailableInstanceTx(ctx, tx, cpuThreshold)
		if err != nil {
			logReservationError(err, modelID, log)
			return err
		}

		if err := updateModelToRunning(tx, model, modelID, reservedInstance.ID); err != nil {
			return err
		}

		instance = reservedInstance
		return nil
	})

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("no webservice available: %w", err)
		}
		log.Errorf("transaction failed for model_id=%d err=%v", modelID, err)
		return nil, err
	}

	return instance, nil
}

func logReservationError(err error, modelID uint, log *logrus.Entry) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Infof("no webservice available in transaction; will retry model_id=%d", modelID)
	} else {
		log.Errorf("failed to reserve webservice in transaction model_id=%d err=%v", modelID, err)
	}
}

func updateModelToRunning(tx *gorm.DB, model *models.Model, modelID uint, webserviceID uint) error {
	now := time.Now().UTC()
	result := tx.Model(model).
		Where("id = ? AND status = ?", modelID, models.ModelStatusQueue).
		Updates(map[string]any{
			"status":                 models.ModelStatusRunning,
			"webservice_id":          webserviceID,
			"calculation_started_at": now,
			"updated_at":             now,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to update model to running: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("model not in queue status (already processed)")
	}
	return nil
}

func sendCalculationAndHandleResponse(ctx context.Context, db *gorm.DB, wsService *services.WebserviceService, model *models.Model, instance *models.WebserviceInstance, modelID uint, calcPayload map[string]interface{}, log *logrus.Entry) error {
	// calcPayload := payload.BuildCalculationPayload(model)
	// calcPayload["max_concurrency"] = instance.MaxConcurrency // Removed as it is not in the schema

	endpoint := getEndpoint(instance)

	result, err := wsService.SendCalculationRequest(ctx, instance, endpoint, calcPayload)
	if err != nil {
		handleCalculationError(db, wsService, model, instance, modelID, err, log)
		return fmt.Errorf("calculation request failed: %w", err)
	}

	updateSessionMetadata(db, model, result, modelID, log)
	return nil
}

func getEndpoint(instance *models.WebserviceInstance) string {
	if instance.Endpoint != nil {
		return *instance.Endpoint
	}
	return ""
}

func handleCalculationError(db *gorm.DB, wsService *services.WebserviceService, model *models.Model, instance *models.WebserviceInstance, modelID uint, err error, log *logrus.Entry) {
	log.Errorf("calculation request failed model_id=%d webservice_id=%d err=%v", modelID, instance.ID, err)
	now := time.Now().UTC()
	_ = db.Model(model).Updates(map[string]any{
		"status":                   models.ModelStatusFailed,
		"webservice_id":            nil, // Clear webservice assignment
		"calculation_completed_at": now,
		"updated_at":               now,
	}).Error
	// Release the webservice (decrement concurrency).
	// Use a bounded context so a hung DB/network doesn't block indefinitely.
	releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if merr := wsService.ReleaseInstance(releaseCtx, instance.ID); merr != nil {
		log.Warnf("failed to release webservice webservice_id=%d err=%v", instance.ID, merr)
	}
}

func updateSessionMetadata(db *gorm.DB, model *models.Model, result map[string]interface{}, modelID uint, log *logrus.Entry) {
	updates := map[string]any{
		"updated_at": time.Now().UTC(),
	}
	if sessionID, ok := utils.ExtractSessionID(result); ok {
		updates["session_id"] = sessionID
	}
	if callbackURL, ok := utils.ExtractCallbackURL(result); ok {
		updates["callback_url"] = callbackURL
	}

	if len(updates) > 1 {
		if err := db.Model(model).Updates(updates).Error; err != nil {
			log.Errorf("failed to persist session metadata model_id=%d err=%v", modelID, err)
		}
	}
}
