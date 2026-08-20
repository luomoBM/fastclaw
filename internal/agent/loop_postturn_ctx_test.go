package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/agent/tools"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// blockingProvider is a provider.Provider whose Chat parks until the
// test releases it, honoring ctx cancellation exactly like the real
// HTTP-backed providers do.
type blockingProvider struct {
	entered chan struct{} // signals Chat was entered (buffered 1)
	release chan struct{} // test closes it to let the call succeed
}

func newBlockingProvider() *blockingProvider {
	return &blockingProvider{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (p *blockingProvider) Chat(ctx context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Response, error) {
	select {
	case p.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return &provider.Response{
			Content: `{"memory_facts":["fact-from-test"],"user_notes":[]}`,
		}, nil
	}
}

func (p *blockingProvider) ChatStream(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.StreamReader, error) {
	return nil, errors.New("not used by this test")
}

// countOnlyStore embeds store.Store and overrides only the method the
// autoPersist gate reads. Any other call would nil-panic, which is the
// desired tripwire: runPostTurn must not touch the rest of the Store.
type countOnlyStore struct {
	store.Store
	n int
}

func (s *countOnlyStore) CountChatterUserMessages(context.Context, string, string) (int, error) {
	return s.n, nil
}

// TestRunPostTurnAutoPersistSurvivesTurnCtxCancel — runPostTurn's
// async work (auto-persist LLM extraction, skills learner) escapes the
// turn via `go`, but the task queue cancels the turn context the
// moment its handler returns. The goroutine is then racing a cancel
// that fires milliseconds later — an LLM call virtually always loses.
// Post-turn bookkeeping that must outlive the turn has to detach from
// the turn's cancellation (the workspace-history commit below it
// already learned this and uses context.Background() for exactly this
// reason).
func TestRunPostTurnAutoPersistSurvivesTurnCtxCancel(t *testing.T) {
	ws := t.TempDir()
	chatterMem := NewMemoryWithStoreForUser(ws, nil, "chatter-1", "agent-test")
	prov := newBlockingProvider()

	a := &Agent{
		name:      "agent-test",
		hooks:     NewHookRegistry(),
		registry:  tools.NewRegistry("", ""),
		provider:  prov,
		dataStore: &countOnlyStore{n: 1},
		memoryCfg: config.MemoryCfg{AutoPersist: config.AutoPersistCfg{
			Enabled:     true,
			EveryNTurns: 1,
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.runPostTurn(ctx, bus.InboundMessage{},
		[]provider.Message{{Role: "user", Content: "hello"}}, 1, chatterMem)

	select {
	case <-prov.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("auto-persist LLM call never started — gate did not fire")
	}
	// Simulate the task queue's `defer cancel()`: the turn handler has
	// returned while the spawned goroutine is still mid-LLM-call.
	cancel()
	// Now let the LLM call finish — after the turn ctx died.
	close(prov.release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(ws, "MEMORY.md"))
		if err == nil && strings.Contains(string(data), "fact-from-test") {
			return // write survived the turn's cancellation
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("auto-persist write was killed by turn-ctx cancellation — MEMORY.md never written")
}
