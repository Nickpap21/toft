package composespec

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
)

// Within reports whether p is root or inside it, lexically (after
// filepath.Clean). Both should be absolute.
func Within(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	rel, err := filepath.Rel(root, p)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// hasDotDot reports whether p has a ".." path segment.
func hasDotDot(p string) bool {
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return true
		}
	}
	return false
}

// relField renders p relative to root for messages, so a violation never
// shows the server's temp directory names.
func relField(root, p string) string {
	if Within(root, p) {
		if rel, err := filepath.Rel(root, p); err == nil {
			return "./" + filepath.ToSlash(rel)
		}
	}
	return p
}

// ScanRepo checks a cloned repository before its compose file is loaded:
//
//   - every symlink must resolve inside root. Docker follows symlinks when it
//     bind-mounts a path and compose follows them when it reads a file, so a
//     link to / or to the panel's .env would escape every other check;
//   - no .env file may read one of the platform's own variables
//     (IsPlatformEnv). compose reads <dir>/.env for every included file's
//     interpolation, expanding ${VAR} with its own environment, which Load
//     cannot see; every .env file is read, whatever its size, because which
//     files are included depends on interpolation.
//
// include env_file and project_directory are refused by Load itself, while
// each file is interpolated, so that check does not depend on this scan.
// The .git directory is skipped.
func ScanRepo(root string) ([]Violation, error) {
	root = filepath.Clean(root)
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	var vs []Violation
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel := relField(root, p)
		if d.Type()&fs.ModeSymlink != 0 {
			if !symlinkInside(root, realRoot, p) {
				vs = append(vs, Violation{
					Rule:    RuleExternalRef,
					Field:   rel,
					Message: "symlink points outside the repository",
				})
				return nil
			}
		} else if !d.Type().IsRegular() {
			return nil
		}
		if d.Name() != ".env" {
			return nil
		}
		info, err := os.Stat(p) // follows a symlink that stays inside root
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		vs = append(vs, dotEnvPlatformRefs(content, rel)...)
		return nil
	})
	if err != nil {
		return vs, fmt.Errorf("scan repository: %w", err)
	}
	return vs, nil
}

// dotEnvPlatformRefs returns a violation for every platform variable a .env
// file reads, in an expansion (${HOME}) or as an inherited key (a bare
// HOME line). A file compose cannot parse is left alone: compose would fail
// on it too (and the root .env is parsed again by Load).
func dotEnvPlatformRefs(content []byte, field string) []Violation {
	refs := &varRecorder{names: map[string]bool{}}
	_, _ = dotenv.ParseWithLookup(bytes.NewReader(content), dotenv.LookupFn(refs.wrap(func(string) (string, bool) { return "", false })))
	return refs.platformViolations(field, field)
}

// symlinkInside reports whether the symlink at p resolves inside root, both
// lexically (one hop) and after full resolution when the target exists.
func symlinkInside(root, realRoot, p string) bool {
	target, err := os.Readlink(p)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(p), target)
	}
	if !Within(root, target) && !Within(realRoot, target) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		// Dangling, or a loop: the lexical check above already holds, and a
		// later file created at the target would be inside root.
		return !errors.Is(err, fs.ErrPermission)
	}
	return Within(realRoot, resolved) || Within(root, resolved)
}
