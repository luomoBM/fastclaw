package agent

import (
	"testing"
	"time"
)

// TestEffectiveSubagentTimeout pins the runtime fallback: a zero or
// negative configured budget falls back to the historical 15-minute
// default, a positive one is used verbatim.
func TestEffectiveSubagentTimeout(t *testing.T) {
	cases := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"unset falls back to 15m", 0, 15 * time.Minute},
		{"negative falls back to 15m", -time.Minute, 15 * time.Minute},
		{"explicit 30m wins", 30 * time.Minute, 30 * time.Minute},
		{"explicit 90s wins", 90 * time.Second, 90 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{subagentTimeout: tc.set}
			if got := a.effectiveSubagentTimeout(); got != tc.want {
				t.Fatalf("effectiveSubagentTimeout() = %s, want %s", got, tc.want)
			}
		})
	}
}
