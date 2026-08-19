package tui

import "strings"

// bannerLines is "FASTCLAW" in the ANSI Shadow figlet font, matching
// the codeany wordmark style.
var bannerLines = []string{
	"███████╗ █████╗ ███████╗████████╗ ██████╗██╗      █████╗ ██╗    ██╗",
	"██╔════╝██╔══██╗██╔════╝╚══██╔══╝██╔════╝██║     ██╔══██╗██║    ██║",
	"█████╗  ███████║███████╗   ██║   ██║     ██║     ███████║██║ █╗ ██║",
	"██╔══╝  ██╔══██║╚════██║   ██║   ██║     ██║     ██╔══██║██║███╗██║",
	"██║     ██║  ██║███████║   ██║   ╚██████╗███████╗██║  ██║╚███╔███╔╝",
	"╚═╝     ╚═╝  ╚═╝╚══════╝   ╚═╝    ╚═════╝╚══════╝╚═╝  ╚═╝ ╚══╝╚══╝ ",
}

// bannerIndent is the left padding applied to every wordmark row.
const bannerIndent = "    "

// renderBanner draws the FASTCLAW wordmark. Returns "" when the
// terminal is too narrow to fit it, letting the caller fall back to a
// plain one-line title.
func renderBanner(width int) string {
	if width > 0 && width < len([]rune(bannerLines[0]))+len(bannerIndent)+1 {
		return ""
	}
	var b strings.Builder
	for i, line := range bannerLines {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(stylePrimary.Render(bannerIndent + line))
	}
	return b.String()
}

// renderTips lists the key bindings shown inside the welcome box.
func renderTips() string {
	pair := func(key, desc string) string {
		return styleDim.Render(key) + styleMuted.Render(" "+desc)
	}
	return pair("Enter", "send") + pair("  Ctrl+J", "newline") + pair("  /help", "commands") +
		"\n" +
		pair("Ctrl+C", "interrupt") + pair("  Esc", "detach") +
		pair("  Ctrl+L", "clear") + pair("  !cmd", "shell")
}
