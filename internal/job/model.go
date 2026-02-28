package job

import (
	"encoding/json"
	"errors"
	"time"
)

type Status string

const (
	StatusQueued     Status = "queued"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

// IsTerminal returns true for statuses that represent a final state.
func (s Status) IsTerminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

var validModels = map[string]bool{
	"haiku":  true,
	"sonnet": true,
	"opus":   true,
}

type Job struct {
	ID             string          `json:"job_id"`
	Prompt         string          `json:"prompt"`
	SystemPrompt   string          `json:"system_prompt,omitempty"`
	Model          string          `json:"model"`
	Status         Status          `json:"status"`
	Result         string          `json:"result,omitempty"`
	Error          string          `json:"error,omitempty"`
	CallbackURL    string          `json:"callback_url,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	ResponseFormat string          `json:"response_format,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
}

// CreateRequest is the payload used to submit a new job.
type CreateRequest struct {
	Prompt         string          `json:"prompt"`
	SystemPrompt   string          `json:"system_prompt,omitempty"`
	Model          string          `json:"model,omitempty"`
	CallbackURL    string          `json:"callback_url,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	ResponseFormat string          `json:"response_format,omitempty"`
}

func (r *CreateRequest) Validate() error {
	if r.Prompt == "" {
		return errors.New("prompt must not be empty")
	}
	if r.Model != "" && !validModels[r.Model] {
		return errors.New("model must be one of: haiku, sonnet, opus")
	}
	if r.ResponseFormat != "" && r.ResponseFormat != "text" && r.ResponseFormat != "json" {
		return errors.New("response_format must be 'text' or 'json'")
	}
	return nil
}
