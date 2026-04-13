package store

import (
	"errors"
	"fmt"
	"time"

	"platform.local/common/pkg/models"

	"gorm.io/gorm"
)

type WebserviceRepository struct {
	db *gorm.DB
}

func NewWebserviceRepository(db *gorm.DB) *WebserviceRepository {
	return &WebserviceRepository{db: db}
}

type WebserviceFilters struct {
	Status    string
	Available string
	Busy      string
	Search    string
	Page      int
	PerPage   int
}

func (r *WebserviceRepository) Create(ws *models.WebserviceInstance) error {
	return r.db.Create(ws).Error
}

func (r *WebserviceRepository) Get(id uint) (*models.WebserviceInstance, error) {
	var m models.WebserviceInstance
	if err := r.db.First(&m, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to get webservice: %w", err)
	}
	return &m, nil
}

func (r *WebserviceRepository) List(f WebserviceFilters) ([]models.WebserviceInstance, int64, error) {
	var list []models.WebserviceInstance
	var total int64

	q := r.db.Model(&models.WebserviceInstance{})
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Available == "true" {
		q = q.Where("available = ?", true)
	} else if f.Available == "false" {
		q = q.Where("available = ?", false)
	}
	if f.Busy == "true" {
		q = q.Where("busy = ?", true)
	} else if f.Busy == "false" {
		q = q.Where("busy = ?", false)
	}
	if f.Search != "" {
		like := fmt.Sprintf("%%%s%%", f.Search)
		q = q.Where("name ILIKE ? OR ip ILIKE ?", like, like)
	}

	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := f.Page
	per := f.PerPage
	if per <= 0 {
		per = 20
	}
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * per
	if err := q.Offset(offset).Limit(per).Order("id DESC").Find(&list).Error; err != nil {
		return nil, 0, err
	}
	return list, total, nil
}

func (r *WebserviceRepository) Update(id uint, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	return r.db.Model(&models.WebserviceInstance{}).
		Where("id = ?", id).
		Updates(updates).Error
}

func (r *WebserviceRepository) Delete(id uint) error {
	return r.db.Delete(&models.WebserviceInstance{}, id).Error
}

func (r *WebserviceRepository) UpdateStatus(id uint, status string, more map[string]interface{}) error {
	if more == nil {
		more = map[string]interface{}{}
	}
	more["status"] = status
	return r.Update(id, more)
}
