package gitstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrBusy: the executor has no free slot; the client may try again.
var ErrBusy = errors.New("the git server is busy, try again in a moment")

// busyError is the executor's refusal in its own words: which limit is
// full, and what to do. errors.Is(err, ErrBusy) holds.
type busyError string

func (b busyError) Error() string      { return string(b) }
func (busyError) Is(target error) bool { return target == ErrBusy }

// ErrUnavailable: the executor isn't listening (its socket is missing or
// refuses the connection), as while the git container restarts.
var ErrUnavailable = errors.New("the git server is not answering")

// Client is the backend's side of the executor's socket.
type Client struct {
	http *http.Client
}

// NewClient talks to the executor listening on socket.
func NewClient(socket string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		// One connection per call: a streamed call holds its connection.
		DisableKeepAlives: true,
	}}}
}

// Result is how a git program ended: its exit code and the start of its
// stderr.
type Result struct {
	Exit   int
	Stderr string
	// Updated is what a push moved (receive-pack only).
	Updated []RefUpdate
}

func (c *Client) do(ctx context.Context, method, path, protocol string, body io.Reader, headers ...string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://git"+path, body)
	if err != nil {
		return nil, err
	}
	if protocol != "" {
		req.Header.Set("Git-Protocol", protocol)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := c.http.Do(req)
	var dial *net.OpError
	if errors.As(err, &dial) && dial.Op == "dial" {
		return nil, fmt.Errorf("git executor: %w: %w", ErrUnavailable, err)
	}
	if err != nil {
		return nil, fmt.Errorf("git executor: %w", err)
	}
	return resp, nil
}

func (c *Client) simple(ctx context.Context, method, path string) error {
	resp, err := c.do(ctx, method, path, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("git executor: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Health answers whether the executor answers.
func (c *Client) Health(ctx context.Context) error {
	return c.simple(ctx, http.MethodGet, "/v1/health")
}

// Init makes repository id, empty, unless it exists.
func (c *Client) Init(ctx context.Context, id string) error {
	return c.simple(ctx, http.MethodPost, "/v1/repos/"+url.PathEscape(id)+"/init")
}

// Delete removes repository id from disk.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.simple(ctx, http.MethodDelete, "/v1/repos/"+url.PathEscape(id))
}

// Serve runs service (upload-pack or receive-pack) on repository id in mode
// (advertise, stateless or stream) with protocol as GIT_PROTOCOL, feeding it
// stdin and copying what it writes to stdout as it comes. protect (a
// branch's name) refuses a push's updates to that branch; readOnly (the URL
// a pull mirror pulls from) refuses every update; pushOnly (an agent's
// branch, D-102) refuses any update but to that branch, and its deletion.
// user (the account the client signed in as) and workspace (the
// repository's) count the call against their limits; a full one is an error
// that is ErrBusy and says which.
func (c *Client) Serve(ctx context.Context, id, service, mode, protocol, protect, readOnly, pushOnly, user, workspace string, stdin io.Reader, stdout io.Writer) (Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if stdin == nil {
		stdin = http.NoBody
	} else {
		// The transport closes the body it sends; the caller's reader (an
		// SSH channel) stays the caller's.
		stdin = io.NopCloser(stdin)
	}
	headers := []string{"Toft-User", user, "Toft-Workspace", workspace}
	if protect != "" {
		headers = append(headers, "Toft-Protect-Default", "1", "Toft-Protect-Branch", url.QueryEscape(protect))
	}
	if readOnly != "" {
		headers = append(headers, "Toft-Read-Only", readOnly)
	}
	if pushOnly != "" {
		headers = append(headers, "Toft-Push-Only", pushOnly)
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/repos/"+url.PathEscape(id)+"/"+url.PathEscape(service)+"?mode="+url.QueryEscape(mode), protocol, stdin, headers...)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Result{}, busyError(strings.TrimSpace(string(msg)))
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Result{}, fmt.Errorf("git executor: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	if _, err := io.Copy(stdout, resp.Body); err != nil {
		return Result{}, fmt.Errorf("git executor: copy output: %w", err)
	}
	res := Result{Exit: -1}
	if v, err := strconv.Atoi(resp.Trailer.Get("Toft-Git-Exit")); err == nil {
		res.Exit = v
	}
	if b, err := base64.StdEncoding.DecodeString(resp.Trailer.Get("Toft-Git-Stderr")); err == nil {
		res.Stderr = string(b)
	}
	if u := resp.Trailer.Get("Toft-Git-Updated"); u != "" {
		if b, err := base64.StdEncoding.DecodeString(u); err == nil {
			_ = json.Unmarshal(b, &res.Updated)
		}
	}
	return res, nil
}

// Write runs a write (merge, head, pull-ref) on repository id with in as
// its JSON body, decoding the answer into out (nil to drop it).
func (c *Client) Write(ctx context.Context, id, op string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/repos/"+url.PathEscape(id)+"/"+url.PathEscape(op), "", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := browseStatus(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("git executor: decode %s: %w", op, err)
	}
	return nil
}

// Browse runs a read (summary, branches, tags, tree, blob, commits, commit,
// diff) on repository id with q, decoding its answer into v. A read the
// executor refuses is a *BrowseError; a full executor is ErrBusy.
func (c *Client) Browse(ctx context.Context, id, op string, q url.Values, v any) error {
	resp, err := c.do(ctx, http.MethodGet, "/v1/repos/"+url.PathEscape(id)+"/browse/"+url.PathEscape(op)+"?"+q.Encode(), "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := browseStatus(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("git executor: decode %s: %w", op, err)
	}
	return nil
}

// Raw is a file's bytes (ref and path in q) and their size; the caller
// closes them.
func (c *Client) Raw(ctx context.Context, id string, q url.Values) (io.ReadCloser, int64, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/repos/"+url.PathEscape(id)+"/raw?"+q.Encode(), "", nil)
	if err != nil {
		return nil, 0, err
	}
	if err := browseStatus(resp); err != nil {
		resp.Body.Close()
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

// browseStatus is the error a read's answer carries, nil for 200.
func browseStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	be := &BrowseError{Status: resp.StatusCode}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(be); err != nil || be.Code == "" {
		return fmt.Errorf("git executor: %s", resp.Status)
	}
	switch {
	case be.Code == "git_busy":
		return ErrBusy
	case be.Status >= 500:
		return fmt.Errorf("git executor: %s: %s", be.Code, be.Msg)
	}
	return be
}

// Remote runs an import's or a mirror's operation (ls, fetch or push) on
// repository id against the egress URL in req, decoding the answer into
// out. What the remote refused is a *BrowseError with code remote_failed.
func (c *Client) Remote(ctx context.Context, id, op string, req RemoteRequest, out any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/repos/"+url.PathEscape(id)+"/remote/"+url.PathEscape(op), "", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := browseStatus(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("git executor: decode %s: %w", op, err)
	}
	return nil
}
