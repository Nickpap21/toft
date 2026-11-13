package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestValidateBaseDirectory(t *testing.T) {
	for _, ok := range []string{"", "/", ".", "app", "/app", "app/sub", "/app/sub/", "a/./b", "..a", "a..", "a/...", ".hidden"} {
		if err := ValidateBaseDirectory(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"..", "../x", "../..", "a/../../b", "a/..", "/../x", "/..", "a/b/../../..", strings.Repeat("a", 256)} {
		if err := ValidateBaseDirectory(bad); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%q: got %v, want ErrInvalidConfig", bad, err)
		}
	}
}

func TestJoinBaseDirectory(t *testing.T) {
	for _, c := range []struct{ base, want string }{
		{"", "/tmp/b"}, {"/", "/tmp/b"}, {".", "/tmp/b"}, {"app", "/tmp/b/app"}, {"/app", "/tmp/b/app"},
		{"app/sub", "/tmp/b/app/sub"}, {"a/./b", "/tmp/b/a/b"},
	} {
		got, err := JoinBaseDirectory("/tmp/b", c.base)
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.base, got, err, c.want)
		}
	}
	for _, bad := range []string{"..", "../x", "a/../../b", "/../x"} {
		if got, err := JoinBaseDirectory("/tmp/b", bad); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%q: got %q, %v; want ErrInvalidConfig", bad, got, err)
		}
	}
}

// A base directory that leaves the build folder is refused on every save.
func TestBaseDirectoryIsValidatedOnSave(t *testing.T) {
	ctx := context.Background()
	repo := &recordingRepo{fakeRepo: fakeRepo{svc: &Service{ID: "s1", Name: "app"}}}
	m := NewServiceManager(repo, zap.NewNop())

	_, err := m.Create(ctx, "env-1", "", CreateInput{Name: "app", SourceType: SourceTypeGit, BaseDirectory: "../../var/lib/docker/volumes"})
	if !errors.Is(err, ErrInvalidConfig) || repo.lastCreate != nil {
		t.Errorf("Create: %v, saved %v", err, repo.lastCreate)
	}
	_, err = m.CreateProjectScoped(ctx, "p1", "", CreateInput{Name: "app", SourceType: SourceTypeGit, BaseDirectory: "a/../.."})
	if !errors.Is(err, ErrInvalidConfig) || repo.lastCreate != nil {
		t.Errorf("CreateProjectScoped: %v, saved %v", err, repo.lastCreate)
	}
	_, err = m.CreateOrgScoped(ctx, "o1", "", CreateInput{Name: "app", SourceType: SourceTypeGit, BaseDirectory: ".."})
	if !errors.Is(err, ErrInvalidConfig) || repo.lastCreate != nil {
		t.Errorf("CreateOrgScoped: %v, saved %v", err, repo.lastCreate)
	}
	_, err = m.UpdateConfig(ctx, "s1", UpdateConfigInput{BaseDirectory: "../x"})
	if !errors.Is(err, ErrInvalidConfig) || repo.lastUpdate != nil {
		t.Errorf("UpdateConfig: %v, saved %v", err, repo.lastUpdate)
	}

	for _, ok := range []string{"", "/", "/app"} {
		if _, err := m.UpdateConfig(ctx, "s1", UpdateConfigInput{BaseDirectory: ok}); err != nil {
			t.Errorf("UpdateConfig %q: %v", ok, err)
		}
	}
}

func TestValidateGitURL(t *testing.T) {
	ok := []string{
		"",
		"https://github.com/org/repo.git",
		"http://gitea.internal/org/repo",
		"https://user:tok@github.com/org/repo.git",
		"ssh://git@github.com/org/repo.git",
		"git://example.com/repo.git",
		"git@github.com:org/repo.git",
		"deploy@gitea.example.com:group/sub/repo",
	}
	for _, u := range ok {
		if err := ValidateGitURL("git_repo_url", u); err != nil {
			t.Errorf("%q: unexpected error %v", u, err)
		}
	}
	bad := []string{
		"--upload-pack=touch /tmp/x",
		"-c",
		"-",
		"/etc",
		"../repo",
		"repo",
		"file:///var/lib",
		"ext::sh -c id",
		"git@github.com:-repo",
		"https://",
		"javascript:alert(1)",
	}
	for _, u := range bad {
		err := ValidateGitURL("git_repo_url", u)
		if err == nil {
			t.Errorf("%q: accepted", u)
		} else if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%q: error %v does not wrap ErrInvalidConfig", u, err)
		}
	}
}

func TestValidateGitBranch(t *testing.T) {
	ok := []string{"", "main", "feature/login-form", "release-1.2.3", "v2", "user/nikos/wip_2", "HEAD"}
	for _, b := range ok {
		if err := ValidateGitBranch(b); err != nil {
			t.Errorf("%q: unexpected error %v", b, err)
		}
	}
	bad := []string{
		"--upload-pack=id", "-x", "a b", "a..b", "a@{1}", "a.lock", "a:b", "a~1", "a^", "a?", "a*", "a[0]",
		"a\\b", "/a", "a/", "a//b", "a/.b", "a.", "a\x01b",
	}
	for _, b := range bad {
		err := ValidateGitBranch(b)
		if err == nil {
			t.Errorf("%q: accepted", b)
		} else if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%q: error %v does not wrap ErrInvalidConfig", b, err)
		}
	}
}

// A repository source (D-77) is a git build's or compose files' source, and
// takes the place of their URLs.
func TestValidateRepositorySource(t *testing.T) {
	const id = "0b6f9c4e-2a51-4f0e-9d6e-6f0c2a7b9d11"
	cases := []struct {
		source           SourceType
		repo, git, other string
		ok               bool
	}{
		{SourceTypeGit, "", "https://github.com/o/r.git", "", true},
		{SourceTypeGit, id, "", "", true},
		{SourceTypeDockerCompose, id, "", "", true},
		{SourceTypeDockerImage, id, "", "", false},
		{SourceTypeGit, id, "https://github.com/o/r.git", "", false},
		{SourceTypeDockerCompose, id, "", "https://github.com/o/infra.git", false},
		{SourceTypeGit, "not-a-uuid", "", "", false},
	}
	for _, tc := range cases {
		err := validateRepositorySource(tc.source, tc.repo, tc.git, tc.other)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrInvalidConfig)) {
			t.Errorf("validateRepositorySource(%s, %q, %q, %q) = %v", tc.source, tc.repo, tc.git, tc.other, err)
		}
	}
}

// A saved credential never follows the service to a new destination unless
// it is entered again.
func TestCredentialsMoved(t *testing.T) {
	saved := &Service{GitRepoURL: "https://github.com/org/app.git", HasGitToken: true,
		RegistryServer: "ghcr.io", DockerImage: "ghcr.io/org/app:1", HasRegistryPassword: true}
	same := UpdateConfigInput{GitRepoURL: "https://GitHub.com/org/other.git", RegistryServer: "GHCR.io", DockerImage: "ghcr.io/org/app:2"}
	cases := []struct {
		name          string
		in            UpdateConfigInput
		git, registry bool
	}{
		{"same hosts", same, false, false},
		{"repository on another host", UpdateConfigInput{GitRepoURL: "https://gitlab.attacker.example/a/b.git", RegistryServer: "ghcr.io", DockerImage: "ghcr.io/org/app:1"}, true, false},
		{"over plain http", UpdateConfigInput{GitRepoURL: "http://github.com/org/app.git", RegistryServer: "ghcr.io", DockerImage: "ghcr.io/org/app:1"}, true, false},
		{"compose repository on another host", UpdateConfigInput{GitRepoURL: same.GitRepoURL, ComposeRepoURL: "https://evil.example/c.git", RegistryServer: "ghcr.io", DockerImage: "ghcr.io/org/app:1"}, true, false},
		{"another registry", UpdateConfigInput{GitRepoURL: same.GitRepoURL, RegistryServer: "registry.attacker.example", DockerImage: "ghcr.io/org/app:1"}, false, true},
		{"an image on another registry", UpdateConfigInput{GitRepoURL: same.GitRepoURL, RegistryServer: "ghcr.io", DockerImage: "registry.attacker.example/x:1"}, false, true},
		{"an image on Docker Hub", UpdateConfigInput{GitRepoURL: same.GitRepoURL, RegistryServer: "ghcr.io", DockerImage: "org/app:1"}, false, true},
		{"entered again", UpdateConfigInput{GitRepoURL: "https://evil.example/a.git", GitToken: "new", RegistryServer: "evil.example", RegistryPassword: "new"}, false, false},
	}
	for _, c := range cases {
		git, registry := credentialsMoved(saved, c.in)
		if git != c.git || registry != c.registry {
			t.Errorf("%s: cleared git %v registry %v, want %v %v", c.name, git, registry, c.git, c.registry)
		}
	}
	if git, registry := credentialsMoved(&Service{}, UpdateConfigInput{GitRepoURL: "https://evil.example/a.git"}); git || registry {
		t.Error("nothing saved: nothing to clear")
	}
}

// Two images are on the same registry as Docker reads their references: a
// saved registry password goes only with an image on its own (SEC-011).
func TestSameRegistry(t *testing.T) {
	same := [][2]string{
		{"nginx:1.27", "acme/app:1"},
		{"nginx", "docker.io/library/nginx:1.27"},
		{"acme/app", "index.docker.io/acme/app"},
		{"ghcr.io/acme/app:1", "GHCR.io/acme/other@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"nginx@sha256:0123", "library/nginx:1"},
		{"localhost:5000/x:1", "localhost:5000/y"},
		{"paas-web-1234abcd:abc123", ""},
	}
	for _, c := range same {
		if !SameRegistry(c[0], c[1]) {
			t.Errorf("%s and %s: another registry, want the same", c[0], c[1])
		}
	}
	other := [][2]string{
		{"ghcr.io/acme/app:2", "evil.example/x:1"},
		{"ghcr.io/acme/app:2", "nginx:1.27"},
		{"ghcr.io/acme/app:2", "ghcr.io.evil.example/acme/app:1"},
		{"ghcr.io/acme/app:2", "ghcr.io:443/acme/app:1"},
		{"localhost:5000/x", "localhost:5001/x"},
		{"localhost/x", "x/y"},
		// An uppercase first component is a host to Docker, never a Docker
		// Hub user (whose names are lowercase).
		{"evil/x:1", "Evil/x:1"},
		{"acme/app:1", "Evil/x:1"},
	}
	for _, c := range other {
		if SameRegistry(c[0], c[1]) {
			t.Errorf("%s and %s: the same registry, want another", c[0], c[1])
		}
	}
}

type imageRepo struct {
	Repository
	svc     *Service
	cleared bool
}

func (r *imageRepo) GetByID(context.Context, string) (*Service, error) { return r.svc, nil }
func (r *imageRepo) UpdateDockerImage(_ context.Context, _, _ string, clear bool) error {
	r.cleared = clear
	return nil
}

// A deploy's image override clears the saved registry password when the
// image comes from another registry, as a save does.
func TestUpdateDockerImageClearsAMovedRegistryPassword(t *testing.T) {
	for _, c := range []struct {
		image string
		want  bool
	}{{"ghcr.io/org/app:2", false}, {"registry.attacker.example/x:1", true}, {"org/app:1", true}} {
		repo := &imageRepo{svc: &Service{DockerImage: "ghcr.io/org/app:1", HasRegistryPassword: true}}
		cleared, err := NewServiceManager(repo, zap.NewNop()).UpdateDockerImage(context.Background(), "s1", c.image)
		if err != nil || cleared != c.want || repo.cleared != c.want {
			t.Errorf("%s: cleared %v (repo %v), %v; want %v", c.image, cleared, repo.cleared, err, c.want)
		}
	}
}
