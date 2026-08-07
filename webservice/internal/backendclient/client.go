// Package backendclient calls the backend's internal lifecycle API; the webservice never writes the models table itself.
package backendclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"platform.local/common/pkg/contracts"
)

// Lifecycle is the interface the dispatcher and scheduler use to request model status changes.
type Lifecycle interface {
	// MarkRunning claims a queued model; claimed=false means it was already taken.
	MarkRunning(ctx context.Context, modelID, webserviceID uint) (claimed bool, err error)
	// MarkFailed fails an in-flight model with a reason.
	MarkFailed(ctx context.Context, modelID uint, reason string) error
	// SetRunSession saves session metadata; no status change.
	SetRunSession(ctx context.Context, modelID uint, sessionID *int64, callbackURL *string) error
	// ActiveModels returns the queued/running models.
	ActiveModels(ctx context.Context) ([]contracts.ActiveModel, error)
}

// HTTPClient is the HTTP adapter for Lifecycle.
type HTTPClient struct {
	base   string
	secret string
	http   *http.Client
}

// New builds an HTTP lifecycle client targeting the backend's internal API.
func New(baseURL, callbackSecret string) *HTTPClient {
	return &HTTPClient{
		base:   baseURL,
		secret: callbackSecret,
		http:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body any) (int, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, fmt.Errorf("encode request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, &buf)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.secret != "" {
		req.Header.Set("X-Callback-Secret", c.secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("call backend %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func (c *HTTPClient) MarkRunning(ctx context.Context, modelID, webserviceID uint) (bool, error) {
	path := fmt.Sprintf("/api/internal/models/%d/mark-running", modelID)
	status, err := c.do(ctx, http.MethodPost, path, contracts.MarkRunningRequest{WebserviceID: webserviceID})
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusConflict:
		return false, nil // model no longer queued — caller releases the instance
	default:
		return false, fmt.Errorf("mark-running unexpected status %d", status)
	}
}

func (c *HTTPClient) MarkFailed(ctx context.Context, modelID uint, reason string) error {
	path := fmt.Sprintf("/api/internal/models/%d/mark-failed", modelID)
	status, err := c.do(ctx, http.MethodPost, path, contracts.MarkFailedRequest{Reason: reason})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("mark-failed unexpected status %d", status)
	}
	return nil
}

func (c *HTTPClient) ActiveModels(ctx context.Context) ([]contracts.ActiveModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/internal/models/active", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.secret != "" {
		req.Header.Set("X-Callback-Secret", c.secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call backend active-models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("active-models unexpected status %d", resp.StatusCode)
	}
	var out contracts.ActiveModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode active-models: %w", err)
	}
	return out.Models, nil
}

func (c *HTTPClient) SetRunSession(ctx context.Context, modelID uint, sessionID *int64, callbackURL *string) error {
	path := fmt.Sprintf("/api/internal/models/%d/run-session", modelID)
	status, err := c.do(ctx, http.MethodPatch, path, contracts.RunSessionRequest{
		SessionID:   sessionID,
		CallbackURL: callbackURL,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("run-session unexpected status %d", status)
	}
	return nil
}
