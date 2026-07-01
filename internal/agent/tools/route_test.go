package tools

import (
	"context"
	"testing"
	"time"
)

type fakeExecutor struct{}

func (fakeExecutor) Exec(context.Context, string, time.Duration) (string, error) { return "", nil }
func (fakeExecutor) ReadFile(context.Context, string) (string, error)            { return "", nil }
func (fakeExecutor) WriteFile(context.Context, string, string) (string, error)   { return "", nil }
func (fakeExecutor) ListDir(context.Context, string) (string, error)             { return "", nil }
func (fakeExecutor) Backend() string                                             { return "test" }
func (fakeExecutor) Close() error                                                { return nil }

func TestFastClawInternalPathsIncludeRootAndEnvHome(t *testing.T) {
	t.Setenv("FASTCLAW_HOME", "/srv/fastclaw")

	cases := []string{
		"~/.fastclaw",
		"~/.fastclaw/workspaces",
		"/root/.fastclaw",
		"/root/.fastclaw/workspaces",
		"/srv/fastclaw",
		"/srv/fastclaw/workspaces",
	}
	for _, path := range cases {
		if !isFastClawInternalPath(path) {
			t.Fatalf("isFastClawInternalPath(%q) = false, want true", path)
		}
		if got, ok := hostHomePath(path); ok || got != "" {
			t.Fatalf("hostHomePath(%q) = (%q, %v), want denied", path, got, ok)
		}
	}
}

func TestRouteForDoesNotExposeFastClawInternalsViaHostFS(t *testing.T) {
	r := &Registry{executor: fakeExecutor{}}

	if got := r.routeFor("/root/.fastclaw/workspaces", OpList); got != RouteSandbox {
		t.Fatalf("routeFor FastClaw internal path = %v, want RouteSandbox", got)
	}
	if got := r.routeFor("/Users/maxwell/Documents/report.txt", OpRead); got != RouteHostFS {
		t.Fatalf("routeFor explicit host document = %v, want RouteHostFS", got)
	}
}
