package webhook

import (
	"testing"
)

func TestValidateURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name:    "valid public IP",
			url:     "http://93.184.216.34/hook",
			wantErr: false,
		},
		{
			name:    "invalid scheme ftp",
			url:     "ftp://example.com/hook",
			wantErr: true,
		},
		{
			name:    "loopback IP blocked",
			url:     "http://127.0.0.1/hook",
			wantErr: true,
		},
		{
			name:    "private IP blocked",
			url:     "http://192.168.1.1/hook",
			wantErr: true,
		},
		{
			name:    "link-local IP blocked (AWS metadata)",
			url:     "http://169.254.169.254/hook",
			wantErr: true,
		},
		{
			name:    "garbled URL",
			url:     "://not a valid url%%",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}
