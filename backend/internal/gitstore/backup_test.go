package gitstore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// TestBackupAndRestore bundles repositories as the nightly backup does,
// only those whose refs changed, and rebuilds them on a fresh volume from
// the bundles alone: every ref, refs/pull included, and the default branch.
func TestBackupAndRestore(t *testing.T) {
	if _, err := CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", MinVersion[0], MinVersion[1], err)
	}
	ctx := context.Background()
	root := t.TempDir()
	e := NewExecutor(root)
	const shop, empty = "6a1f0e2d-3c4b-4d5e-8f60-718293a4b5c6", "9b8c7d6e-5f4a-4b3c-9d2e-1f0a9b8c7d6e"
	shopDir, _ := RepoPath(root, shop)
	emptyDir, _ := RepoPath(root, empty)
	for _, d := range []string{shopDir, emptyDir} {
		if err := e.init(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	w := t.TempDir()
	gitIn(t, w, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(w, "a.txt"), []byte("one\n"), 0o644)
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "First")
	gitIn(t, w, "tag", "-a", "-m", "v1", "v1.0.0")
	gitIn(t, w, "checkout", "-qb", "dev")
	os.WriteFile(filepath.Join(w, "b.txt"), []byte("two\n"), 0o644)
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "Second")
	gitIn(t, w, "push", "-q", shopDir, "main", "dev", "v1.0.0")
	gitIn(t, shopDir, "update-ref", "refs/pull/1/head", "refs/heads/dev")
	gitIn(t, shopDir, "symbolic-ref", "HEAD", "refs/heads/dev")

	rep, err := Backup(ctx, root)
	if err != nil || rep.Repositories != 2 || rep.Bundled != 2 || len(rep.Failed) != 0 {
		t.Fatalf("first backup = %+v, %v", rep, err)
	}
	if rep, _ = Backup(ctx, root); rep.Bundled != 0 || rep.Unchanged != 2 {
		t.Fatalf("an unchanged night bundled again: %+v", rep)
	}
	gitIn(t, w, "commit", "-q", "--allow-empty", "-m", "Third")
	gitIn(t, w, "push", "-q", shopDir, "dev")
	if rep, _ = Backup(ctx, root); rep.Bundled != 1 || rep.Unchanged != 1 {
		t.Fatalf("after a push: %+v", rep)
	}
	want := gitIn(t, shopDir, "for-each-ref", "--format=%(objectname) %(refname)")

	// A fresh volume with only the bundles: everything comes back.
	fresh := t.TempDir()
	if out, err := exec.Command("cp", "-R", filepath.Join(root, BundlesDir), filepath.Join(fresh, BundlesDir)).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v %s", err, out)
	}
	// The backend made this one empty at its start, before the restore.
	freshShop, _ := RepoPath(fresh, shop)
	if err := NewExecutor(fresh).init(ctx, freshShop); err != nil {
		t.Fatal(err)
	}
	res, err := Restore(ctx, fresh)
	if err != nil || res.Restored != 2 || len(res.Failed) != 0 {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	if got := gitIn(t, freshShop, "for-each-ref", "--format=%(objectname) %(refname)"); got != want {
		t.Fatalf("refs after the restore:\n%s\nwant\n%s", got, want)
	}
	if h := gitIn(t, freshShop, "symbolic-ref", "HEAD"); h != "refs/heads/dev" {
		t.Errorf("HEAD = %s", h)
	}
	gitIn(t, freshShop, "fsck", "--no-progress")
	freshEmpty, _ := RepoPath(fresh, empty)
	if _, err := os.Stat(filepath.Join(freshEmpty, "HEAD")); err != nil {
		t.Errorf("the empty repository wasn't made: %v", err)
	}
	if res, _ = Restore(ctx, fresh); res.Kept != 1 {
		t.Errorf("a second restore touched a repository with refs: %+v", res)
	}

	// A repository deleted since: its bundle goes at the next backup.
	os.RemoveAll(shopDir)
	if rep, _ = Backup(ctx, root); rep.Removed != 1 || rep.Repositories != 1 {
		t.Fatalf("after a delete: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(root, BundlesDir, shop+".bundle")); !os.IsNotExist(err) {
		t.Errorf("the deleted repository's bundle is still there: %v", err)
	}
}
