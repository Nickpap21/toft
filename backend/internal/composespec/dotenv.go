package composespec

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
)

// ─── The .env file the platform writes for docker compose ────────────────────
//
// On a remote server the service's env vars reach docker compose through the
// project directory's .env file, which the platform writes (replacing any
// .env from the repository). The deploy policy interpolates the compose file
// with the env var map itself, so that file must parse back as exactly that
// map, and the files it checks must read the same .env as docker compose.

// envKeyPattern is a valid environment variable name (as envvar.ValidKey).
var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ServiceEnv returns the service's env vars that may be handed to docker
// compose, as its process environment or in the .env file the platform
// writes. It leaves out names the docker and compose CLIs read as their own
// settings (IsReservedCLIEnv: DOCKER_HOST, COMPOSE_*, PATH, ...) and names
// that are not valid variable names (they could add lines to the .env file).
// The deploy policy refuses both in enforce mode; this is the backstop for
// warn mode. dropped lists the names left out, sorted.
func ServiceEnv(env map[string]string) (kept map[string]string, dropped []string) {
	kept = make(map[string]string, len(env))
	for k, v := range env {
		if !envKeyPattern.MatchString(k) || IsReservedCLIEnv(k) {
			dropped = append(dropped, k)
			continue
		}
		kept[k] = v
	}
	sort.Strings(dropped)
	return kept, dropped
}

// DotEnvFile renders env vars (already filtered by ServiceEnv) as the .env
// file docker compose reads: one KEY="value" line per variable, sorted.
// Values are double-quoted with \, ", $ and newlines escaped, which compose's
// dotenv parser reads back literally: nothing is expanded, a trailing
// backslash cannot escape the closing quote, and a newline cannot start
// another variable. The rendered file is parsed back with that parser, and a
// file that does not read back as exactly env is an error, so the deploy
// fails instead of interpolating values the policy did not check.
func DotEnvFile(env map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		if !envKeyPattern.MatchString(k) {
			return nil, fmt.Errorf("env var name %q cannot be written to the compose .env file", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(`="`)
		v := env[k]
		for i := 0; i < len(v); i++ {
			switch c := v[i]; c {
			case '\\':
				b.WriteString(`\\`)
			case '"':
				b.WriteString(`\"`)
			case '$':
				b.WriteString(`\$`)
			case '\n':
				b.WriteString(`\n`)
			default:
				b.WriteByte(c)
			}
		}
		b.WriteString("\"\n")
	}
	content := []byte(b.String())

	// The parse error is not wrapped: it can quote a value, which may be a
	// secret, and it would reach the deploy log.
	got, err := dotenv.ParseWithLookup(bytes.NewReader(content), func(string) (string, bool) { return "", false })
	if err != nil {
		return nil, errors.New("the compose .env file cannot be read back as the service's env vars")
	}
	if len(got) != len(env) {
		return nil, errors.New("the compose .env file does not read back as the service's env vars")
	}
	for k, v := range env {
		if gv, ok := got[k]; !ok || gv != v {
			return nil, fmt.Errorf("env var %s cannot be written to the compose .env file exactly; change its value", k)
		}
	}
	return content, nil
}

// StageDotEnv writes the .env file a remote deploy uses into a local clone,
// before the clone is checked and uploaded: <root>/<directory of
// composeFile>/.env becomes DotEnvFile(ServiceEnv(env)), replacing whatever
// the repository had there (a symlink is removed, not followed). That is the
// compose project directory on the remote server, where the platform writes
// the same file again; compose reads it for the compose file and for every
// file included from that directory, so the policy check must read it too.
//
// The directory must resolve inside root. It is an error when it does not,
// or does not exist.
func StageDotEnv(root, composeFile string, env map[string]string) error {
	kept, _ := ServiceEnv(env)
	content, err := DotEnvFile(kept)
	if err != nil {
		return err
	}
	rel := path.Dir(filepath.ToSlash(composeFile))
	if path.IsAbs(rel) || hasDotDot(rel) {
		return errors.New("the compose file path must be inside the repository")
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", withoutPath(err))
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return fmt.Errorf("the compose file's directory %s: %w", rel, withoutPath(err))
	}
	if !Within(realRoot, dir) {
		return fmt.Errorf("the compose file's directory %s is outside the repository", rel)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("the compose file's directory %s is not a directory", rel)
	}
	p := filepath.Join(dir, ".env")
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replace %s/.env: %w", rel, withoutPath(err))
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write %s/.env: %w", rel, withoutPath(err))
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return fmt.Errorf("write %s/.env: %w", rel, withoutPath(err))
	}
	return f.Close()
}

// withoutPath drops the path from a filesystem error, so a deploy log never
// shows the server's temp directory names.
func withoutPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
