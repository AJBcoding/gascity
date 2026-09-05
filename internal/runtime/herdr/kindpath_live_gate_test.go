package herdr

import (
	"os"
	"strings"
	"testing"
)

func liveClaudeKindPathSkipReason() string {
	if strings.TrimSpace(os.Getenv("GC_FAST_UNIT")) == "0" {
		return ""
	}
	return "skipping live herdr+claude test outside GC_FAST_UNIT=0 process lane"
}

func TestLiveClaudeKindPathSkipReason(t *testing.T) {
	tests := []struct {
		name     string
		fastUnit string
		wantSkip bool
	}{
		{
			name:     "fast unit lane skips live Claude startup",
			fastUnit: "1",
			wantSkip: true,
		},
		{
			name:     "unset fast unit defaults away from live Claude startup",
			fastUnit: "",
			wantSkip: true,
		},
		{
			name:     "process lane keeps live Claude startup",
			fastUnit: "0",
			wantSkip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GC_FAST_UNIT", tt.fastUnit)
			got := liveClaudeKindPathSkipReason()
			if gotSkip := got != ""; gotSkip != tt.wantSkip {
				t.Fatalf("liveClaudeKindPathSkipReason() skip=%v, want %v (reason %q)", gotSkip, tt.wantSkip, got)
			}
		})
	}
}
