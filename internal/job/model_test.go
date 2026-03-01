package job

import "testing"

func TestIsTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status   Status
		terminal bool
	}{
		{StatusQueued, false},
		{StatusProcessing, false},
		{StatusCompleted, true},
		{StatusFailed, true},
		{StatusCancelled, true},
	}
	for _, tt := range tests {
		if got := tt.status.IsTerminal(); got != tt.terminal {
			t.Errorf("Status(%q).IsTerminal() = %v, want %v", tt.status, got, tt.terminal)
		}
	}
}

func TestValidate_EmptyPrompt(t *testing.T) {
	t.Parallel()
	r := &CreateRequest{Model: "haiku"}
	if err := r.Validate(); err == nil {
		t.Error("expected error for empty prompt, got nil")
	}
}

func TestValidate_InvalidModel(t *testing.T) {
	t.Parallel()
	r := &CreateRequest{Prompt: "hello", Model: "gpt-4"}
	if err := r.Validate(); err == nil {
		t.Error("expected error for invalid model, got nil")
	}
}

func TestValidate_InvalidResponseFormat(t *testing.T) {
	t.Parallel()
	r := &CreateRequest{Prompt: "hello", ResponseFormat: "xml"}
	if err := r.Validate(); err == nil {
		t.Error("expected error for invalid response_format, got nil")
	}
}

func TestValidate_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		req  CreateRequest
	}{
		{"minimal", CreateRequest{Prompt: "hello"}},
		{"with model", CreateRequest{Prompt: "hello", Model: "sonnet"}},
		{"json format", CreateRequest{Prompt: "hello", ResponseFormat: "json"}},
		{"text format", CreateRequest{Prompt: "hello", ResponseFormat: "text"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.req
			if err := r.Validate(); err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}
