// Package gitstore is the only code that runs git for Toft's own
// repositories (D-20, D-71). The executor runs in its own container,
// paas-git, as an unprivileged user with no network, no secrets and only the
// repositories' volume; the backend reaches it through Client over a Unix
// socket. git parses untrusted content here, so every call has a fixed
// environment, a path built only from the repository's UUID, and its policy
// on the command line rather than in files a repository could carry.
package gitstore

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Where the paas-git container keeps the repositories and listens; the
// backend mounts only the socket's directory (docker-compose.git.yml).
const (
	Root   = "/var/lib/toft/git"
	Socket = "/run/toft-git/git.sock"
)

// MinVersion is the oldest git the executor runs on (D-71).
var MinVersion = [2]int{2, 54}

// Services are the two git programs a client may reach.
var Services = map[string]bool{"upload-pack": true, "receive-pack": true}

// MaxPush is receive.maxInputSize: one push carries at most this many bytes.
const MaxPush = 2 << 30

// policy is every repository's configuration, given on each call so nothing
// a repository holds can change it. System and global files are off (env).
var policy = []string{
	// No hooks until Toft's own exist (session 3): never a repository's.
	"-c", "core.hooksPath=/dev/null",
	// An acknowledged push survives a power cut.
	"-c", "core.fsync=committed",
	"-c", "receive.fsckObjects=true",
	"-c", "receive.maxInputSize=" + strconv.Itoa(MaxPush),
	// Names the server owns: a push that creates, moves or deletes one is
	// refused (git rejects updates to hidden refs).
	"-c", "receive.hideRefs=refs/pull",
	"-c", "receive.hideRefs=refs/keep-around",
	// Partial clone, so a large repository can be cloned without its blobs.
	"-c", "uploadpack.allowFilter=true",
}

// protocolValue is what GIT_PROTOCOL may hold ("version=2", and keys git
// adds later); anything else is dropped.
var protocolValue = regexp.MustCompile(`^[A-Za-z0-9=:._-]{1,64}$`)

// RepoPath is where repository id lives under root, or an error when id is
// not a UUID: no client-supplied string ever reaches a path.
func RepoPath(root, id string) (string, error) {
	u, err := uuid.Parse(id)
	if err != nil || u.String() != id {
		return "", fmt.Errorf("repository id %q is not a UUID", id)
	}
	return filepath.Join(root, "@hashed", id[0:2], id[2:4], id+".git"), nil
}

// command is git with the fixed environment and the policy, and protocol
// passed as GIT_PROTOCOL when it is well formed.
func command(ctx context.Context, protocol string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, policy...), args...)...)
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		// A path a client names is a path, never a glob or :(magic).
		"GIT_LITERAL_PATHSPECS=1",
	}
	if protocolValue.MatchString(protocol) {
		cmd.Env = append(cmd.Env, "GIT_PROTOCOL="+protocol)
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// IsV2 says whether a GIT_PROTOCOL value asks for protocol version 2, which
// changes what the HTTP ref advertisement starts with.
func IsV2(protocol string) bool {
	for _, kv := range strings.Split(protocol, ":") {
		if kv == "version=2" {
			return true
		}
	}
	return false
}

// CheckVersion refuses a git older than MinVersion.
func CheckVersion(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "version").Output()
	if err != nil {
		return "", fmt.Errorf("git version: %w", err)
	}
	v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(out)), "git version "))
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v, fmt.Errorf("unreadable git version %q", v)
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(strings.TrimRightFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	if err1 != nil || err2 != nil {
		return v, fmt.Errorf("unreadable git version %q", v)
	}
	if major < MinVersion[0] || (major == MinVersion[0] && minor < MinVersion[1]) {
		return v, fmt.Errorf("git %s is older than %d.%d", v, MinVersion[0], MinVersion[1])
	}
	return v, nil
}
