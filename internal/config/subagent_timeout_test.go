package config

import "testing"

// TestApplyDefaultsSubagentTimeout pins the system default: an unset
// agents.defaults.subagentTimeoutSec resolves to the 15-min constant,
// matching the hard-coded budget runSubagentLoop used before the knob
// existed.
func TestApplyDefaultsSubagentTimeout(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	if got := cfg.Agents.Defaults.SubagentTimeoutSec; got != DefaultSubagentTimeoutSec {
		t.Fatalf("default SubagentTimeoutSec = %d, want %d", got, DefaultSubagentTimeoutSec)
	}
	// An explicit value survives ApplyDefaults untouched.
	cfg = &Config{}
	cfg.Agents.Defaults.SubagentTimeoutSec = 1800
	ApplyDefaults(cfg)
	if got := cfg.Agents.Defaults.SubagentTimeoutSec; got != 1800 {
		t.Fatalf("explicit SubagentTimeoutSec = %d, want 1800", got)
	}
}

// TestMergedAgentConfigSubagentTimeout walks the override chain:
// agents.defaults → AgentEntry → AgentFileConfig (agents.config row),
// each layer only applying when positive.
func TestMergedAgentConfigSubagentTimeout(t *testing.T) {
	restore := AgentFileConfigLoader
	defer func() { AgentFileConfigLoader = restore }()

	// No overrides anywhere: resolved inherits the defaults value.
	AgentFileConfigLoader = func(_, _ string) (AgentFileConfig, bool) {
		return AgentFileConfig{}, false
	}
	cfg := &Config{}
	ApplyDefaults(cfg)
	got := cfg.MergedAgentConfig(AgentEntry{ID: "agt_test"})
	if got.SubagentTimeoutSec != DefaultSubagentTimeoutSec {
		t.Fatalf("no-override SubagentTimeoutSec = %d, want %d", got.SubagentTimeoutSec, DefaultSubagentTimeoutSec)
	}

	// Entry-level override wins over defaults.
	got = cfg.MergedAgentConfig(AgentEntry{ID: "agt_test", SubagentTimeoutSec: 1800})
	if got.SubagentTimeoutSec != 1800 {
		t.Fatalf("entry override SubagentTimeoutSec = %d, want 1800", got.SubagentTimeoutSec)
	}

	// File-config (agents.config row) override wins over entry.
	AgentFileConfigLoader = func(_, _ string) (AgentFileConfig, bool) {
		return AgentFileConfig{SubagentTimeoutSec: 3600}, true
	}
	got = cfg.MergedAgentConfig(AgentEntry{ID: "agt_test", SubagentTimeoutSec: 1800})
	if got.SubagentTimeoutSec != 3600 {
		t.Fatalf("file-config override SubagentTimeoutSec = %d, want 3600", got.SubagentTimeoutSec)
	}

	// Non-positive entry values are ignored, not merged as 0.
	AgentFileConfigLoader = func(_, _ string) (AgentFileConfig, bool) {
		return AgentFileConfig{}, false
	}
	got = cfg.MergedAgentConfig(AgentEntry{ID: "agt_test", SubagentTimeoutSec: -5})
	if got.SubagentTimeoutSec != DefaultSubagentTimeoutSec {
		t.Fatalf("negative entry SubagentTimeoutSec = %d, want %d (default)", got.SubagentTimeoutSec, DefaultSubagentTimeoutSec)
	}
}
