package gitstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A deploy's source (D-77): the tree of one commit as a tar, which the
// backend streams to the server that builds it. The build server never sees
// a URL, a token or .git.

// archiveAttributes makes git archive ignore a repository's export-ignore
// and export-subst: a build sees what a clone sees, and export-subst's
// format expansion never runs. $GIT_DIR/info/attributes outranks the
// .gitattributes files of the tree being archived, and only the executor
// writes it.
const archiveAttributes = "* -export-ignore -export-subst\n"

// handleArchive serves GET /v1/repos/{id}/archive?ref=…: ref (a branch, a
// tag or a SHA; the default branch when empty) is resolved once to a full
// SHA, which the Toft-Commit header names with its subject (base64) in
// Toft-Subject; the body is git archive's tar of that commit, and the
// trailers say how git ended, as a service call's do.
func (e *Executor) handleArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dir, err := RepoPath(e.root, id)
	if err != nil {
		writeErr(w, browseErr(http.StatusBadRequest, "bad_repository", err.Error()))
		return
	}
	if err := e.init(r.Context(), dir); err != nil {
		writeErr(w, err)
		return
	}
	release, err := e.acquire(r.Context(), id, "", "")
	if err != nil {
		writeErr(w, browseErr(http.StatusServiceUnavailable, "git_busy", err.Error()))
		return
	}
	defer release()

	rctx, cancel := context.WithTimeout(r.Context(), browseTimeout)
	ref, err := resolve(rctx, dir, r.URL.Query().Get("ref"))
	var c *Commit
	if err == nil {
		c, err = commitAt(rctx, dir, ref.SHA)
	}
	if err == nil {
		err = writeArchiveAttributes(dir)
	}
	cancel()
	if err != nil {
		writeErr(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), e.maxTime)
	defer cancel()
	idle := time.AfterFunc(e.idle, cancel)
	defer idle.Stop()

	stderr := &capped{max: 4096}
	cmd := command(ctx, "", "--git-dir="+dir, "archive", "--format=tar", "--end-of-options", ref.SHA)
	cmd.Stderr = stderr
	w.Header().Set("Trailer", "Toft-Git-Exit, Toft-Git-Stderr")
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Toft-Commit", ref.SHA)
	w.Header().Set("Toft-Subject", base64.StdEncoding.EncodeToString([]byte(c.Subject)))
	w.WriteHeader(http.StatusOK)
	cmd.Stdout = &flushWriter{w: w, rc: http.NewResponseController(w), moved: func() { idle.Reset(e.idle) }}
	code := 0
	if err := cmd.Run(); err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			code = exit.ExitCode()
		}
		if ctx.Err() != nil {
			stderr.Write([]byte("\ntoft: the archive ran too long or stopped moving"))
		}
	}
	w.Header().Set("Toft-Git-Exit", strconv.Itoa(code))
	w.Header().Set("Toft-Git-Stderr", base64.StdEncoding.EncodeToString(stderr.Bytes()))
}

// writeArchiveAttributes makes dir's info/attributes archiveAttributes.
func writeArchiveAttributes(dir string) error {
	p := filepath.Join(dir, "info", "attributes")
	if b, err := os.ReadFile(p); err == nil && string(b) == archiveAttributes {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(archiveAttributes), 0o640)
}

// Archive is a commit's tree as a tar (Client.Archive). Read it to the end,
// then Close it; Err says whether git finished the tar.
type Archive struct {
	io.ReadCloser
	// SHA and Subject are the commit the ref resolved to.
	SHA, Subject string
	resp         *http.Response
}

// Err is nil when git wrote the whole tar; call it after reading to EOF,
// when the trailers have arrived.
func (a *Archive) Err() error {
	code, err := strconv.Atoi(a.resp.Trailer.Get("Toft-Git-Exit"))
	if err == nil && code == 0 {
		return nil
	}
	msg, _ := base64.StdEncoding.DecodeString(a.resp.Trailer.Get("Toft-Git-Stderr"))
	return fmt.Errorf("git archive: exit %s: %s", a.resp.Trailer.Get("Toft-Git-Exit"), strings.TrimSpace(string(msg)))
}

// Archive starts the tar of the commit ref names in repository id (the
// default branch when ref is ""). A ref the executor refuses is a
// *BrowseError (bad_ref, ref_not_found, empty); a full executor is ErrBusy.
func (c *Client) Archive(ctx context.Context, id, ref string) (*Archive, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/repos/"+url.PathEscape(id)+"/archive?"+url.Values{"ref": {ref}}.Encode(), "", nil)
	if err != nil {
		return nil, err
	}
	if err := browseStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	subject, _ := base64.StdEncoding.DecodeString(resp.Header.Get("Toft-Subject"))
	sha := resp.Header.Get("Toft-Commit")
	if !isHex(sha, 40, 64) {
		resp.Body.Close()
		return nil, fmt.Errorf("git executor: archive named no commit")
	}
	return &Archive{ReadCloser: resp.Body, SHA: sha, Subject: string(bytes.TrimSpace(subject)), resp: resp}, nil
}
