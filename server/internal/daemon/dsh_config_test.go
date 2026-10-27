package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

// TestLoadConfigDshHome verifies that profile checks and child processes share
// the persisted home, including when a GUI launches the daemon without DSH_HOME.
func TestLoadConfigDshHome(t *testing.T) {
	stageFakeAgent(t)
	redirectTestHome(t, t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := os.Unsetenv("DSH_HOME"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MULTICA_DSH_PATH", "")
	if err := os.Unsetenv("MULTICA_DSH_PATH"); err != nil {
		t.Fatal(err)
	}
	dshHome := t.TempDir()
	installMulticaProfile(t, dshHome)
	executable := filepath.Join(t.TempDir(), "fixture-dsh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+dshProbeOKFrame+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := cli.CLIConfig{Backends: &cli.BackendOverrides{DSH: &cli.DSHOverride{HomeDir: dshHome, BinaryPath: executable}}}
	const profile = "dsh-test"
	if err := cli.SaveCLIConfigForProfile(config, profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(Overrides{Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DSH_HOME") != dshHome || !dshMulticaProfilePresent() {
		t.Fatal("daemon did not use the configured DSH profile home")
	}
	if loaded.Agents["dsh"].Path != executable {
		t.Fatalf("DSH executable = %q, want %q", loaded.Agents["dsh"].Path, executable)
	}
	if verdict := probeDshMulticaProfile(context.Background(), executable); verdict != dshProbeOK {
		t.Fatalf("profile verdict = %v, want OK", verdict)
	}
}

// TestApplyDshOverrideEnvWins preserves explicit environment configuration.
func TestApplyDshOverrideEnvWins(t *testing.T) {
	t.Setenv("DSH_HOME", t.TempDir())
	t.Setenv("MULTICA_DSH_PATH", "/explicit/dsh")
	wantHome := os.Getenv("DSH_HOME")
	applyDshOverride(&cli.DSHOverride{HomeDir: "/config/home", BinaryPath: "/config/dsh"})
	if os.Getenv("DSH_HOME") != wantHome || os.Getenv("MULTICA_DSH_PATH") != "/explicit/dsh" {
		t.Fatal("configuration replaced explicit environment values")
	}
}

// TestDshWrappedHomeIsNotMissing prevents an unreadable implicit home from
// condemning a runtime whose launcher can select its own DSH_HOME.
func TestDshWrappedHomeIsNotMissing(t *testing.T) {
	executable := dshProbeFixture(t, "exit 1", false)
	redirectTestHome(t, t.TempDir())
	if err := os.Unsetenv("DSH_HOME"); err != nil {
		t.Fatal(err)
	}
	if verdict := probeDshMulticaProfile(context.Background(), executable); verdict != dshProbeUnavailable {
		t.Fatalf("wrapped profile verdict = %v, want unavailable", verdict)
	}
}
