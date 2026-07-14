package gateway

import (
	"bytes"
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

func TestAppendNewWorkspaceMediaSkipsHistoricalFiles(t *testing.T) {
	ctx := context.Background()
	ws := workspace.NewLocalFS(t.TempDir())
	put := func(name, body string) {
		t.Helper()
		if err := ws.Put(ctx, "agent", "", "chat", name, bytes.NewBufferString(body), int64(len(body)), "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}

	put("old.pdf", "old")
	before, ok := snapshotWorkspacePaths(ctx, ws, "agent", "", "chat")
	if !ok {
		t.Fatal("snapshot failed")
	}

	// Rewriting an old artifact simulates sandbox sync refreshing its mtime.
	put("old.pdf", "old")
	put("current.pdf", "current")

	got := appendNewWorkspaceMedia(ctx, ws, "agent", "", "chat", before, ok, nil)
	if len(got) != 1 || got[0].Filename != "current.pdf" || string(got[0].Bytes) != "current" {
		t.Fatalf("got %#v, want only current.pdf", got)
	}
}

func TestAppendNewWorkspaceMediaRequiresSuccessfulSnapshot(t *testing.T) {
	ctx := context.Background()
	ws := workspace.NewLocalFS(t.TempDir())
	if err := ws.Put(ctx, "agent", "", "chat", "old.pdf", bytes.NewBufferString("old"), 3, "application/pdf"); err != nil {
		t.Fatal(err)
	}

	got := appendNewWorkspaceMedia(ctx, ws, "agent", "", "chat", nil, false, nil)
	if len(got) != 0 {
		t.Fatalf("got %d attachments after failed snapshot, want 0", len(got))
	}
}

func TestSplitFilesFromReplyUsesExplicitFinalDocument(t *testing.T) {
	ctx := context.Background()
	ws := workspace.NewLocalFS(t.TempDir())
	for name, body := range map[string]string{"draft.pdf": "draft", "final.pdf": "final"} {
		if err := ws.Put(ctx, "agent", "", "chat", name, bytes.NewBufferString(body), int64(len(body)), "application/pdf"); err != nil {
			t.Fatal(err)
		}
	}

	text, items := splitFilesFromReply(ctx, ws, "agent", "", "chat", "已完成：[最终报告](/workspace/final.pdf)")
	if text != "已完成：" {
		t.Fatalf("text = %q", text)
	}
	if len(items) != 1 || items[0].Filename != "final.pdf" || string(items[0].Bytes) != "final" {
		t.Fatalf("items = %#v, want only final.pdf", items)
	}
}
