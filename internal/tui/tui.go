// Package tui is the interactive terminal front-end for `fastclaw
// chat`. It is a thin client over the gateway's /api/chat endpoints:
// the agent loop runs server-side; this package renders the event
// stream and manages input, sessions, and agent switching.
package tui

import tea "github.com/charmbracelet/bubbletea"

// printBuffer is how many committed blocks may queue up before the model
// starts deferring them to the next message. Prints drain as fast as the
// event loop runs, so this only absorbs bursts.
const printBuffer = 256

// Run starts the chat TUI and blocks until the user exits.
func Run(opts Options) error {
	m := NewModel(opts)
	// No alt-screen, no mouse capture: finished blocks are printed into
	// the terminal's normal buffer, so scrolling and text selection stay
	// native. Only the live tail below them is redrawn by Bubble Tea.
	p := tea.NewProgram(m)
	m.SetProgram(p)

	// One goroutine owns scrollback writes so they stay in the order the
	// model committed them. Program.Println blocks until the event loop
	// takes the message, which is why this cannot run on the event loop.
	printCh := make(chan string, printBuffer)
	m.printCh = printCh
	go func() {
		for s := range printCh {
			p.Println(s)
		}
	}()

	_, err := p.Run()
	// Deliberately not waiting for the printer: Program.Println is an
	// uncancellable send on a channel only the (now stopped) event loop
	// drains, so a print in flight here would block forever. Nothing is
	// lost by returning — the renderer is already down.
	close(printCh)
	return err
}
