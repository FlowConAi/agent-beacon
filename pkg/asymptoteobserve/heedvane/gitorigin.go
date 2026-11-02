// Copyright (c) 2026 FlowConAI UG (haftungsbeschränkt). All rights reserved.
package heedvane

import (
	"bufio"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// maxGitConfigBytes caps the read of a repository's config so a corrupt or hostile file cannot
// balloon a hook or a collector pass.
const maxGitConfigBytes = 1 << 20

// gitOriginFreshFor bounds how long a collector or watcher keeps an answer, so a folder that gains or
// changes its origin is named correctly without a restart. Long enough that a busy pass reads the
// config once, not once per event.
const gitOriginFreshFor = time.Minute

type cachedOrigin struct {
	origin string
	at     time.Time
}

var gitOriginCache sync.Map // physical directory -> cachedOrigin ("" when none)

// GitOrigin names the repository a working directory belongs to: the sanitized URL of its "origin"
// remote, or "" when the folder is no repository, has no origin, or git metadata is disabled
// (BEACON_DISABLE_GIT_METADATA). The server cannot turn a path on someone's machine into a
// repository, so the machine stamps it on every event that carries a working directory.
//
// It reads local files only: it walks up from cwd to the nearest ".git", follows a linked worktree's
// pointer file to the common git dir, and reads the config there. It never runs git or touches the
// network. Results are cached per directory for gitOriginFreshFor.
//
// shortcut: reads only [remote "origin"] in the repository's own config; include files and
// url.<base>.insteadOf rewrites are ignored, upgrade if a customer's origin hides behind either.
func GitOrigin(cwd string) string {
	if strings.TrimSpace(cwd) == "" || gitMetadataDisabled() {
		return ""
	}
	dir := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		dir = resolved
	}
	if cached, ok := gitOriginCache.Load(dir); ok && time.Since(cached.(cachedOrigin).at) < gitOriginFreshFor {
		return cached.(cachedOrigin).origin
	}
	origin := SanitizeRemoteURL(readOriginURL(commonGitDir(dir)))
	gitOriginCache.Store(dir, cachedOrigin{origin: origin, at: time.Now()})
	return origin
}

func gitMetadataDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BEACON_DISABLE_GIT_METADATA"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// commonGitDir walks up from dir to the nearest checkout and returns the git dir that holds its
// config: ".git" itself, or for a linked worktree the common dir its gitdir points at.
func commonGitDir(dir string) string {
	for {
		dotGit := filepath.Join(dir, ".git")
		if info, err := os.Stat(dotGit); err == nil {
			if info.IsDir() {
				return dotGit
			}
			if gitdir := pointerTarget(dotGit); gitdir != "" {
				return commonDirOf(gitdir)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// pointerTarget reads a ".git" file ("gitdir: <path>") and returns the absolute gitdir.
func pointerTarget(dotGitFile string) string {
	target, ok := strings.CutPrefix(firstLine(readCapped(dotGitFile, 4096)), "gitdir:")
	if !ok {
		return ""
	}
	return absoluteFrom(filepath.Dir(dotGitFile), strings.TrimSpace(target))
}

// commonDirOf follows a linked worktree's "commondir" file ("../.." relative to its gitdir).
func commonDirOf(gitdir string) string {
	if common := firstLine(readCapped(filepath.Join(gitdir, "commondir"), 4096)); common != "" {
		return absoluteFrom(gitdir, strings.TrimSpace(common))
	}
	return gitdir
}

func absoluteFrom(base, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}

// readOriginURL returns the url of [remote "origin"] in <gitDir>/config.
func readOriginURL(gitDir string) string {
	if gitDir == "" {
		return ""
	}
	inOrigin := false
	scanner := bufio.NewScanner(strings.NewReader(readCapped(filepath.Join(gitDir, "config"), maxGitConfigBytes)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = opensOriginSection(line)
			continue
		}
		if !inOrigin {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && strings.EqualFold(strings.TrimSpace(key), "url") {
			return configValue(value)
		}
	}
	return ""
}

// opensOriginSection reads a section header as Git does: the section name in any case, the subsection
// exactly, and a comment may follow the closing bracket.
func opensOriginSection(line string) bool {
	section, rest, quoted := strings.Cut(strings.TrimPrefix(line, "["), `"`)
	subsection, _, closed := strings.Cut(rest, `"`)
	return quoted && closed && strings.EqualFold(strings.TrimSpace(section), "remote") && subsection == "origin"
}

// configValue reads a value as Git does: a comment starts at an unquoted ";" or "#", and quotes are
// dropped.
//
// shortcut: backslash escapes and values continued on the next line are not read, upgrade if a remote
// URL ever needs them.
func configValue(raw string) string {
	var value strings.Builder
	quoted := false
	for _, char := range raw {
		switch {
		case char == '"':
			quoted = !quoted
		case !quoted && (char == ';' || char == '#'):
			return strings.TrimSpace(value.String())
		default:
			value.WriteRune(char)
		}
	}
	return strings.TrimSpace(value.String())
}

func readCapped(path string, limit int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return ""
	}
	return string(data)
}

func firstLine(content string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(content), "\n")
	return strings.TrimSpace(line)
}

// SanitizeRemoteURL removes credentials from a remote URL. An https remote can carry a token as
// its user info (https://x-access-token:ghp_...@github.com/org/repo), and the URL is about to be
// written to a log that may be forwarded. The user of an scp-style remote (git@host:org/repo) is a
// login, not a secret, but it is dropped too so every spelling of a remote reads the same.
func SanitizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			// Unparseable, so its credentials cannot be told apart: name no remote rather than risk keeping one.
			return ""
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	raw, _, _ = strings.Cut(raw, "?")
	// scp-like syntax: [user@]host:path. A local path has no colon before its first slash.
	if at := strings.Index(raw, "@"); at >= 0 {
		colon := strings.Index(raw, ":")
		slash := strings.Index(raw, "/")
		if colon > at && (slash < 0 || colon < slash) {
			return raw[at+1:]
		}
	}
	return raw
}
