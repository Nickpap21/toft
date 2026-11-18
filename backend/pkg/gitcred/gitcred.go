// Package gitcred keeps git credentials off command lines. A clone URL with
// user:token in it would show in the process list (ps) for as long as git
// runs; instead the credentials go in a git-credential-store file only the
// owner can read, and git is pointed at it.
package gitcred

import (
	"fmt"
	"net/url"
	"os"
)

// Split removes the credentials from an http(s) repo URL and returns them as
// a git-credential-store line. A URL with a user and no password (a token
// saved without a username) gets an empty password, which is what git sends
// for such a URL when it cannot prompt: the server sees the same request. A
// URL without credentials, or not http(s), is returned unchanged with no line.
func Split(repoURL string) (cloneURL, credLine string) {
	u, err := url.Parse(repoURL)
	if err != nil || u.User == nil || (u.Scheme != "https" && u.Scheme != "http") {
		return repoURL, ""
	}
	pass, _ := u.User.Password()
	user := url.UserPassword(u.User.Username(), pass)
	credLine = (&url.URL{Scheme: u.Scheme, User: user, Host: u.Host}).String()
	u.User = nil
	return u.String(), credLine
}

// HelperArgs are the git options that make git read credentials only from
// the credential-store file at path: put them before the git subcommand.
func HelperArgs(path string) []string {
	return []string{"-c", "credential.helper=", "-c", "credential.helper=store --file=" + path}
}

// ProtocolArgs are the git options that limit a clone or fetch to https,
// http, ssh and git: ext:: and fd:: would run or read what the URL names,
// and file:// or a path would read the host's own disk. Put them before the
// git subcommand; git hands -c options on to the git processes it starts.
func ProtocolArgs() []string {
	return []string{
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.http.allow=always",
		"-c", "protocol.ssh.allow=always",
		"-c", "protocol.git.allow=always",
	}
}

// Local splits repoURL (Split) and, when it carries credentials, writes them
// to a new file only the owner can read. It returns the URL to clone, the
// git options to put before the subcommand (ProtocolArgs, and HelperArgs
// with credentials), and a cleanup that removes the file.
func Local(repoURL string) (cloneURL string, gitArgs []string, cleanup func(), err error) {
	cloneURL, credLine := Split(repoURL)
	if credLine == "" {
		return cloneURL, ProtocolArgs(), func() {}, nil
	}
	f, err := os.CreateTemp("", "paas-git-credentials-*")
	if err != nil {
		return "", nil, nil, fmt.Errorf("create git credentials file: %w", err)
	}
	cleanup = func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(credLine + "\n"); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, nil, fmt.Errorf("write git credentials file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, nil, fmt.Errorf("write git credentials file: %w", err)
	}
	return cloneURL, append(ProtocolArgs(), HelperArgs(f.Name())...), cleanup, nil
}
