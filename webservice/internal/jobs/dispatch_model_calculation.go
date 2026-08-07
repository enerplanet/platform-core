package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hibiken/asynq"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"

	"platform.local/common/pkg/contracts"
	"platform.local/common/pkg/models"
	"platform.local/common/pkg/utils"
	"platform.local/platform/logger"

	"spatialhub_webservice/internal/backendclient"
	"spatialhub_webservice/internal/services"
)

// TypeDispatchModelCalculation is the asynq task type (shared contract).
const TypeDispatchModelCalculation = contracts.TaskDispatchModelCalculation

// HandleDispatchModelCalculation reserves an instance, claims the model via the backend, and sends the calculation.
func HandleDispatchModelCalculation(
	ctx context.Context,
	t *asynq.Task,
	db *gorm.DB,
	cpuThreshold float64,
	backend backendclient.Lifecycle,
) error {
	log := logger.ForComponent("job")

	payload, err := parsePayload(t)
	if err != nil {
		return err
	}
	modelID := payload.ModelID

	wsService := services.NewWebserviceService(db)

	// 1. Reserve a compute instance (touches WebserviceInstance only).
	instance, err := reserveInstance(ctx, db, cpuThreshold, modelID, log)
	if err != nil {
		return err // no-capacity / tx error → asynq retries
	}

	// 2. Ask the backend to claim the model (queue -> running); release the instance if we lost the race.
	claimed, err := backend.MarkRunning(ctx, modelID, instance.ID)
	if err != nil {
		log.Errorf("mark-running call failed model_id=%d webservice_id=%d err=%v", modelID, instance.ID, err)
		releaseInstance(ctx, wsService, instance.ID, log)
		return fmt.Errorf("mark-running failed: %w", err)
	}
	if !claimed {
		log.Infof("model no longer queued, releasing instance model_id=%d webservice_id=%d", modelID, instance.ID)
		releaseInstance(ctx, wsService, instance.ID, log)
		return nil
	}

	// 3. Send the calculation to the instance; report failures to the backend.
	if err := sendCalculation(ctx, wsService, backend, instance, modelID, payload.Payload, log); err != nil {
		return err
	}
	return nil
}

func parsePayload(t *asynq.Task) (*contracts.DispatchModelCalculation, error) {
	var payload contracts.DispatchModelCalculation
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
	}
	if payload.Version != "" && payload.Version != contracts.LifecycleAPIVersion {
		return nil, fmt.Errorf("unsupported dispatch payload version %q", payload.Version)
	}
	return &payload, nil
}

func reserveInstance(ctx context.Context, db *gorm.DB, cpuThreshold float64, modelID uint, log *logrus.Entry) (*models.WebserviceInstance, error) {
	var instance *models.WebserviceInstance
	err := db.Transaction(func(tx *gorm.DB) error {
		reserved, err := services.NewWebserviceService(tx).ReserveAvailableInstanceTx(ctx, tx, cpuThreshold)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				log.Infof("no webservice available; will retry model_id=%d", modelID)
			} else {
				log.Errorf("failed to reserve webservice model_id=%d err=%v", modelID, err)
			}
			return err
		}
		instance = reserved
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("no webservice available: %w", err)
		}
		return nil, err
	}
	return instance, nil
}

func releaseInstance(ctx context.Context, wsService *services.WebserviceService, instanceID uint, log *logrus.Entry) {
	if err := wsService.ReleaseInstance(ctx, instanceID); err != nil {
		log.Warnf("failed to release webservice webservice_id=%d err=%v", instanceID, err)
	}
}

func sendCalculation(
	ctx context.Context,
	wsService *services.WebserviceService,
	backend backendclient.Lifecycle,
	instance *models.WebserviceInstance,
	modelID uint,
	calcPayload map[string]interface{},
	log *logrus.Entry,
) error {
	endpoint := getEndpoint(instance)

	result, err := wsService.SendCalculationRequest(ctx, instance, endpoint, calcPayload)
	if err != nil {
		log.Errorf("calculation request failed model_id=%d webservice_id=%d err=%v", modelID, instance.ID, err)
		if merr := backend.MarkFailed(ctx, modelID, fmt.Sprintf("calculation request failed: %v", err)); merr != nil {
			log.Errorf("mark-failed call failed model_id=%d err=%v", modelID, merr)
		}
		releaseInstance(ctx, wsService, instance.ID, log)
		return fmt.Errorf("calculation request failed: %w", err)
	}

	persistSessionMetadata(ctx, backend, modelID, result, log)
	return nil
}

func getEndpoint(instance *models.WebserviceInstance) string {
	if instance.Endpoint != nil {
		return *instance.Endpoint
	}
	return ""
}

func persistSessionMetadata(ctx context.Context, backend backendclient.Lifecycle, modelID uint, result map[string]interface{}, log *logrus.Entry) {
	var sessionID *int64
	var callbackURL *string
	if sid, ok := utils.ExtractSessionID(result); ok {
		sessionID = &sid
	}
	if cb, ok := utils.ExtractCallbackURL(result); ok {
		callbackURL = &cb
	}
	if sessionID == nil && callbackURL == nil {
		return
	}
	if err := backend.SetRunSession(ctx, modelID, sessionID, callbackURL); err != nil {
		log.Errorf("failed to persist session metadata model_id=%d err=%v", modelID, err)
	}
}
