package queue

import "testing"

func TestStripCodeFences(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "standard JSON fence",
			input: "```json\n{\"key\":\"value\"}\n```",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "plain fence",
			input: "```\n{\"key\":\"value\"}\n```",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "no fence unchanged",
			input: "{\"key\":\"value\"}",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "only whitespace trimmed",
			input: "  {\"key\":\"value\"}  ",
			want:  "{\"key\":\"value\"}",
		},
		{
			name:  "trailing newline after closing fence",
			input: "```json\n{\"a\":1}\n```\n",
			want:  "{\"a\":1}",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only opening fence no newline",
			input: "```",
			// HasPrefix matches, no newline so opening fence is kept, but HasSuffix
			// also matches the same "```" so the closing fence removal strips it,
			// leaving an empty string after TrimSpace.
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripCodeFences(tt.input)
			if got != tt.want {
				t.Errorf("stripCodeFences(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
