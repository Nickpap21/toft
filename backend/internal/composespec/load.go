package composespec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/consts"
	"github.com/compose-spec/compose-go/v2/dotenv"
	interp "github.com/compose-spec/compose-go/v2/interpolation"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/tree"
	"github.com/compose-spec/compose-go/v2/types"
)

// InlineFileName is the name the orchestrators give inline compose content.
const InlineFileName = "docker-compose.yml"

// Source describes one compose file to load.
type Source struct {
	// Root is the directory the deployment's files live in: the clone of an
	// infra repository, or a stand-in directory for inline content (it need
	// not exist then). Everything the file references must stay inside it.
	Root string
	// ProjectDir is the compose project directory: relative paths resolve
	// against it, as with `docker compose --project-directory`. It must be
	// inside Root; empty means Root.
	ProjectDir string
	// File is the compose file's path relative to Root. Empty means the file
	// is inline: Content is used and no other file may be referenced.
	File string
	// Content is the inline compose YAML (when File is empty).
	Content string
	// ProjectName is the name passed as --project-name.
	ProjectName string
	// Env is the interpolation environment: the service's env vars, exactly
	// as the deploy hands them to docker compose. docker compose also sees
	// the platform's own variables (PATH, HOME, DOCKER_*, ...; see
	// IsPlatformEnv); a file that reads one of those is refused, because its
	// value at deploy time is not known here.
	Env map[string]string
	// ReadDotEnv also reads <ProjectDir>/.env for interpolation, below Env,
	// as docker compose does when the deploy does not overwrite that file.
	ReadDotEnv bool
	// Profiles to enable. nil enables every profile ("*"), so services that
	// a COMPOSE_PROFILES value could switch on are loaded and checked too.
	Profiles []string
	// Overrides are platform-generated override files, applied after the
	// compose file in order, as the deploy passes them with extra -f flags.
	Overrides []Override
	// AppNetworks is the network policy in force (TOFT_APP_NETWORKS).
	// Inspect applies CheckAppNetworks when it is isolated.
	AppNetworks AppNetworkPolicy
}

// Override is a platform-generated compose override file.
type Override struct {
	// Name is the file name, in the project directory.
	Name    string
	Content []byte
}

// ErrInvalid wraps every error that means the compose file could not be
// loaded (bad YAML, schema errors, unknown services, missing files).
var ErrInvalid = errors.New("invalid compose file")

// Load reads, interpolates and normalises a compose file with compose-go,
// confined to src.Root, and returns the typed project with relative paths
// made absolute. It does not resolve env_file contents.
//
// The returned violations are references outside Root found while loading,
// include options that would read files outside a resource loader, and reads
// of the platform's own environment variables (IsPlatformEnv). When there are
// any, the project is nil: the file was not loaded (loading it would read
// those files), or its model would differ from the one docker compose builds
// at deploy time. A non-nil error wraps ErrInvalid.
//
// Load does not apply the policy itself; call Check on the project. It is
// meant to be reused by anything that needs the loaded model (for example to
// rewrite networks before deploying).
func Load(ctx context.Context, src Source) (*types.Project, []Violation, error) {
	if src.Root == "" || !filepath.IsAbs(src.Root) {
		return nil, nil, fmt.Errorf("%w: root must be an absolute path", ErrInvalid)
	}
	root := filepath.Clean(src.Root)
	projectDir := root
	if src.ProjectDir != "" {
		projectDir = filepath.Clean(src.ProjectDir)
		if !filepath.IsAbs(projectDir) {
			projectDir = filepath.Join(root, projectDir)
		}
	}
	if !Within(root, projectDir) {
		return nil, []Violation{{Rule: RuleExternalRef, Field: "project_directory", Message: "the project directory is outside the repository"}}, nil
	}

	inline := src.File == ""
	var file types.ConfigFile
	if inline {
		if strings.TrimSpace(src.Content) == "" {
			return nil, nil, fmt.Errorf("%w: compose content is empty", ErrInvalid)
		}
		file = types.ConfigFile{Filename: filepath.Join(projectDir, InlineFileName), Content: []byte(src.Content)}
	} else {
		p := filepath.Join(root, src.File)
		if filepath.IsAbs(src.File) || hasDotDot(src.File) || !Within(root, p) {
			return nil, []Violation{{Rule: RuleExternalRef, Field: "compose_file_path", Message: "the compose file path must be inside the repository"}}, nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: read %s: %v", ErrInvalid, src.File, err)
		}
		file = types.ConfigFile{Filename: p, Content: content}
	}

	env := map[string]string{}
	refs := &varRecorder{names: map[string]bool{}}
	if src.ReadDotEnv && !inline {
		dotEnv, vs, err := readDotEnv(root, projectDir, src.Env, refs)
		if err != nil {
			return nil, nil, err
		}
		if len(vs) > 0 {
			return nil, vs, nil
		}
		for k, v := range dotEnv {
			env[k] = v
		}
	}
	for k, v := range src.Env {
		env[k] = v
	}

	profiles := src.Profiles
	if profiles == nil {
		profiles = []string{"*"}
	}

	sandbox := &sandboxLoader{root: root, inline: inline}
	files := []types.ConfigFile{file}
	for _, o := range src.Overrides {
		if o.Name == "" || o.Name != filepath.Base(o.Name) {
			return nil, nil, fmt.Errorf("%w: invalid override file name %q", ErrInvalid, o.Name)
		}
		files = append(files, types.ConfigFile{Filename: filepath.Join(projectDir, o.Name), Content: o.Content})
	}
	details := types.ConfigDetails{
		WorkingDir:  projectDir,
		ConfigFiles: files,
		Environment: env,
	}
	project, err := loader.LoadWithContext(ctx, details, func(o *loader.Options) {
		o.SetProjectName(src.ProjectName, true)
		o.Profiles = profiles
		// env_file contents are not needed to decide anything, and reading
		// them is the deploy's job. label_file is read below, after its
		// paths are checked.
		o.SkipResolveEnvironment = true
		o.SkipResolveLabels = true
		o.ResourceLoaders = []loader.ResourceLoader{sandbox}
		o.Interpolate = guardInterpolation(o.Interpolate, refs, sandbox)
	})
	if len(sandbox.violations) > 0 {
		return nil, sandbox.violations, nil
	}
	if vs := refs.platformViolations("", "the compose file"); len(vs) > 0 {
		return nil, vs, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	// Label files are read only once every one of them is inside Root, so
	// their labels can be checked like inline ones.
	var vs []Violation
	for _, svc := range project.AllServices() {
		for i, lf := range svc.LabelFiles {
			if !Within(root, lf) {
				vs = append(vs, Violation{
					Rule:    RuleExternalRef,
					Service: svc.Name,
					Field:   fmt.Sprintf("services.%s.label_file[%d]", svc.Name, i),
					Message: fmt.Sprintf("label file %s is outside the project", lf),
				})
			}
		}
	}
	if len(vs) > 0 {
		return nil, vs, nil
	}
	project, err = project.WithServicesLabelsResolved(false)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return project, nil, nil
}

// Validate loads src and checks it. A file that cannot be loaded gives one
// RuleComposeInvalid violation; Validate never returns an error for it.
func Validate(ctx context.Context, src Source) []Violation {
	vs, _ := Inspect(ctx, src)
	return vs
}

// Inspect is Validate that also returns the names of the file's services,
// sorted, including those in profiles that are not enabled. services is nil
// when the file was not loaded.
func Inspect(ctx context.Context, src Source) (violations []Violation, services []string) {
	violations, services, _, _ = InspectLimits(ctx, src)
	return violations, services
}

// InspectLimits is Inspect that also returns each service's memory limit
// from the file in MiB, rounded up (0 when the file sets none): mem_limit,
// else deploy.resources.limits.memory. It covers the same services as the
// returned names. It is a plain map because resources imports composespec.
// deployLimits are the services that declare deploy.resources.limits: compose
// then refuses a top-level cpus, mem_limit or pids_limit that differs from
// its counterpart there, so an override must set both forms on them, and
// must not add the block to the others.
func InspectLimits(ctx context.Context, src Source) (violations []Violation, services []string, fileMemoryMB map[string]int64, deployLimits map[string]bool) {
	project, vs, err := Load(ctx, src)
	if err != nil {
		return []Violation{{Rule: RuleComposeInvalid, Field: "compose", Message: invalidMessage(err)}}, nil, nil, nil
	}
	if project == nil {
		return vs, nil, nil, nil
	}
	vs = append(vs, Check(project, src.Root)...)
	if src.AppNetworks.Isolated {
		vs = append(vs, CheckAppNetworks(project, src.AppNetworks.Legacy)...)
	}
	services = sortedServiceNames(project)
	fileMemoryMB = make(map[string]int64, len(services))
	deployLimits = map[string]bool{}
	for _, name := range services {
		s, ok := project.Services[name]
		if !ok {
			s = project.DisabledServices[name]
		}
		hasDeploy := s.Deploy != nil && s.Deploy.Resources.Limits != nil
		if hasDeploy {
			deployLimits[name] = true
		}
		b := int64(s.MemLimit)
		if b == 0 && hasDeploy {
			b = int64(s.Deploy.Resources.Limits.MemoryBytes)
		}
		fileMemoryMB[name] = (b + (1<<20 - 1)) >> 20
	}
	return vs, services, fileMemoryMB, deployLimits
}

// invalidMessage is the message of a RuleComposeInvalid violation. It is
// not shortened here: compose-go errors quote interpolated values, which may
// be secrets, and a message cut inside one could not be redacted. The deploy
// policy redacts secrets and then cuts long messages. A ${VAR:?message}
// with no value gives the file's own message: a catalog template says
// there what is missing ("add a domain", D-147).
func invalidMessage(err error) string {
	var missing *template.MissingRequiredError
	if errors.As(err, &missing) && missing.Reason != "" {
		return missing.Reason
	}
	return "the compose file cannot be loaded: " + strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")
}

// includeOptionPaths are include options compose-go reads directly, not
// through a resource loader: env_file is read as a dotenv file for the
// included file's interpolation, and project_directory moves where the
// included file's relative paths (and its .env) resolve. Either could pull a
// host file (the panel's own .env on a single-server install) into the
// model, so they are refused while the file is interpolated, which happens
// for every file (included ones too) before its include entries are applied.
var includeOptionPaths = map[string]tree.Path{
	"env_file":          tree.NewPath("include", tree.PathMatchList, "env_file"),
	"env_file[]":        tree.NewPath("include", tree.PathMatchList, "env_file", tree.PathMatchList),
	"project_directory": tree.NewPath("include", tree.PathMatchList, "project_directory"),
}

// guardInterpolation returns compose-go's interpolation options with every
// variable lookup recorded in refs, without the "variable is not set" warning
// compose-go writes to the global logger (unset variables are normal when a
// file is checked before its env vars exist), and with include env_file and
// project_directory refused. The default type casts are kept; the shared
// default map is copied, not changed.
func guardInterpolation(in *interp.Options, refs *varRecorder, sandbox *sandboxLoader) *interp.Options {
	out := interp.Options{}
	if in != nil {
		out = *in
	}
	out.Substitute = func(tmpl string, mapping template.Mapping) (string, error) {
		return template.SubstituteWithOptions(tmpl, refs.wrap(mapping), template.WithoutLogging)
	}
	casts := make(map[tree.Path]interp.Cast, len(out.TypeCastMapping)+len(includeOptionPaths))
	for k, v := range out.TypeCastMapping {
		casts[k] = v
	}
	for _, name := range sortedKeys(includeOptionPaths) {
		option := strings.TrimSuffix(name, "[]")
		casts[includeOptionPaths[name]] = func(string) (interface{}, error) {
			return nil, sandbox.refuseIncludeOption(option)
		}
	}
	out.TypeCastMapping = casts
	return &out
}

// varRecorder records the name of every variable a compose file, or a .env
// file it is interpolated with, reads.
type varRecorder struct {
	names map[string]bool
}

func (r *varRecorder) wrap(mapping template.Mapping) template.Mapping {
	return func(name string) (string, bool) {
		r.names[name] = true
		return mapping(name)
	}
}

// platformViolations returns one violation per platform variable recorded
// (IsPlatformEnv). field prefixes the field ("" for the compose file); where
// names the file in the message.
func (r *varRecorder) platformViolations(field, where string) []Violation {
	var names []string
	for name := range r.names {
		if IsPlatformEnv(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	vs := make([]Violation, 0, len(names))
	for _, name := range names {
		f := "${" + name + "}"
		if field != "" {
			f = field + ": " + f
		}
		vs = append(vs, Violation{
			Rule:  RuleReservedEnv,
			Field: f,
			Message: fmt.Sprintf("%s reads %s, which is not one of the service's env vars: docker compose would take the platform's own value "+
				"when deploying, so the result cannot be checked. Use an env var of the service instead", where, name),
		})
	}
	return vs
}

// RunEnv are the only variables of the platform's own environment docker
// compose runs with, on every host (IsPlatformEnv). PATH finds docker, HOME
// its config and CLI plugins, DOCKER_HOST and XDG_RUNTIME_DIR the daemon
// (rootless).
var RunEnv = []string{"PATH", "HOME", "DOCKER_HOST", "XDG_RUNTIME_DIR"}

// IsPlatformEnv reports whether docker compose, as the orchestrators run it,
// reads name from the platform's own environment rather than from the
// service's env vars. The orchestrators give it only PATH, HOME, DOCKER_HOST
// and XDG_RUNTIME_DIR from the platform (a clean environment locally, env -i
// on remote servers) plus the service's env vars; the docker CLI adds
// DOCKER_CLI_PLUGIN_* and OTEL_RESOURCE_ATTRIBUTES when it starts the compose
// plugin. Every one of those is either a reserved CLI name (IsReservedCLIEnv)
// or OTEL_RESOURCE_ATTRIBUTES, which the docker CLI rewrites even when the
// service sets it. COMPOSE_PROJECT_NAME is set by compose itself, to the
// project name the check uses too, so reading it is fine.
func IsPlatformEnv(name string) bool {
	if name == consts.ComposeProjectName {
		return false
	}
	return IsReservedCLIEnv(name) || name == "OTEL_RESOURCE_ATTRIBUTES"
}

// readDotEnv reads <projectDir>/.env the way docker compose does, when it is
// a regular file inside root. Values in env win over it (they are the
// process environment). Variables its values read are recorded in refs, and
// reads of platform variables are violations.
func readDotEnv(root, projectDir string, env map[string]string, refs *varRecorder) (map[string]string, []Violation, error) {
	p := filepath.Join(projectDir, ".env")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: stat .env: %v", ErrInvalid, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil || !Within(root, resolved) {
			return nil, []Violation{{Rule: RuleExternalRef, Field: relField(root, p), Message: ".env is a symlink outside the repository"}}, nil
		}
	} else if !info.Mode().IsRegular() {
		return nil, nil, nil
	}
	content, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read .env: %v", ErrInvalid, err)
	}
	// As dotenv.GetEnvFromFile: env first, then the file's earlier lines.
	dotRefs := &varRecorder{names: map[string]bool{}}
	lookup := dotRefs.wrap(func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	vals, err := dotenv.ParseWithLookup(bytes.NewReader(content), dotenv.LookupFn(lookup))
	for name := range dotRefs.names {
		refs.names[name] = true
	}
	if vs := dotRefs.platformViolations(relField(root, p), relField(root, p)); len(vs) > 0 {
		return nil, vs, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read .env: %v", ErrInvalid, err)
	}
	var vs []Violation
	for _, k := range sortedKeys(vals) {
		if IsReservedCLIEnv(k) {
			vs = append(vs, Violation{
				Rule:    RuleReservedEnv,
				Field:   relField(root, p) + ": " + k,
				Message: fmt.Sprintf("%s is read by docker compose as its own setting and cannot be set in the project's .env", k),
			})
		}
	}
	if len(vs) > 0 {
		return nil, vs, nil
	}
	return vals, nil, nil
}

// sandboxLoader is the first compose-go resource loader. It refuses every
// include path and extends file that is absolute or has a ".." segment, and
// everything when the content is inline. Relative paths without ".." are
// left to compose-go's own loader: they resolve against a directory that is
// itself inside Root (the project directory, or the directory of a file
// loaded from inside Root), so every file loaded stays inside Root. ScanRepo
// rejects symlinks that would break that.
type sandboxLoader struct {
	root       string
	inline     bool
	violations []Violation
}

func (l *sandboxLoader) Accept(p string) bool {
	return l.inline || filepath.IsAbs(p) || hasDotDot(p)
}

func (l *sandboxLoader) Load(_ context.Context, p string) (string, error) {
	msg := fmt.Sprintf("%s is outside the project; include and extends may only name files below the compose file's directory, without ..", p)
	if l.inline {
		msg = fmt.Sprintf("an inline compose file cannot include or extend other files (%s)", p)
	}
	l.violations = append(l.violations, Violation{Rule: RuleExternalRef, Field: "include/extends", Message: msg})
	return "", fmt.Errorf("%s", msg)
}

func (l *sandboxLoader) Dir(p string) string {
	return filepath.Dir(p)
}

// refuseIncludeOption records an include env_file or project_directory and
// returns the error that stops the load.
func (l *sandboxLoader) refuseIncludeOption(option string) error {
	msg := "include." + option + " is not allowed; included files use their own directory and the service's environment"
	l.violations = append(l.violations, Violation{Rule: RuleExternalRef, Field: "include." + option, Message: msg})
	return errors.New(msg)
}

// reservedCLIEnvPrefixes and reservedCLIEnvNames are environment variables
// that the docker and compose CLIs, and the git and SSH tools they start,
// read as their own settings. A local compose deploy runs the CLI with the
// service's env vars in its environment, and compose reads COMPOSE_* from the
// project's .env, so these would let a service choose CLI plugins and
// credential helpers (DOCKER_CONFIG, HOME, PATH), extra env files
// (COMPOSE_ENV_FILES) or git commands (GIT_CONFIG_*, GIT_SSH_COMMAND).
var reservedCLIEnvPrefixes = []string{"DOCKER_", "COMPOSE_", "BUILDKIT_", "BUILDX_", "LD_", "XDG_", "SSH_", "GIT_CONFIG", "GIT_SSH"}

var reservedCLIEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true,
	"GIT_ASKPASS": true, "GIT_EXEC_PATH": true, "GIT_DIR": true, "GIT_WORK_TREE": true,
	"GIT_PROXY_COMMAND": true, "GIT_EXTERNAL_DIFF": true, "GIT_TEMPLATE_DIR": true,
	"GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
}

// IsReservedCLIEnv reports whether an environment variable name is one the
// docker or compose CLI would read as its own setting. Names are
// case-sensitive, as on Linux.
func IsReservedCLIEnv(key string) bool {
	if reservedCLIEnvNames[key] {
		return true
	}
	for _, p := range reservedCLIEnvPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
