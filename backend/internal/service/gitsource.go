package service

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A repository URL and a branch end up as git command-line arguments on the
// panel host or a server. git reads an argument that starts with "-" as an
// option (--upload-pack=<command> runs the command), and a bare path clones
// from the host's filesystem. The deploy pipelines also put "--" before the
// positional arguments; this check is what tells the user, at save time.

// scpLikeURL is git's "user@host:path" form. The path may not start with
// "-" or "/" (a leading "/" would be ssh://host//path, which still works, but
// nothing hosted uses it, and it keeps the check simple).
var scpLikeURL = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~][^\s]*$`)

// branchForbidden are the characters git check-ref-format refuses, plus
// whitespace. ".." and "@{" are checked separately.
var branchForbidden = regexp.MustCompile(`[\s~^:?*\[\\\x00-\x1f\x7f]`)

// ValidateGitURL accepts an http(s), ssh or git URL with a host, or the
// scp-like form. Empty is accepted (no repository). Errors wrap
// ErrInvalidConfig.
func ValidateGitURL(field, raw string) error {
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("%w: %s cannot start with '-'", ErrInvalidConfig, field)
	}
	if len(raw) > 2048 {
		return fmt.Errorf("%w: %s is too long (2048 characters at most)", ErrInvalidConfig, field)
	}
	if scpLikeURL.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: %s must be an https://, ssh:// or git@host:path repository URL", ErrInvalidConfig, field)
	}
	switch u.Scheme {
	case "http", "https", "ssh", "git":
		return nil
	}
	return fmt.Errorf("%w: %s must use https, ssh or git, not %q", ErrInvalidConfig, field, u.Scheme)
}

// ValidateGitBranch accepts what git check-ref-format --branch does, and
// nothing that starts with "-". Empty is accepted (the default branch).
// Errors wrap ErrInvalidConfig.
func ValidateGitBranch(branch string) error {
	switch {
	case branch == "":
		return nil
	case strings.HasPrefix(branch, "-"):
		return fmt.Errorf("%w: git_branch cannot start with '-'", ErrInvalidConfig)
	case len(branch) > 255:
		return fmt.Errorf("%w: git_branch is too long (255 characters at most)", ErrInvalidConfig)
	case branchForbidden.MatchString(branch),
		strings.Contains(branch, ".."),
		strings.Contains(branch, "@{"),
		strings.HasSuffix(branch, ".lock"),
		strings.HasSuffix(branch, "."),
		strings.HasSuffix(branch, "/"),
		strings.HasPrefix(branch, "/"),
		strings.Contains(branch, "//"),
		strings.Contains(branch, "/."):
		return fmt.Errorf("%w: git_branch %q is not a valid branch name", ErrInvalidConfig, branch)
	}
	return nil
}

// ValidateBaseDirectory accepts a path inside the repository: after one
// leading "/" (the stored default is "/", and "/app" means "app") it has no
// ".." segment. Empty is the repository root. Errors wrap ErrInvalidConfig.
func ValidateBaseDirectory(dir string) error {
	if len(dir) > 255 {
		return fmt.Errorf("%w: base_directory is too long (255 characters at most)", ErrInvalidConfig)
	}
	if slices.Contains(strings.Split(strings.TrimPrefix(dir, "/"), "/"), "..") {
		return fmt.Errorf("%w: base_directory cannot contain '..'", ErrInvalidConfig)
	}
	return nil
}

// JoinBaseDirectory is where a build's base directory is under root, the
// folder it was cloned or unpacked into. Every build path joins through it,
// so a row saved before ValidateBaseDirectory still can't leave root.
func JoinBaseDirectory(root, dir string) (string, error) {
	if err := ValidateBaseDirectory(dir); err != nil {
		return "", err
	}
	p := filepath.Join(root, filepath.Clean(strings.TrimPrefix(dir, "/")))
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%w: base_directory is outside the build folder", ErrInvalidConfig)
	}
	return p, nil
}

// maxComposeContent matches the database's CHECK on services.compose_content.
const maxComposeContent = 1 << 20

func validateComposeContent(content string) error {
	if len(content) > maxComposeContent {
		return fmt.Errorf("%w: compose_content is larger than 1 MiB", ErrInvalidConfig)
	}
	return nil
}

// validateGitSource checks every repository URL and the branch of a
// service's input.
func validateGitSource(gitRepoURL, composeRepoURL, branch string) error {
	if err := ValidateGitURL("git_repo_url", gitRepoURL); err != nil {
		return err
	}
	if err := ValidateGitURL("compose_repo_url", composeRepoURL); err != nil {
		return err
	}
	return ValidateGitBranch(branch)
}

// credentialsMoved says which saved credential an update would send
// somewhere it wasn't saved for, unless the update enters it again: the git
// token to a repository URL on another origin (a deploy and the version
// check send it there), the registry password to another registry, or with
// an image from another registry (a local pull sends it to the image's).
// Otherwise a developer could point the URL at a host of theirs and read a
// credential that is never shown back.
func credentialsMoved(existing *Service, in UpdateConfigInput) (gitToken, registryPassword bool) {
	if existing.HasGitToken && in.GitToken == "" {
		saved := []string{urlOrigin(existing.GitRepoURL), urlOrigin(existing.ComposeRepoURL)}
		for _, o := range []string{urlOrigin(in.GitRepoURL), urlOrigin(in.ComposeRepoURL)} {
			if o != "" && o != saved[0] && o != saved[1] {
				gitToken = true
			}
		}
	}
	if existing.HasRegistryPassword && in.RegistryPassword == "" {
		registryPassword = !strings.EqualFold(in.RegistryServer, existing.RegistryServer) ||
			imageRegistry(in.DockerImage) != imageRegistry(existing.DockerImage)
	}
	return gitToken, registryPassword
}

// urlOrigin is an http(s) URL's scheme and host, "" for anything else (an
// scp-like URL gets no token).
func urlOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// SameRegistry reports whether two image references pull from the same
// registry: a saved registry password goes only with an image on the
// registry it was saved for.
func SameRegistry(a, b string) bool { return imageRegistry(a) == imageRegistry(b) }

// imageRegistry is the registry an image reference pulls from, "" for
// Docker Hub. As Docker reads a reference, what comes before the first "/"
// is a registry host when it has a "." or ":", is localhost, or has an
// uppercase letter (a repository path can't), and docker.io and
// index.docker.io are Docker Hub.
func imageRegistry(image string) string {
	host, _, ok := strings.Cut(image, "/")
	if !ok || !(strings.ContainsAny(host, ".:") || host == "localhost" || strings.ToLower(host) != host) ||
		host == "docker.io" || host == "index.docker.io" {
		return ""
	}
	return strings.ToLower(host)
}
