package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallDSHCursorGrok(t *testing.T) {
	dir := t.TempDir()
	dsh := filepath.Join(dir, "dsh")
	cur := filepath.Join(dir, "cursor")
	grok := filepath.Join(dir, "grok")
	os.MkdirAll(dsh, 0o755)
	os.MkdirAll(cur, 0o755)
	os.MkdirAll(grok, 0o755)
	t.Setenv("DSH_HOME", dsh)
	t.Setenv("X_MEM_CURSOR_DIR", cur)
	t.Setenv("GROK_HOME", grok)

	// Existing user entries must stay intact.
	os.WriteFile(filepath.Join(dsh, "cordis.patch.yml"), []byte("- id: system-prompt\n  config:\n    personaPrefix: hi\n"), 0o644)
	os.WriteFile(filepath.Join(cur, "hooks.json"), []byte(`{"version":1,"hooks":{"sessionStart":[{"command":"other-hook"}]}}`), 0o644)
	os.WriteFile(filepath.Join(grok, "config.toml"), []byte("model = \"grok-4\"\n"), 0o644)

	in := installer{exe: "/opt/x-mem", apply: true}
	if err := in.dsh(); err != nil {
		t.Fatalf("dsh install: %v", err)
	}
	if err := in.cursor(); err != nil {
		t.Fatalf("cursor install: %v", err)
	}
	if err := in.grok(); err != nil {
		t.Fatalf("grok install: %v", err)
	}

	patch, _ := os.ReadFile(filepath.Join(dsh, "cordis.patch.yml"))
	if !strings.Contains(string(patch), "personaPrefix: hi") || !strings.Contains(string(patch), "mcp-x-mem") || !strings.Contains(string(patch), "hooks-x-mem") {
		t.Fatalf("dsh patch: %s", patch)
	}
	curHooks, _ := os.ReadFile(filepath.Join(cur, "hooks.json"))
	if !strings.Contains(string(curHooks), "other-hook") || !strings.Contains(string(curHooks), "--client cursor") {
		t.Fatalf("cursor hooks: %s", curHooks)
	}
	if !exists(filepath.Join(cur, "rules", "x-mem.mdc")) {
		t.Fatal("cursor rule missing")
	}
	grokCfg, _ := os.ReadFile(filepath.Join(grok, "config.toml"))
	if !strings.Contains(string(grokCfg), `model = "grok-4"`) || !strings.Contains(string(grokCfg), "[mcp_servers.x-mem]") {
		t.Fatalf("grok config: %s", grokCfg)
	}

	var oks []string
	doctorExtraClients(map[string]bool{"DSH": true, "Cursor": true, "Grok CLI": true},
		func(f string, a ...any) { oks = append(oks, f) },
		func(string, ...any) {},
		func(f string, a ...any) { t.Fatalf("doctor fail: "+f, a...) },
	)
	if len(oks) != 3 {
		t.Fatalf("want 3 ok clients in doctor, got %v", oks)
	}

	// Remove leaves existing user config untouched and cleans up ours.
	rm := installer{exe: "/opt/x-mem", apply: true, remove: true}
	if err := rm.dsh(); err != nil {
		t.Fatal(err)
	}
	if err := rm.cursor(); err != nil {
		t.Fatal(err)
	}
	if err := rm.grok(); err != nil {
		t.Fatal(err)
	}
	patch, _ = os.ReadFile(filepath.Join(dsh, "cordis.patch.yml"))
	if strings.Contains(string(patch), "x-mem") || !strings.Contains(string(patch), "personaPrefix: hi") {
		t.Fatalf("dsh after remove: %s", patch)
	}
	curHooks, _ = os.ReadFile(filepath.Join(cur, "hooks.json"))
	if strings.Contains(string(curHooks), "x-mem") || !strings.Contains(string(curHooks), "other-hook") {
		t.Fatalf("cursor after remove: %s", curHooks)
	}
	grokCfg, _ = os.ReadFile(filepath.Join(grok, "config.toml"))
	if strings.Contains(string(grokCfg), "x-mem") || !strings.Contains(string(grokCfg), `model = "grok-4"`) {
		t.Fatalf("grok after remove: %s", grokCfg)
	}
}
