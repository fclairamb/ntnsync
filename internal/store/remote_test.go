package store

import "testing"

func TestParseDepthEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		val  string
		want int
	}{
		{"unset", "", 1},
		{"explicit full history", "0", 0},
		{"explicit depth", "5", 5},
		{"garbage", "not-a-number", 1},
		{"negative", "-3", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := parseDepthEnv(tt.val); got != tt.want {
				t.Errorf("parseDepthEnv(%q) = %d, want %d", tt.val, got, tt.want)
			}
		})
	}
}

func TestLoadRemoteConfigFromEnv_Depth(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		wantVal int
	}{
		{"unset defaults to 1", "", 1},
		{"zero means full history", "0", 0},
		{"positive depth", "5", 5},
		{"garbage falls back to default", "nonsense", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NTN_GIT_DEPTH", tt.envVal)

			cfg := LoadRemoteConfigFromEnv()
			if cfg.Depth != tt.wantVal {
				t.Errorf("Depth = %d, want %d", cfg.Depth, tt.wantVal)
			}
		})
	}
}
