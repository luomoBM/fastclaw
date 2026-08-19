package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEnterSubmitsTurn(t *testing.T) {
	m := newTestModel()
	for _, r := range "hi" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := m.input.Value(); got != "hi" {
		t.Fatalf("input value = %q", got)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.querying {
		t.Fatal("enter did not start a turn")
	}
	if cmd == nil {
		t.Fatal("no command returned from submit")
	}
}

func TestCtrlJInsertsNewlineWithoutSubmitting(t *testing.T) {
	m := newTestModel()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("first")})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.querying {
		t.Fatal("Ctrl+J submitted instead of inserting a newline")
	}
	if cmd != nil {
		t.Fatal("Ctrl+J unexpectedly returned a submit command")
	}
	if got := m.input.RawValue(); got != "first\n" {
		t.Fatalf("input value = %q, want a trailing newline", got)
	}
}

func TestClipboardImageCanSubmitWithoutText(t *testing.T) {
	m := newTestModel()
	m.Update(clipboardImageMsg{dataURL: "data:image/png;base64,eA=="})
	if len(m.pendingImages) != 1 {
		t.Fatalf("pending images = %d", len(m.pendingImages))
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.querying || cmd == nil {
		t.Fatal("image-only enter did not start a turn")
	}
	if len(m.pendingImages) != 0 {
		t.Fatal("sent image remained attached to composer")
	}
	if len(m.blocks) != 1 || m.blocks[0].Content != "[image]" {
		t.Fatalf("image-only display block = %#v", m.blocks)
	}
}

func TestEscapeClearsClipboardImages(t *testing.T) {
	m := newTestModel()
	m.pendingImages = []string{"data:image/png;base64,eA=="}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.pendingImages) != 0 {
		t.Fatal("escape did not clear attached image")
	}
}
