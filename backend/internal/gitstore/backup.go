package gitstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Repository backups (D-79). The nightly backup (toft-backup.sh) runs
// `toft-git backup` in paas-git right after it dumps the panel's database,
// so the repositories are never behind it, and backs up BundlesDir instead
// of the repositories' own files, which a push during the copy could leave
// inconsistent. `toft-git restore` (install.sh --restore) rebuilds the
// repositories from them.

// BundlesDir is where the bundles live, beside @hashed on the volume.
const BundlesDir = "@bundles"

// bundleMeta is <id>.json beside <id>.bundle: what HEAD named and a digest
// of every ref when the bundle was written, so an unchanged repository
// isn't bundled again (restic then keeps the same bytes for free).
type bundleMeta struct {
	Head      string    `json:"head"`
	Refs      string    `json:"refs"`
	Empty     bool      `json:"empty"`
	BundledAt time.Time `json:"bundled_at"`
}

// BackupReport is what `toft-git backup` did.
type BackupReport struct {
	Repositories int            `json:"repositories"`
	Bundled      int            `json:"bundled"`
	Unchanged    int            `json:"unchanged"`
	Removed      int            `json:"removed"`
	Failed       []BundleFailed `json:"failed"`
}

// BundleFailed is a repository whose bundle couldn't be written; its last
// good one is kept.
type BundleFailed struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// Backup writes a verified bundle of every repository under root whose refs
// changed since its last one, and removes the bundles of repositories that
// are gone.
func Backup(ctx context.Context, root string) (BackupReport, error) {
	rep := BackupReport{Failed: []BundleFailed{}}
	dir := filepath.Join(root, BundlesDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return rep, fmt.Errorf("bundles: %w", err)
	}
	repos, err := filepath.Glob(filepath.Join(root, "@hashed", "*", "*", "*.git"))
	if err != nil {
		return rep, err
	}
	present := map[string]bool{}
	for _, repo := range repos {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		id := strings.TrimSuffix(filepath.Base(repo), ".git")
		if p, err := RepoPath(root, id); err != nil || p != repo {
			continue
		}
		present[id] = true
		rep.Repositories++
		bundled, err := bundleOne(ctx, repo, dir, id)
		switch {
		case err != nil:
			rep.Failed = append(rep.Failed, BundleFailed{ID: id, Error: err.Error()})
		case bundled:
			rep.Bundled++
		default:
			rep.Unchanged++
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return rep, err
	}
	for _, e := range entries {
		id, _, _ := strings.Cut(e.Name(), ".")
		if !present[id] {
			if os.Remove(filepath.Join(dir, e.Name())) == nil && strings.HasSuffix(e.Name(), ".json") {
				rep.Removed++
			}
		}
	}
	return rep, nil
}

// bundleOne bundles repo unless its refs are what its last bundle holds;
// the new bundle replaces the old only once git has verified it.
func bundleOne(ctx context.Context, repo, dir, id string) (bool, error) {
	refs, err := out(ctx, repo, nil, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return false, err
	}
	head, err := defaultBranch(ctx, repo)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(append(refs, []byte("\nHEAD "+head)...))
	meta := bundleMeta{Head: head, Refs: hex.EncodeToString(sum[:]), Empty: len(strings.TrimSpace(string(refs))) == 0}
	metaPath := filepath.Join(dir, id+".json")
	bundlePath := filepath.Join(dir, id+".bundle")
	if b, err := os.ReadFile(metaPath); err == nil {
		var old bundleMeta
		_, bundleErr := os.Stat(bundlePath)
		if json.Unmarshal(b, &old) == nil && old.Refs == meta.Refs && (old.Empty || bundleErr == nil) {
			return false, nil
		}
	}
	if meta.Empty {
		// git refuses an empty bundle; the meta alone restores it.
		_ = os.Remove(bundlePath)
	} else {
		tmp := bundlePath + ".tmp"
		defer os.Remove(tmp)
		if _, err := out(ctx, repo, nil, "bundle", "create", "--quiet", tmp, "--all"); err != nil {
			return false, err
		}
		if _, err := out(ctx, repo, nil, "bundle", "verify", "--quiet", tmp); err != nil {
			return false, err
		}
		if err := os.Rename(tmp, bundlePath); err != nil {
			return false, err
		}
	}
	meta.BundledAt = time.Now().UTC()
	b, _ := json.Marshal(meta)
	tmp := metaPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, metaPath)
}

// RestoreReport is what `toft-git restore` did.
type RestoreReport struct {
	Restored int            `json:"restored"`
	Kept     int            `json:"kept"`
	Failed   []BundleFailed `json:"failed"`
}

// Restore rebuilds, from their bundles, the repositories under root that
// are missing or have no refs (a fresh server's, made empty at the
// backend's start); one with refs is left as it is.
func Restore(ctx context.Context, root string) (RestoreReport, error) {
	rep := RestoreReport{Failed: []BundleFailed{}}
	metas, err := filepath.Glob(filepath.Join(root, BundlesDir, "*.json"))
	if err != nil {
		return rep, err
	}
	e := NewExecutor(root)
	for _, m := range metas {
		id := strings.TrimSuffix(filepath.Base(m), ".json")
		repo, err := RepoPath(root, id)
		if err != nil {
			continue
		}
		if err := restoreOne(ctx, e, repo, m, strings.TrimSuffix(m, ".json")+".bundle"); errors.Is(err, errKept) {
			rep.Kept++
		} else if err != nil {
			rep.Failed = append(rep.Failed, BundleFailed{ID: id, Error: err.Error()})
		} else {
			rep.Restored++
		}
	}
	return rep, nil
}

var errKept = errors.New("the repository has refs: kept")

func restoreOne(ctx context.Context, e *Executor, repo, metaPath, bundlePath string) error {
	if err := e.init(ctx, repo); err != nil {
		return err
	}
	if refs, err := out(ctx, repo, nil, "for-each-ref", "--count=1", "--format=%(refname)"); err != nil {
		return err
	} else if len(strings.TrimSpace(string(refs))) > 0 {
		return errKept
	}
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return err
	}
	var meta bundleMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(metaPath), err)
	}
	if meta.Empty {
		return nil
	}
	if _, err := out(ctx, repo, nil, "-c", "transfer.fsckObjects=true", "fetch", "--quiet", "--no-write-fetch-head",
		"--end-of-options", bundlePath, "+refs/*:refs/*"); err != nil {
		return err
	}
	if meta.Head != "" {
		if err := setHead(ctx, repo, meta.Head); err == nil {
			return nil
		}
	}
	return fixHead(ctx, repo)
}
