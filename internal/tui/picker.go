package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// pickerItem is one selectable row in an overlay picker.
type pickerItem struct {
	ID    string
	Title string
	Desc  string
}

// pickerModel is a filterable single-select list overlay used for
// session and agent switching.
type pickerModel struct {
	Title  string
	items  []pickerItem
	filter string
	cursor int
	width  int
}

func newPicker(title string, items []pickerItem, width int) *pickerModel {
	return &pickerModel{Title: title, items: items, width: width}
}

func (p *pickerModel) filtered() []pickerItem {
	if p.filter == "" {
		return p.items
	}
	needle := strings.ToLower(p.filter)
	var out []pickerItem
	for _, it := range p.items {
		if strings.Contains(strings.ToLower(it.Title), needle) ||
			strings.Contains(strings.ToLower(it.Desc), needle) ||
			strings.Contains(strings.ToLower(it.ID), needle) {
			out = append(out, it)
		}
	}
	return out
}

// Handle processes one key press. done=true means the picker closed;
// chosen is nil on cancel.
func (p *pickerModel) Handle(key string) (chosen *pickerItem, done bool) {
	items := p.filtered()
	switch key {
	case "esc":
		return nil, true
	case "enter":
		if len(items) == 0 {
			return nil, true
		}
		if p.cursor >= len(items) {
			p.cursor = len(items) - 1
		}
		it := items[p.cursor]
		return &it, true
	case "up", "ctrl+p":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n":
		if p.cursor < len(items)-1 {
			p.cursor++
		}
	case "backspace":
		if p.filter != "" {
			p.filter = p.filter[:len(p.filter)-1]
			p.cursor = 0
		}
	default:
		if len(key) == 1 || len([]rune(key)) == 1 {
			p.filter += key
			p.cursor = 0
		}
	}
	return nil, false
}

func (p *pickerModel) View() string {
	items := p.filtered()
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(p.Title))
	if p.filter != "" {
		b.WriteString("  " + styleMuted.Render("filter: "+p.filter))
	}
	b.WriteString("\n")

	const window = 10
	start := 0
	if p.cursor >= window {
		start = p.cursor - window + 1
	}
	end := min(start+window, len(items))
	if len(items) == 0 {
		b.WriteString(styleMuted.Render("(no matches)"))
	}
	for i := start; i < end; i++ {
		it := items[i]
		line := it.Title
		if it.Desc != "" {
			line += "  " + it.Desc
		}
		maxw := max(p.width-10, 20)
		if lipgloss.Width(line) > maxw {
			line = truncateANSI(line, maxw)
		}
		if i == p.cursor {
			b.WriteString(stylePickerSelected.Render("▸ "+it.Title) + "  " + styleMuted.Render(it.Desc) + "\n")
		} else {
			b.WriteString("  " + it.Title + "  " + styleDim.Render(it.Desc) + "\n")
		}
	}
	if end < len(items) {
		b.WriteString(styleDim.Render("  … more below, type to filter"))
		b.WriteString("\n")
	}
	b.WriteString(styleDim.Render("↑↓ select · Enter confirm · Esc cancel"))
	return stylePickerBox.Width(min(p.width-4, 100)).Render(b.String())
}

// truncateANSI naively truncates by rune count; picker rows are plain
// text at this point so ANSI safety reduces to rune safety.
func truncateANSI(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
