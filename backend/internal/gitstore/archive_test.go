package gitstore

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/testenv"
)

// TestArchive streams a commit's tree through the executor's socket: the
// ref resolves once, export-ignore and export-subst are ignored (a build sees
// what a clone sees), server-owned names are never resolved, and the
// trailers say git finished.
func TestArchive(t *testing.T) {
	if _, err := CheckVersion(context.Background()); err != nil {
		testenv.Skip(t, "needs git %d.%d or newer: %v", MinVersion[0], MinVersion[1], err)
	}
	root := t.TempDir()
	const id = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	dir, _ := RepoPath(root, id)
	e := NewExecutor(root)
	if err := e.init(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	w := t.TempDir()
	write := func(name, body string) {
		os.MkdirAll(filepath.Join(w, filepath.Dir(name)), 0o755)
		if err := os.WriteFile(filepath.Join(w, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, w, "init", "-q", "-b", "main")
	write(".gitattributes", "tests/ export-ignore\nsecret.txt export-ignore\nversion.txt export-subst\n")
	write("tests/a_test.go", "package a\n")
	write("secret.txt", "kept\n")
	write("version.txt", "$Format:%H$\n")
	write("services/api/Dockerfile", "FROM scratch\n")
	gitIn(t, w, "add", ".")
	gitIn(t, w, "commit", "-qm", "Add the api")
	sha := gitIn(t, w, "rev-parse", "HEAD")
	gitIn(t, w, "push", "-q", dir, "main")
	gitIn(t, dir, "update-ref", "refs/pull/1/head", sha)

	sockDir, err := os.MkdirTemp("", "ta")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e.Handler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	c := NewClient(sock)
	ctx := context.Background()

	for _, ref := range []string{"main", "", sha, sha[:10]} {
		a, err := c.Archive(ctx, id, ref)
		if err != nil {
			t.Fatalf("Archive(%q): %v", ref, err)
		}
		files := map[string]string{}
		tr := tar.NewReader(a)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if h.Typeflag == tar.TypeReg {
				b, _ := io.ReadAll(tr)
				files[h.Name] = string(b)
			}
		}
		io.Copy(io.Discard, a)
		a.Close()
		if err := a.Err(); err != nil {
			t.Errorf("Archive(%q).Err() = %v", ref, err)
		}
		if a.SHA != sha || a.Subject != "Add the api" {
			t.Errorf("Archive(%q) = %s %q, want %s", ref, a.SHA, a.Subject, sha)
		}
		for name, want := range map[string]string{
			"tests/a_test.go": "package a\n", "secret.txt": "kept\n", "version.txt": "$Format:%H$\n",
			"services/api/Dockerfile": "FROM scratch\n",
		} {
			if files[name] != want {
				t.Errorf("Archive(%q): %s = %q, want %q (files %v)", ref, name, files[name], want, files)
			}
		}
	}

	var be *BrowseError
	for ref, code := range map[string]string{"refs/pull/1/head": "ref_not_found", "pull/1/head": "ref_not_found", "a..b": "bad_ref", "nope": "ref_not_found"} {
		if _, err := c.Archive(ctx, id, ref); !errors.As(err, &be) || be.Code != code {
			t.Errorf("Archive(%q) = %v, want %s", ref, err, code)
		}
	}
}
