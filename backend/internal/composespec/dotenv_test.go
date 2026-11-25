package composespec_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceEnv_DropsCLINamesAndInvalidNames(t *testing.T) {
	kept, dropped := composespec.ServiceEnv(map[string]string{
		"DATABASE_URL":   "postgres://x",
		"_PRIVATE":       "1",
		"PATH":           "/evil",
		"HOME":           "/tmp",
		"DOCKER_HOST":    "tcp://attacker:2375",
		"COMPOSE_FILE":   "/etc/passwd",
		"LD_PRELOAD":     "/x.so",
		"BAD KEY":        "v",
		"A\nB":           "v",
		"1STARTS_DIGIT":  "v",
		"OTEL_SVC_NAME":  "web",
		"XDG_CONFIG_DIR": "/x",
	})
	assert.Equal(t, map[string]string{"DATABASE_URL": "postgres://x", "_PRIVATE": "1", "OTEL_SVC_NAME": "web"}, kept)
	assert.Equal(t, []string{"1STARTS_DIGIT", "A\nB", "BAD KEY", "COMPOSE_FILE", "DOCKER_HOST", "HOME", "LD_PRELOAD", "PATH", "XDG_CONFIG_DIR"}, dropped)
}

// parseDotEnv reads a .env file as docker compose does, recording whether
// any variable was looked up.
func parseDotEnv(t *testing.T, content []byte) (map[string]string, bool) {
	t.Helper()
	looked := false
	got, err := dotenv.ParseWithLookup(bytes.NewReader(content), func(string) (string, bool) { looked = true; return "x", true })
	require.NoError(t, err)
	return got, looked
}

func TestDotEnvFile_ReadBackExactly(t *testing.T) {
	env := map[string]string{
		"A":     "plain",
		"HASH":  "admin:$2y$05$abcdefghijklmnopqrstuv",
		"MULTI": "line1\nline2",
		"REF":   "${HOME}",
		"SQ":    "it's",
		"DQ":    `say "hi"`,
		// A trailing backslash must not escape the closing quote and pull
		// the next line into this value.
		"TRAIL": `C:\path\`,
		"NEXT":  "x\nCOMPOSE_FILE=/etc/passwd",
		// Backslash before a quote, a newline and a dollar.
		"BSQ":    `\"`,
		"BSNL":   "a\\\nb",
		"BSD":    `\$HOME`,
		"ESC":    `\n\t\\0123\x41`,
		"CTRL":   "tab\there\rcr",
		"UNI":    "héllo ✓",
		"EMPTY":  "",
		"HASHES": "a #not a comment",
		"SPACES": "  padded  ",
	}
	content, err := composespec.DotEnvFile(env)
	require.NoError(t, err)

	got, looked := parseDotEnv(t, content)
	assert.Equal(t, env, got, "compose's dotenv parser reads every value back unchanged")
	assert.False(t, looked, "nothing in the file is expanded")
	assert.Contains(t, string(content), "A=\"plain\"\n")
	assert.Contains(t, string(content), `HASH="admin:\$2y\$05\$abcdefghijklmnopqrstuv"`)
}

func TestDotEnvFile_RefusesWhatCannotBeWrittenExactly(t *testing.T) {
	_, err := composespec.DotEnvFile(map[string]string{"BAD KEY": "v"})
	assert.Error(t, err)
	_, err = composespec.DotEnvFile(map[string]string{"A\nB": "v"})
	assert.Error(t, err)
}

func TestStageDotEnv(t *testing.T) {
	t.Run("replaces the repository's .env in the compose file's directory", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			"envs/dev/docker-compose.yml": "services: {}\n",
			"envs/dev/.env":               "LOCK=false\n",
			".env":                        "ROOT=1\n",
		})
		require.NoError(t, composespec.StageDotEnv(root, "envs/dev/docker-compose.yml", map[string]string{"A": "1", "DOCKER_HOST": "tcp://x"}))
		content, err := os.ReadFile(filepath.Join(root, "envs/dev/.env"))
		require.NoError(t, err)
		got, _ := parseDotEnv(t, content)
		assert.Equal(t, map[string]string{"A": "1"}, got, "only the service's env vars, as the remote deploy writes them")
		info, err := os.Stat(filepath.Join(root, "envs/dev/.env"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		other, err := os.ReadFile(filepath.Join(root, ".env"))
		require.NoError(t, err)
		assert.Equal(t, "ROOT=1\n", string(other), "other directories' .env files are left alone")
	})
	t.Run("an empty env still replaces the file", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"docker-compose.yml": "services: {}\n", ".env": "LOCK=false\n"})
		require.NoError(t, composespec.StageDotEnv(root, "docker-compose.yml", nil))
		content, err := os.ReadFile(filepath.Join(root, ".env"))
		require.NoError(t, err)
		assert.Empty(t, content)
	})
	t.Run("a symlinked .env is replaced, not followed", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		target := filepath.Join(outside, "target")
		require.NoError(t, os.WriteFile(target, []byte("KEEP=1\n"), 0o644))
		writeFiles(t, root, map[string]string{"docker-compose.yml": "services: {}\n"})
		require.NoError(t, os.Symlink(target, filepath.Join(root, ".env")))
		require.NoError(t, composespec.StageDotEnv(root, "docker-compose.yml", map[string]string{"A": "1"}))
		kept, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "KEEP=1\n", string(kept))
		info, err := os.Lstat(filepath.Join(root, ".env"))
		require.NoError(t, err)
		assert.True(t, info.Mode().IsRegular())
	})
	t.Run("a compose directory outside the repository is refused", func(t *testing.T) {
		root, outside := t.TempDir(), t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(root, "envs")))
		err := composespec.StageDotEnv(root, "envs/docker-compose.yml", map[string]string{"A": "1"})
		require.Error(t, err)
		assert.NotContains(t, err.Error(), root, "the error does not show the temp directory")
		_, statErr := os.Stat(filepath.Join(outside, ".env"))
		assert.True(t, os.IsNotExist(statErr), "nothing is written outside the repository")

		assert.Error(t, composespec.StageDotEnv(root, "../docker-compose.yml", nil))
		assert.Error(t, composespec.StageDotEnv(root, "/etc/docker-compose.yml", nil))
		assert.Error(t, composespec.StageDotEnv(root, "missing/docker-compose.yml", nil))
	})
}

// A remote deploy replaces the compose directory's .env with the service's
// env vars, and compose reads that file for files included from the same
// directory. Once it is staged, the check sees what compose will: the
// repository's .env cannot neutralise a dangerous default at check time.
func TestStageDotEnv_IncludedFileIsCheckedWithTheDeployedEnv(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"docker-compose.yml": "include:\n  - svc.yml\n",
		"svc.yml":            "services:\n  app:\n    image: alpine\n    privileged: ${LOCK:-true}\n    volumes: [\"${ROOT:-/}:/host\"]\n",
		".env":               "LOCK=false\nROOT=.\n",
	})
	check := func() []composespec.Violation {
		scan, err := composespec.ScanRepo(root)
		require.NoError(t, err)
		require.Empty(t, scan)
		return composespec.Validate(context.Background(), composespec.Source{
			Root: root, ProjectDir: root, File: "docker-compose.yml", ProjectName: "paas-test-12345678",
			Env: map[string]string{"A": "1"},
		})
	}
	require.Empty(t, check(), "with the repository's .env the included file looks harmless")

	require.NoError(t, composespec.StageDotEnv(root, "docker-compose.yml", map[string]string{"A": "1"}))
	assert.Equal(t, []string{"host_bind_mount", "privileged"}, rulesOf(check()))
}

// On the panel's host compose reads the repository's own .env, with the
// service's variables above it: a line of the file that reads one gets the
// service's value, and a name of the docker CLI's is left out.
func TestLocalEnv(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{".env": "A=repo\nB=repo\nD=${A}-${PASS:-nopass}\nDOCKER_HOST=tcp://x\n"})
	env, dropped, err := composespec.LocalEnv(dir, map[string]string{"A": "svc", "PASS": "p"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "svc", "B": "repo", "D": "svc-p", "PASS": "p"}, env)
	assert.Equal(t, []string{"DOCKER_HOST"}, dropped)

	// The deploy policy reads the same file to the same values.
	same := t.TempDir()
	writeFiles(t, same, map[string]string{
		".env":               "A=repo\nD=${A}-${PASS:-nopass}\n",
		"docker-compose.yml": "services:\n  web:\n    image: \"nginx:${D}\"\n",
	})
	service := map[string]string{"A": "svc", "PASS": "p"}
	env, _, err = composespec.LocalEnv(same, service)
	require.NoError(t, err)
	p, vs, err := composespec.Load(context.Background(), composespec.Source{
		Root: same, File: "docker-compose.yml", ProjectName: "paas-test-12345678", ReadDotEnv: true, Env: service,
	})
	require.NoError(t, err)
	require.Empty(t, vs)
	assert.Equal(t, "nginx:svc-p", p.Services["web"].Image)
	assert.Equal(t, "svc-p", env["D"])

	env, dropped, err = composespec.LocalEnv(t.TempDir(), map[string]string{"A": "svc"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "svc"}, env, "no .env: the service's variables")
	assert.Empty(t, dropped)
}

// compose hands a build secret that is a variable to the build's own
// process, under the variable's name: one the build tools read as a setting
// is refused there, and stays free everywhere else.
func TestValidate_BuildSecretFromAVariableTheBuildToolsRead(t *testing.T) {
	stack := func(name string) string {
		return "services:\n  app:\n    build:\n      context: .\n      secrets: [p]\nsecrets:\n  p:\n    environment: " + name + "\n"
	}
	for _, name := range []string{
		"EXPERIMENTAL_BUILDKIT_SOURCE_POLICY", "BUILDX_BUILDER", "DOCKER_HOST", "HTTPS_PROXY", "https_proxy", "NO_PROXY",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "GODEBUG", "OTEL_EXPORTER_OTLP_ENDPOINT", "GRPC_GO_LOG_SEVERITY_LEVEL",
	} {
		t.Run(name, func(t *testing.T) {
			vs := validateInline(t, stack(name), map[string]string{name: "x"})
			require.Equal(t, []string{"reserved_env"}, rulesOf(vs), "%+v", vs)
			assert.Equal(t, "secrets.p.environment", vs[0].Field)
		})
	}
	assert.Empty(t, validateInline(t, stack("NPM_TOKEN"), map[string]string{"NPM_TOKEN": "x"}), "an ordinary variable is a build secret")
	assert.Empty(t, validateInline(t, "services:\n  app:\n    image: alpine\n    secrets: [p]\n    environment:\n      HTTPS_PROXY: ${HTTPS_PROXY}\n      OTEL_SERVICE_NAME: web\n"+
		"secrets:\n  p:\n    environment: SSL_CERT_FILE\n", map[string]string{"HTTPS_PROXY": "http://proxy:3128", "SSL_CERT_FILE": "x"}),
		"the same names are the app's to use: a container's variables and its own secrets")
}

// compose runs with four variables of the platform's own (RunEnv). A secret
// or a config whose source is one of them would put the platform's value in
// a container's file, whatever the service's variable of that name says.
func TestValidate_SecretFromAPlatformVariable(t *testing.T) {
	stack := func(kind, name string) string {
		return "services:\n  app:\n    image: alpine\n    " + kind + ": [p]\n" + kind + ":\n  p:\n    environment: " + name + "\n"
	}
	for _, name := range composespec.RunEnv {
		for _, kind := range []string{"secrets", "configs"} {
			t.Run(kind+" "+name, func(t *testing.T) {
				vs := validateInline(t, stack(kind, name), map[string]string{name: "x"})
				require.Equal(t, []string{"reserved_env"}, rulesOf(vs), "%+v", vs)
				assert.Equal(t, kind+".p.environment", vs[0].Field)
			})
		}
	}
	assert.Empty(t, validateInline(t, stack("secrets", "APP_TOKEN"), map[string]string{"APP_TOKEN": "x"}), "a variable of the service's own is a secret")
}
