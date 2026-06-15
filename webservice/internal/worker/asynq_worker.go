package worker

import (
	"context"

	"github.com/hibiken/asynq"
	"gorm.io/gorm"

	"platform.local/platform/logger"
	"spatialhub_webservice/internal/backendclient"
	"spatialhub_webservice/internal/jobs"
)

type TaskProcessor struct {
	db           *gorm.DB
	cpuThreshold float64
	backend      backendclient.Lifecycle
}

func NewTaskProcessor(db *gorm.DB, cpuThreshold float64, backend backendclient.Lifecycle) *TaskProcessor {
	return &TaskProcessor{db: db, cpuThreshold: cpuThreshold, backend: backend}
}

func (p *TaskProcessor) ProcessTask(ctx context.Context, t *asynq.Task) error {
	switch t.Type() {
	case jobs.TypeDispatchModelCalculation:
		return jobs.HandleDispatchModelCalculation(ctx, t, p.db, p.cpuThreshold, p.backend)
	default:
		logger.ForComponent("asynq_worker").Warnf("unknown task type=%s", t.Type())
		return nil
	}
}
