package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	endpointhooks "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/hooks"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/testenv"
	"github.com/spf13/cobra"
)

// hermesSweepHome sandboxes a user-mode `beacon endpoint hooks` run and returns the Hermes native
// root inside it.
func hermesSweepHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	testenv.SetHome(t, home)
	for _, name := range []string{"HERMES_HOME", "HERMES_DATA_DIR_SUFFIX", "CODEX_HOME", "KIRO_HOME", "KIMI_CODE_HOME", "DSH_HOME"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if runtime.GOOS != "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	}

	saved := endpointOpts
	t.Cleanup(func() { endpointOpts = saved })
	endpointOpts.userMode = true
	endpointOpts.systemMode = false
	endpointOpts.dryRun = false
	endpointOpts.hookLevel = "user"
	endpointOpts.logPath = filepath.Join(home, "logs", "runtime.jsonl")

	native := filepath.Join(home, ".hermes")
	if runtime.GOOS == "windows" {
		native = filepath.Join(os.Getenv("LOCALAPPDATA"), "hermes")
	}
	return home, native
}

func breakHermesActiveProfile(t *testing.T, native string) {
	t.Helper()
	if err := os.MkdirAll(native, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "active_profile"), []byte("gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An active_profile Hermes cannot use is a Hermes problem, not a reason to leave every runtime after
// Hermes in the --all list without hooks. Install reports Hermes and carries on; uninstall still
// removes the hooks an earlier install wrote to the base directory.
func TestEndpointHooksAllSweepSurvivesUnusableHermesProfile(t *testing.T) {
	_, native := hermesSweepHome(t)
	logPath := endpointOpts.logPath
	opts := endpointhooks.HermesOptions{Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true}

	// Hooks written before the profile went bad, in the base directory's config.yaml.
	if _, err := endpointhooks.InstallHermes(opts); err != nil {
		t.Fatalf("InstallHermes: %v", err)
	}
	baseConfig := filepath.Join(native, "config.yaml")
	breakHermesActiveProfile(t, native)

	endpointOpts.allTargets = true
	endpointOpts.hookHarnesses = ""
	var stderr bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetErr(&stderr)
	if _, err := captureStdout(t, func() error { return runEndpointHooksInstall(cmd, nil) }); err != nil {
		t.Fatalf("hooks install --all returned error: %v", err)
	}
	if !strings.Contains(stderr.String(), "hermes") || !strings.Contains(stderr.String(), "active profile") {
		t.Fatalf("install --all did not report the Hermes profile problem; stderr: %q", stderr.String())
	}
	if !endpointhooks.IsDevinInstalled(endpointhooks.DevinOptions{Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true}) {
		t.Fatal("devin-cli, after hermes in the --all list, was not installed")
	}
	if !endpointhooks.DevinDesktopHookStatus(endpointhooks.DevinDesktopOptions{Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true}).Installed {
		t.Fatal("devin-desktop, after hermes in the --all list, was not installed")
	}
	if !endpointhooks.IsAntigravityInstalled(endpointhooks.AntigravityOptions{Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true}) {
		t.Fatal("antigravity, last in the --all list, was not installed")
	}
	if _, err := os.Stat(filepath.Join(native, "profiles", "gone")); !os.IsNotExist(err) {
		t.Fatalf("install --all created the missing profile: %v", err)
	}

	if _, err := captureStdout(t, func() error { return runEndpointHooksUninstall(cmd, nil) }); err != nil {
		t.Fatalf("hooks uninstall --all returned error: %v", err)
	}
	if _, err := os.Stat(baseConfig); !os.IsNotExist(err) {
		t.Fatalf("uninstall --all left the base directory's Hermes hooks in %s: %v", baseConfig, err)
	}
	if endpointhooks.IsAntigravityInstalled(endpointhooks.AntigravityOptions{Level: endpointhooks.LevelUser, LogPath: logPath, UserMode: true}) {
		t.Fatal("uninstall --all stopped before antigravity")
	}
}

// Asking for Hermes by name is different: the operator wants exactly that install, so a profile
// Hermes cannot use is an error that names it.
func TestEndpointHooksInstallHermesExplicitRefusesUnusableProfile(t *testing.T) {
	_, native := hermesSweepHome(t)
	breakHermesActiveProfile(t, native)
	endpointOpts.allTargets = false
	endpointOpts.hookHarnesses = "hermes"

	_, err := captureStdout(t, func() error { return runEndpointHooksInstall(&cobra.Command{}, nil) })
	if err == nil || !strings.Contains(err.Error(), "active profile") {
		t.Fatalf("hooks install --harness hermes err = %v, want the active profile named", err)
	}
	if _, statErr := os.Stat(filepath.Join(native, "config.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused install wrote the base config.yaml: %v", statErr)
	}
}
