package tui

import (
	"fmt"
	"os"
	"time"
)

// debugLog appends one line to $FASTCLAW_TUI_DEBUG when set. The TUI
// owns the terminal, so printf-debugging goes to a file instead.
func debugLog(format string, args ...any) {
	path := os.Getenv("FASTCLAW_TUI_DEBUG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05.000")}, args...)...)
}
