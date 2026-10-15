package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/hermessession"
)

type HermesOptions struct {
	Level    Level
	LogPath  string
	UserMode bool
}

type HermesStatus struct {
	Installed  bool   `json:"installed"`
	BinaryPath string `json:"binary_path,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	Message    string `json:"message,omitempty"`
	// StaleConfigPaths are config.yaml files other than ConfigPath that still hold Beacon hooks
	// from an earlier install. Hermes does not read them now, but reads them again once
	// HERMES_HOME or the active profile changes back, so status names them and uninstall removes
	// Beacon's entries from them.
	StaleConfigPaths []string `json:"stale_config_paths,omitempty"`
}

type hermesConfig struct {
	values map[string]interface{}
	hooks  map[string][]hermesHookRef
}

type hermesHookRef struct {
	Matcher string `yaml:"matcher,omitempty"`
	Command string `yaml:"command"`
	Timeout int    `yaml:"timeout,omitempty"`
}

var hermesRuntime = hookRuntime{
	displayName: "Hermes Agent",
	configPath:  hermesConfigPath,
	install:     installHermesConfig,
	uninstall:   removeHermesEndpointHooks,
	isInstalled: isHermesInstalledAt,
}

// hermesExistingRuntime is hermesRuntime for uninstall and status, which look at hooks already
// written rather than choosing where new ones go. When the active profile is unusable they look in
// the base directory, where an install made before the profile went bad put them; install still
// refuses, because Hermes would never load hooks written there while the profile is selected.
var hermesExistingRuntime = hookRuntime{
	displayName: "Hermes Agent",
	configPath:  hermesExistingConfigPath,
	install:     installHermesConfig,
	uninstall:   removeHermesEndpointHooks,
	isInstalled: isHermesInstalledAt,
}

func InstallHermes(opts HermesOptions) (HermesStatus, error) {
	status, err := installRuntimeHooks(hermesRuntime, RuntimeOptions(opts))
	if err != nil {
		return HermesStatus{}, err
	}
	return hermesStatusFromRuntime(status), nil
}

func UninstallHermes(opts HermesOptions) (HermesStatus, error) {
	status, err := uninstallRuntimeHooks(hermesExistingRuntime, RuntimeOptions(opts))
	if err != nil {
		return HermesStatus{}, err
	}
	out := hermesStatusFromRuntime(status)
	var removed, failed []string
	for _, path := range hermesEarlierConfigPaths(opts.Level, status.ConfigPath) {
		updated, err := removeHermesEndpointHooks(path)
		if err != nil {
			// The file is not one this uninstall was asked about and may be the user's own,
			// so a parse failure there is reported rather than failing the uninstall.
			failed = append(failed, fmt.Sprintf("%s (%v)", path, err))
		} else if updated {
			removed = append(removed, path)
		}
	}
	if len(removed) > 0 {
		out.Message = fmt.Sprintf("%s; removed Beacon hooks left by an earlier install from %s", out.Message, strings.Join(removed, ", "))
	}
	if len(failed) > 0 {
		out.Message = fmt.Sprintf("%s; could not check for Beacon hooks left by an earlier install in %s", out.Message, strings.Join(failed, ", "))
		out.StaleConfigPaths = hermesStaleConfigPaths(opts.Level, status.ConfigPath)
	}
	return out, nil
}

// HermesHookStatus reports the hooks in the config.yaml Hermes reads, or in the base directory's when
// the active profile is unusable, with the profile problem appended to the message.
func HermesHookStatus(opts HermesOptions) HermesStatus {
	status := hermesStatusFromRuntime(runtimeHookStatus(hermesExistingRuntime, RuntimeOptions(opts)))
	if _, err := hermesConfigPath(opts.Level); errors.Is(err, hermessession.ErrUnusableProfile) && status.ConfigPath != "" {
		status.Message = fmt.Sprintf("%s; %v", status.Message, err)
	}
	if stale := hermesStaleConfigPaths(opts.Level, status.ConfigPath); len(stale) > 0 {
		status.StaleConfigPaths = stale
		status.Message = fmt.Sprintf("%s; Beacon hooks from an earlier install remain in %s, which Hermes does not read now; `beacon endpoint hooks uninstall --harness hermes` removes them", status.Message, strings.Join(stale, ", "))
	}
	return status
}

// hermesEarlierConfigPaths are the config.yaml files other than current that an earlier Beacon
// install may have written: ~/.hermes/config.yaml, where every version before HERMES_HOME and
// profile support wrote on every platform; the native root's, where an install made before
// HERMES_HOME was set wrote; and the base directory's, where one made before `hermes profile use`
// wrote. Hermes reads each of them again once HERMES_HOME or the active profile changes back.
func hermesEarlierConfigPaths(level Level, current string) []string {
	if current == "" || (level != "" && level != LevelUser) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	seen := map[string]bool{filepath.Clean(current): true}
	var paths []string
	for _, dir := range []string{filepath.Join(home, ".hermes"), hermessession.NativeRoot(home), hermessession.BaseDir(home)} {
		if dir == "" {
			continue
		}
		path := filepath.Clean(filepath.Join(dir, "config.yaml"))
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths
}

// hermesStaleConfigPaths are the hermesEarlierConfigPaths that still hold Beacon hooks.
func hermesStaleConfigPaths(level Level, current string) []string {
	var stale []string
	for _, path := range hermesEarlierConfigPaths(level, current) {
		if isHermesInstalledAt(path) {
			stale = append(stale, path)
		}
	}
	return stale
}

func IsHermesInstalled(opts HermesOptions) bool {
	return isRuntimeInstalled(hermesExistingRuntime, RuntimeOptions(opts))
}

func hermesStatusFromRuntime(status runtimeStatus) HermesStatus {
	return HermesStatus{
		Installed:  status.Installed,
		BinaryPath: status.BinaryPath,
		ConfigPath: status.ConfigPath,
		Message:    status.Message,
	}
}

func installHermesConfig(path, binaryPath, logPath, configPath string) error {
	config, err := readHermesConfig(path)
	if err != nil {
		return err
	}
	prefix := endpointCommandPrefix("hermes", binaryPath, logPath, configPath)
	endpointHooks := map[string]hermesHookRef{
		"on_session_start":       {Command: prefix + " session-start"},
		"pre_llm_call":           {Command: prefix + " prompt-submit", Timeout: 30},
		"pre_tool_call":          {Matcher: ".*", Command: prefix + " pre-tool", Timeout: 10},
		"post_tool_call":         {Matcher: ".*", Command: prefix + " post-tool", Timeout: 10},
		"pre_approval_request":   {Command: prefix + " permission-request", Timeout: 10},
		"post_approval_response": {Command: prefix + " permission-request", Timeout: 10},
		"subagent_stop":          {Command: prefix + " subagent-stop", Timeout: 10},
		"on_session_end":         {Command: prefix + " session-end"},
		"on_session_finalize":    {Command: prefix + " session-end"},
	}
	for eventName, ref := range endpointHooks {
		config.hooks[eventName] = mergeHermesEndpointHook(config.hooks[eventName], ref)
	}
	return writeHermesConfig(path, config)
}

// Hermes used to need its own builder, and now it does not.
//
// It prefixed the command with `env`, which is what an inline `VAR=value cmd` prefix requires when the
// command is executed without a shell to interpret it. Passing the settings as flags removes the need
// for a prefix at all, so there is nothing left for `env` to set and the special case collapses into
// the shared builder.
//
// One pre-existing question this does not answer: if Hermes really does invoke without a shell, the
// quoting around each path is literal there rather than syntax. That was equally true of the old form,
// and Hermes is not a runtime this repository can exercise, so it stays as it was rather than being
// changed on a guess.

func readHermesConfig(path string) (hermesConfig, error) {
	config := hermesConfig{
		values: map[string]interface{}{},
		hooks:  map[string][]hermesHookRef{},
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 0 {
			if err := yaml.Unmarshal(data, &config.values); err != nil {
				return hermesConfig{}, err
			}
		}
	} else if !os.IsNotExist(err) {
		return hermesConfig{}, err
	}
	if config.values == nil {
		config.values = map[string]interface{}{}
	}
	if rawHooks, ok := config.values["hooks"]; ok && rawHooks != nil {
		data, err := yaml.Marshal(rawHooks)
		if err != nil {
			return hermesConfig{}, err
		}
		if err := yaml.Unmarshal(data, &config.hooks); err != nil {
			return hermesConfig{}, err
		}
	}
	if config.hooks == nil {
		config.hooks = map[string][]hermesHookRef{}
	}
	return config, nil
}

func writeHermesConfig(path string, config hermesConfig) error {
	out := make(map[string]interface{}, len(config.values)+1)
	for key, value := range config.values {
		if key != "hooks" {
			out[key] = value
		}
	}
	if len(config.hooks) > 0 {
		out["hooks"] = config.hooks
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func mergeHermesEndpointHook(existing []hermesHookRef, ref hermesHookRef) []hermesHookRef {
	out := make([]hermesHookRef, 0, len(existing)+1)
	for _, item := range existing {
		if !isEndpointHookCommand(item.Command, "hermes") {
			out = append(out, item)
		}
	}
	return append(out, ref)
}

func removeHermesEndpointHooks(path string) (bool, error) {
	config, err := readHermesConfig(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	changed := false
	for eventName, refs := range config.hooks {
		filtered := refs[:0]
		for _, ref := range refs {
			if isEndpointHookCommand(ref.Command, "hermes") {
				changed = true
				continue
			}
			filtered = append(filtered, ref)
		}
		if len(filtered) == 0 {
			delete(config.hooks, eventName)
		} else {
			config.hooks[eventName] = filtered
		}
	}
	if !changed {
		return false, nil
	}
	if len(config.hooks) == 0 {
		delete(config.values, "hooks")
	}
	if len(config.values) == 0 && len(config.hooks) == 0 {
		return true, os.Remove(path)
	}
	return true, writeHermesConfig(path, config)
}

func isHermesInstalledAt(path string) bool {
	config, err := readHermesConfig(path)
	if err != nil {
		return false
	}
	for _, refs := range config.hooks {
		for _, ref := range refs {
			if isEndpointHookCommand(ref.Command, "hermes") {
				return true
			}
		}
	}
	return false
}

// hermesConfigPath is the config.yaml a plain `hermes` reads, where install writes.
func hermesConfigPath(level Level) (string, error) {
	return hermesUserConfigPath(level, hermessession.HomeDir)
}

// hermesExistingConfigPath is hermesConfigPath, except that an unusable active profile resolves to
// the base directory's config.yaml instead of an error.
func hermesExistingConfigPath(level Level) (string, error) {
	return hermesUserConfigPath(level, func(home string) (string, error) {
		dir, err := hermessession.FallbackHomeDir(home)
		if errors.Is(err, hermessession.ErrUnusableProfile) {
			return dir, nil
		}
		return dir, err
	})
}

func hermesUserConfigPath(level Level, homeDir func(string) (string, error)) (string, error) {
	switch level {
	case "", LevelUser:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir, err := homeDir(home)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "config.yaml"), nil
	case LevelProject:
		return "", fmt.Errorf("Hermes Agent endpoint hooks support user-level config only")
	default:
		return "", fmt.Errorf("unknown hook level %q", level)
	}
}
