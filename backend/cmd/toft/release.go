package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/buildinfo"
)

// toft release-check (D-68): asks the image registry for the newest release
// and writes /var/lib/toft/upgrade/available.json, which the panel shows
// beside the version it runs. toft-release-check.timer runs it daily, and
// the panel's "Check now" runs it on demand. It signs in with Docker's own
// login for the registry, so it works while the images are private.

const releaseCheckUsage = `usage: toft release-check [--repo ghcr.io/OWNER]

Writes the newest release of the panel's images to
/var/lib/toft/upgrade/available.json. Run as root on the panel host.`

// available is available.json.
type available struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
	// Error is why the last check failed; Latest is then the one before.
	Error string `json:"error,omitempty"`
}

func releaseCheckCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("release-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", getenv("TOFT_IMAGE_REPO"), "where the images are; ghcr.io/<GHCR_USER> from secrets.env by default")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usageError(releaseCheckUsage)
	}
	u := &upgrader{p: hostPaths, repo: *repo, out: stdout}
	env, err := readEnv(u.p.secrets)
	if err != nil {
		return fmt.Errorf("read %s: %w", u.p.secrets, err)
	}
	home := getenv("HOME")
	if home == "" {
		home = "/root"
	}
	c := registryClient{http: &http.Client{Timeout: 30 * time.Second}, auths: dockerAuths(filepath.Join(home, ".docker", "config.json"))}
	return u.releaseCheck(ctx, c, u.repoFor(env)+"/paas-backend", time.Now)
}

// releaseCheck writes available.json for image (host/owner/name).
func (u *upgrader) releaseCheck(ctx context.Context, c registryClient, image string, now func() time.Time) error {
	path := filepath.Join(u.p.work, "available.json")
	var av available
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &av)
	}
	av.Current, av.CheckedAt, av.Error = u.readState().Version, now().UTC(), ""
	tags, err := c.tags(ctx, image)
	if err == nil {
		av.Latest = newestRelease(tags)
	} else {
		av.Error = err.Error()
	}
	if err := os.MkdirAll(u.p.work, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(av, "", "  ")
	if werr := writeAtomic(path, b, 0o600); werr != nil {
		return werr
	}
	if err != nil {
		return fmt.Errorf("list the releases of %s: %w", image, err)
	}
	switch {
	case av.Latest == "":
		u.say("No release of %s yet.", image)
	case av.Current != "" && buildinfo.Newer(av.Latest, av.Current):
		u.say("Toft %s is available; this panel runs %s.", av.Latest, av.Current)
	default:
		u.say("Toft %s is the newest release.", av.Latest)
	}
	return nil
}

// newestRelease is the highest vX.Y.Z tag; others (latest, sha-…) don't count.
func newestRelease(tags []string) string {
	best := ""
	for _, t := range tags {
		// Pre-releases are offered by name only, never as the newest.
		if _, ok := buildinfo.Parse(t); ok && !strings.Contains(t, "-") && (best == "" || buildinfo.Newer(t, best)) {
			best = t
		}
	}
	return best
}

// registryClient lists an image's tags with the Docker registry API,
// answering its bearer-token challenge with Docker's stored login.
type registryClient struct {
	http *http.Client
	// auths maps a registry host to "user:password".
	auths map[string]string
}

// dockerAuths reads the logins `docker login` stored in path. Logins kept
// in a credential helper aren't readable here: the check then asks
// anonymously, which works for public images.
func dockerAuths(path string) map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return out
	}
	for host, a := range cfg.Auths {
		dec, err := base64.StdEncoding.DecodeString(a.Auth)
		if err != nil || !strings.Contains(string(dec), ":") {
			continue
		}
		host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		out[strings.TrimSuffix(host, "/")] = string(dec)
	}
	return out
}

func (c registryClient) tags(ctx context.Context, image string) ([]string, error) {
	host, name, ok := strings.Cut(image, "/")
	if !ok {
		return nil, fmt.Errorf("%s has no registry host", image)
	}
	endpoint := "https://" + host + "/v2/" + name + "/tags/list?n=1000"
	res, err := c.get(ctx, endpoint, "")
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusUnauthorized {
		challenge := res.Header.Get("WWW-Authenticate")
		res.Body.Close()
		token, err := c.token(ctx, host, challenge)
		if err != nil {
			return nil, err
		}
		if res, err = c.get(ctx, endpoint, "Bearer "+token); err != nil {
			return nil, err
		}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the registry answered %s", res.Status)
	}
	var body struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("read the tags: %w", err)
	}
	return body.Tags, nil
}

func (c registryClient) get(ctx context.Context, endpoint, authorization string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return c.http.Do(req)
}

// token answers a `Bearer realm=…,service=…,scope=…` challenge, with the
// host's stored login when there is one.
func (c registryClient) token(ctx context.Context, host, challenge string) (string, error) {
	params := map[string]string{}
	rest, ok := strings.CutPrefix(challenge, "Bearer ")
	if !ok {
		return "", fmt.Errorf("the registry asked for %q, not a bearer token", challenge)
	}
	for _, part := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		params[k] = strings.Trim(v, `"`)
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Scheme != "https" {
		return "", fmt.Errorf("the registry's token realm %q isn't https", params["realm"])
	}
	q := realm.Query()
	for _, k := range []string{"service", "scope"} {
		if params[k] != "" {
			q.Set(k, params[k])
		}
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	if login, ok := c.auths[host]; ok {
		user, pass, _ := strings.Cut(login, ":")
		req.SetBasicAuth(user, pass)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry refused this server's login (%s)", res.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("read the registry's token: %w", err)
	}
	if body.Token == "" {
		body.Token = body.AccessToken
	}
	if body.Token == "" {
		return "", errors.New("the registry gave no token")
	}
	return body.Token, nil
}
