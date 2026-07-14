package session

import "testing"

func TestDisplaySessionTitle(t *testing.T) {
	tests := []struct {
		name        string
		storedTitle string
		sessionKey  string
		preview     string
		want        string
	}{
		{
			name:       "empty title uses first user message",
			sessionKey: "s-1783753587119-lvlph0",
			preview:    "帮我分析一下这个问题",
			want:       "帮我分析一下这个问题",
		},
		{
			name:        "legacy session id title uses first user message",
			storedTitle: "s-1783753587119-lvlph0",
			sessionKey:  "s-1783753587119-lvlph0",
			preview:     "帮我分析一下这个问题",
			want:        "帮我分析一下这个问题",
		},
		{
			name:        "custom title is preserved",
			storedTitle: "故障排查",
			sessionKey:  "s-1783753587119-lvlph0",
			preview:     "帮我分析一下这个问题",
			want:        "故障排查",
		},
		{
			name:        "surrounding whitespace is normalized",
			storedTitle: "  故障排查  ",
			sessionKey:  "s-1783753587119-lvlph0",
			preview:     "帮我分析一下这个问题",
			want:        "故障排查",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displaySessionTitle(tt.storedTitle, tt.sessionKey, tt.preview); got != tt.want {
				t.Fatalf("displaySessionTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}
