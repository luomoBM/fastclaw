package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestChannelReplyEndpointRoundTripExpiryAndReplacement(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	key := []string{"dingtalk", "ding-app", "user:staff-1"}

	if err := db.SaveChannelReplyEndpoint(ctx, key[0], key[1], key[2], "https://old.invalid/hook", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("save endpoint: %v", err)
	}
	if err := db.SaveChannelReplyEndpoint(ctx, key[0], key[1], key[2], "https://new.invalid/hook", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("replace endpoint: %v", err)
	}
	got, err := db.GetChannelReplyEndpoint(ctx, key[0], key[1], key[2])
	if err != nil || got != "https://new.invalid/hook" {
		t.Fatalf("get endpoint = %q, %v; want replacement", got, err)
	}

	if err := db.SaveChannelReplyEndpoint(ctx, key[0], key[1], key[2], "https://expired.invalid/hook", time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("save expired endpoint: %v", err)
	}
	if _, err := db.GetChannelReplyEndpoint(ctx, key[0], key[1], key[2]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired endpoint error = %v; want ErrNotFound", err)
	}
}

func TestDeleteChannelReplyEndpointsRemovesOnlyOneAccount(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	expires := time.Now().Add(time.Hour)
	for _, account := range []string{"app-a", "app-b"} {
		if err := db.SaveChannelReplyEndpoint(ctx, "dingtalk", account, "user:staff-1", "https://example.invalid/"+account, expires); err != nil {
			t.Fatalf("save %s: %v", account, err)
		}
	}
	if err := db.DeleteChannelReplyEndpoints(ctx, "dingtalk", "app-a"); err != nil {
		t.Fatalf("delete account endpoints: %v", err)
	}
	if _, err := db.GetChannelReplyEndpoint(ctx, "dingtalk", "app-a", "user:staff-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted account error = %v; want ErrNotFound", err)
	}
	if _, err := db.GetChannelReplyEndpoint(ctx, "dingtalk", "app-b", "user:staff-1"); err != nil {
		t.Fatalf("other account endpoint was removed: %v", err)
	}
}
