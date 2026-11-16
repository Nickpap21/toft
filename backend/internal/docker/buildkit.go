package docker

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/deployment"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/google/uuid"
)

// What keeps one service's BuildKit build from another's (D-167). Both go
// to every build as build arguments, after the service's own, where they
// win over a Dockerfile's "# syntax=" line and its ARGs:
//
//   - BUILDKIT_SYNTAX is the Dockerfile frontend every build runs, so a
//     repository can't have the builder fetch and run one it chose.
//   - BUILDKIT_CACHE_MOUNT_NS prefixes the ids of the build's cache mounts,
//     so a RUN --mount=type=cache reads and writes its own service's only.
//
// dockerfileFrontend is docker/dockerfile:1 as of 2026-11-16;
// scripts/dockerfile-frontend.sh fails in CI when the tag has moved on.
const (
	dockerfileFrontend = "docker/dockerfile@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e"
	buildSyntaxArg     = "BUILDKIT_SYNTAX"
	buildCacheNSArg    = "BUILDKIT_CACHE_MOUNT_NS"
)

// buildNamespace is the cache-mount namespace of serviceID's builds. No
// namespace is a prefix of another: a service ID has no "/".
func buildNamespace(serviceID string) string { return "paas/" + serviceID }

// builtService is the service whose build makes imageTag, which is
// paas/<service ID>:<deployment ID> (deployment's gitImageTag). Any other
// tag is refused: a build without its service has no namespace, and the
// tag goes into a shell command on a remote server as it is.
func builtService(imageTag string) (string, error) {
	repo, tag, _ := strings.Cut(imageTag, ":")
	id, ok := strings.CutPrefix(repo, "paas/")
	if !ok || !isUUID(id) || !isUUID(tag) {
		return "", fmt.Errorf("build of %q: not a service's image tag", imageTag)
	}
	return id, nil
}

// isUUID is whether s is a UUID in the form the database writes.
func isUUID(s string) bool { return len(s) == 36 && uuid.Validate(s) == nil }

// buildArgList is the KEY=VALUE of each --build-arg of a build of
// imageTag: buildArgs in order of key, then the two that isolate it. A
// BUILDKIT_ argument among buildArgs is dropped: those steer the builder,
// and a Nixpacks plan's variables come from the repository.
func buildArgList(imageTag string, buildArgs map[string]*string) ([]string, error) {
	serviceID, err := builtService(imageTag)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(buildArgs)+2)
	for _, k := range slices.Sorted(maps.Keys(buildArgs)) {
		if v := buildArgs[k]; v != nil && !strings.HasPrefix(k, "BUILDKIT_") {
			out = append(out, k+"="+*v)
		}
	}
	return append(out, buildSyntaxArg+"="+dockerfileFrontend, buildCacheNSArg+"="+buildNamespace(serviceID)), nil
}

// nixpacksArgs are the flags `nixpacks plan` and `nixpacks build` both
// take: the commands that replace the detected ones, and buildArgs as
// --env, in order of key.
func nixpacksArgs(cmds deployment.NixpacksCommands, buildArgs map[string]*string) []string {
	args := nixpacksCommandArgs(cmds)
	for _, k := range slices.Sorted(maps.Keys(buildArgs)) {
		if v := buildArgs[k]; v != nil {
			args = append(args, "--env", k+"="+*v)
		}
	}
	return args
}

// nixpacksVariables are the variables of a Nixpacks plan (the JSON
// `nixpacks plan` prints), as build arguments: the Dockerfile Nixpacks
// writes declares each as an ARG, and nixpacks' own docker build passes
// them the same way. Toft runs that build itself (buildArgList), because a
// repository's nixpacks.toml can name BUILDKIT_SYNTAX and
// BUILDKIT_CACHE_MOUNT_NS as variables, and those beat --env.
func nixpacksVariables(plan []byte) (map[string]*string, error) {
	var p struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal(plan, &p); err != nil {
		return nil, fmt.Errorf("read the nixpacks plan: %w", err)
	}
	out := make(map[string]*string, len(p.Variables))
	for k, v := range p.Variables {
		out[k] = &v
	}
	return out, nil
}

// registryConfig is a docker CLI config.json holding a's credentials for
// server ("" is Docker Hub) and nothing else: what `docker login` would
// write into the host's own config, for a config directory one deploy
// uses. A credential that is only a token still needs a username.
func registryConfig(a rollout.RegistryAuth, server string) ([]byte, error) {
	user := a.Username
	if user == "" {
		user = "token"
	}
	// The CLI looks a registry up by its host, and Docker Hub by its
	// index's URL.
	host, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(server, "https://"), "http://"), "/")
	switch host {
	case "", "docker.io", "index.docker.io", "registry-1.docker.io":
		host = "https://index.docker.io/v1/"
	}
	return json.Marshal(map[string]any{"auths": map[string]any{
		host: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(user + ":" + a.Password))},
	}})
}
