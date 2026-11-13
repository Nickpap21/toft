package deploypolicy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/composespec"
	"github.com/Nickpap21/toft/backend/internal/envvar"
	"github.com/Nickpap21/toft/backend/internal/resources"
	"github.com/Nickpap21/toft/backend/pkg/listversion"
	"go.uber.org/zap"
)

// inlineRoot stands in for the temporary directory an inline compose file
// is written to at deploy time. It is never read.
const inlineRoot = "/nonexistent/toft-inline-compose"

// OperatorChecker reports whether a user is an instance operator. Satisfied
// by instance.Service; read from the database on every call.
type OperatorChecker interface {
	IsOperator(ctx context.Context, userID string) (bool, error)
}

// OwnershipChecker reports whether a service belongs to a workspace.
// Satisfied by service.ServiceManager.
type OwnershipChecker interface {
	CheckOwnership(ctx context.Context, id, orgID string) (bool, error)
}

// EnvProvider returns a service's env vars and secrets with their decrypted
// values and IsSecret set. Compose files are interpolated with the real
// values (so the check is exact), and secret values are then kept out of
// every violation the policy returns, shows or logs: secrets are write-once
// (D-17) and violations reach workspace viewers and deploy logs.
// Satisfied by envvar.Service.
type EnvProvider interface {
	ListAllDecrypted(ctx context.Context, serviceID string) ([]envvar.EnvVar, error)
}

// Service applies the deploy policy and manages exceptions.
type Service struct {
	repo      Repository
	mode      Mode
	operators OperatorChecker
	ownership OwnershipChecker
	env       EnvProvider
	log       *zap.Logger
	now       func() time.Time
	// controlPlaneStopgap mirrors deployment.Service's D-19 stopgap, for the
	// service view only; deployment.Service enforces it.
	controlPlaneStopgap bool
	// appNetworks is the compose network policy (TOFT_APP_NETWORKS).
	appNetworks composespec.AppNetworkPolicy
	// hashKey is the instance's HMAC key, for the fingerprints (D-126).
	hashKey []byte
}

// NewService creates a policy Service in the given mode.
func NewService(repo Repository, mode Mode, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	if mode != ModeWarn {
		mode = ModeEnforce
	}
	return &Service{repo: repo, mode: mode, log: log, now: time.Now}
}

// WithHashKey sets the instance's HMAC key, which keys the fingerprints.
func (s *Service) WithHashKey(key []byte) *Service { s.hashKey = key; return s }

// WithOperatorChecker enables exception management and the report. Without
// one, both are refused (fail closed).
func (s *Service) WithOperatorChecker(c OperatorChecker) *Service { s.operators = c; return s }

// WithOwnershipChecker lets workspace members read their services' policy.
func (s *Service) WithOwnershipChecker(c OwnershipChecker) *Service { s.ownership = c; return s }

// WithEnvProvider lets saved compose files be interpolated with the
// service's env vars (save time, the service view and the report), and tells
// every check which values are secrets to redact. Without one, a deploy check
// redacts every env value (fail closed).
func (s *Service) WithEnvProvider(p EnvProvider) *Service { s.env = p; return s }

// WithControlPlaneStopgap tells the service view whether deploys to the
// control-plane server are limited to operators (D-19 stopgap), so it can say
// so for services with no server.
func (s *Service) WithControlPlaneStopgap(enabled bool) *Service {
	s.controlPlaneStopgap = enabled
	return s
}

// WithAppNetworks sets the compose network policy: under isolated app
// networks, compose files may only use their own networks and the control
// plane's network name, which the deploy points at the environment's
// network (composespec.CheckAppNetworks).
func (s *Service) WithAppNetworks(p composespec.AppNetworkPolicy) *Service {
	s.appNetworks = p
	return s
}

// InlineComposeSource is the composespec.Source an inline compose file is
// checked with, and that the deploy re-checks its network rewrite with.
func InlineComposeSource(serviceID, serviceName, content string, env map[string]string) composespec.Source {
	return composespec.Source{
		Root:        inlineRoot,
		Content:     content,
		ProjectName: ComposeProjectName(serviceID, serviceName),
		Env:         env,
	}
}

// RepoComposeSource is the composespec.Source a compose file from a cloned
// repository is checked with (see CheckComposeRepo).
func RepoComposeSource(in RepoCheck) composespec.Source {
	file := in.ComposeFilePath
	if file == "" {
		file = "docker-compose.yml"
	}
	values, _ := splitEnv(in.EnvVars)
	projectDir := in.RepoDir
	if !in.Local {
		projectDir = filepath.Join(in.RepoDir, filepath.FromSlash(path.Dir(filepath.ToSlash(file))))
	}
	return composespec.Source{
		Root:        in.RepoDir,
		ProjectDir:  projectDir,
		File:        file,
		ProjectName: ComposeProjectName(in.ServiceID, in.ServiceName),
		Env:         values,
		ReadDotEnv:  in.Local,
	}
}

// Mode returns the enforcement mode.
func (s *Service) Mode() Mode { return s.mode }

// ─── Checks ──────────────────────────────────────────────────────────────────

// subject is everything one evaluation looks at.
type subject struct {
	serviceID   string
	serviceName string
	sourceType  string
	// image is what a single-container service runs (ServiceImage).
	image string
	// composeContent is the inline compose file; composeRepo means the file
	// comes from a repository and is only known at deploy time.
	composeContent string
	composeRepo    bool
	envValues      map[string]string
	envKeys        []string
	// secrets are values that must not appear in any violation.
	secrets     []string
	volumes     []VolumeSpec
	preDeploy   []string
	configPaths []string
	// saveTime: a compose file that cannot be loaded is not reported, because
	// its env vars may not exist yet. The deploy-time check reports it.
	saveTime bool
	// memory is the D-41 memory_limit input; nil = not checked.
	memory *resources.MemoryCheck
}

// evaluation is the outcome of evaluate, before exceptions and the mode.
type evaluation struct {
	violations []Violation
	warnings   []string
	// atDeploy: part of the configuration (a compose file from a repository)
	// can only be checked at deploy time.
	atDeploy bool
	// services are the compose file's services, when it was loaded, fileMB
	// their memory limits in the file, and deployLimits those that declare
	// deploy.resources.limits.
	services     []string
	fileMB       map[string]int64
	deployLimits map[string]bool
}

// withFileLimits makes the services checked for memory the compose file's
// own, when its limits are known (fileMB non-nil): the file's limit from
// fileMB and the panel's from m.ComposeServices. Panel entries for names the
// file lacks are dropped. When the limits are not known, m is unchanged.
func withFileLimits(m *resources.MemoryCheck, fileMB map[string]int64) *resources.MemoryCheck {
	if m == nil || !m.Compose || fileMB == nil {
		return m
	}
	out := *m
	out.ComposeServices = make(map[string]resources.ComposeLimitSource, len(fileMB))
	for name, mb := range fileMB {
		out.ComposeServices[name] = resources.ComposeLimitSource{FileMB: mb, PanelMB: m.ComposeServices[name].PanelMB}
	}
	return &out
}

// evaluate returns the violations in sub, with secret values redacted.
func (s *Service) evaluate(ctx context.Context, sub subject) evaluation {
	spec := ServiceSpec{PreDeployCommands: sub.preDeploy, ConfigFilePaths: sub.configPaths}
	if sub.sourceType != SourceCompose {
		spec.Volumes = sub.volumes
	}
	ev := evaluation{violations: CheckServiceSpec(spec)}

	if sub.sourceType == SourceCompose {
		ev.violations = append(ev.violations, checkComposeEnv(sub.envKeys)...)
		switch {
		case sub.composeRepo:
			ev.atDeploy = true
		case strings.TrimSpace(sub.composeContent) != "":
			src := InlineComposeSource(sub.serviceID, sub.serviceName, sub.composeContent, sub.envValues)
			src.AppNetworks = s.appNetworks
			cvs, services, fileMB, deployLimits := composespec.InspectLimits(ctx, src)
			ev.services, ev.fileMB, ev.deployLimits = services, fileMB, deployLimits
			sub.memory = withFileLimits(sub.memory, fileMB)
			for _, v := range cvs {
				if sub.saveTime && v.Rule == composespec.RuleComposeInvalid {
					continue
				}
				ev.violations = append(ev.violations, v)
			}
		}
	} else {
		ev.warnings = invalidKeyWarnings(sub.envKeys)
	}
	if sub.memory != nil {
		vs, ws := resources.CheckMemory(*sub.memory, s.now())
		ev.violations = append(ev.violations, vs...)
		ev.warnings = append(ev.warnings, ws...)
	}
	ev.violations = finish(ev.violations, sub.secrets, sub.image, s.hashKey)
	return ev
}

// listPosition is a list index in a field: services.web.volumes[2].
var listPosition = regexp.MustCompile(`\[\d+\]`)

// fingerprint identifies a violation for an exception's values (K3): its
// rule, compose service, field without list positions (reordering a list
// changes nothing), value and image. Messages don't count, so rewording one
// never invalidates an exception. With key it is an HMAC, so a stored one
// can't be checked against a guessed secret (D-126).
func fingerprint(v Violation, key []byte) string {
	h := sha256.New()
	if key != nil {
		h = hmac.New(sha256.New, key)
	}
	for _, part := range []string{string(v.Rule), v.Service, listPosition.ReplaceAllString(v.Field, "[]"), v.Value, v.Image} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// valuesDigest names the set of values vs would have a grant approve: the
// fingerprints, sorted, hashed again so none leaves the server.
func valuesDigest(vs []Violation) string {
	fps := make([]string, 0, len(vs))
	for _, v := range vs {
		if v.Fingerprint != "" {
			fps = append(fps, v.Fingerprint)
		}
	}
	sort.Strings(fps)
	fps = slices.Compact(fps)
	sum := sha256.Sum256([]byte(strings.Join(fps, "\n")))
	return hex.EncodeToString(sum[:])
}

// finish gives violations without an image the service's, fingerprints
// those of grantable rules, then redacts secrets: a changed secret that
// changes a host path is a changed value.
//
// The fingerprint is keyed with the instance's HMAC key; AltFingerprint is
// the plain one releases before v0.28.0 pinned, matched and then rewritten
// by repin (D-126).
func finish(vs []Violation, secrets []string, image string, hashKey []byte) []Violation {
	for i := range vs {
		if vs[i].Image == "" {
			vs[i].Image = image
		}
		if composespec.Grantable(vs[i].Rule) && !byRuleOnly[vs[i].Rule] {
			vs[i].Fingerprint = fingerprint(vs[i], hashKey)
			if hashKey != nil {
				vs[i].AltFingerprint = fingerprint(vs[i], nil)
			}
		}
	}
	return redactSecrets(vs, secrets)
}

// decide applies an exception and the mode. byValue: a granted rule covers
// only the values the exception approved (deploys, and the views that show
// what a deploy would do); otherwise any value (saves, so the new value can
// be seen and approved).
func (s *Service) decide(ev evaluation, exc *Exception, byValue bool) Decision {
	active, covered := []Violation{}, []Violation{}
	for _, v := range ev.violations {
		v.State = exc.state(v)
		switch {
		case v.State == StatePinned || v.State == StateGranted || v.State == StatePending && !byValue:
			covered = append(covered, v)
		case v.State == StatePending:
			v.Message += "; this differs from what the exception approved on " + exc.UpdatedAt.UTC().Format("2006-01-02") +
				", and an instance operator can approve it in the service's Settings → Deploy policy"
			active = append(active, v)
		default:
			active = append(active, v)
		}
	}
	return Decision{
		Mode:                s.mode,
		Violations:          active,
		Allowed:             covered,
		Warnings:            ev.warnings,
		Blocked:             s.mode == ModeEnforce && len(active) > 0,
		ComposeServices:     ev.services,
		ComposeMemoryMB:     ev.fileMB,
		ComposeDeployLimits: ev.deployLimits,
	}
}

// decideDeploy is decide for a deploy: in warn mode it still refuses a
// compose service's own traefik.* labels (SelfRouting).
func (s *Service) decideDeploy(ev evaluation, exc *Exception) Decision {
	d := s.decide(ev, exc, true)
	for _, v := range d.Violations {
		if SelfRouting(v) {
			d.Blocked = true
		}
	}
	return d
}

// storedEnv reads a service's env vars and secrets: the values compose files
// are interpolated with, their keys in order, and the secret values.
func (s *Service) storedEnv(ctx context.Context, serviceID string) (values map[string]string, keys, secrets []string, err error) {
	if s.env == nil || serviceID == "" {
		return nil, nil, nil, nil
	}
	vars, err := s.env.ListAllDecrypted(ctx, serviceID)
	if err != nil {
		return nil, nil, nil, err
	}
	values = make(map[string]string, len(vars))
	for _, v := range vars {
		if _, dup := values[v.Key]; !dup {
			keys = append(keys, v.Key)
		}
		values[v.Key] = v.Value
		if v.IsSecret && v.Value != "" {
			secrets = append(secrets, v.Value)
		}
	}
	return values, keys, secrets, nil
}

// deploySecrets returns the values a deploy check must redact: the service's
// secrets, as stored and as the deploy passes them. When they cannot be read,
// every env value of the deploy is treated as a secret (fail closed).
func (s *Service) deploySecrets(ctx context.Context, serviceID string, deployValues map[string]string) []string {
	if s.env != nil && serviceID != "" {
		vars, err := s.env.ListAllDecrypted(ctx, serviceID)
		if err == nil {
			var out []string
			for _, v := range vars {
				if !v.IsSecret {
					continue
				}
				out = append(out, v.Value, deployValues[v.Key])
			}
			return out
		}
		s.log.Warn("read secrets for the deploy policy; redacting every env value", zap.String("service_id", serviceID), zap.Error(err))
	}
	out := make([]string, 0, len(deployValues))
	for _, v := range deployValues {
		out = append(out, v)
	}
	return out
}

// exceptionFor loads a service's exception. A read error is logged and
// treated as no exception, so a database problem never loosens the policy.
func (s *Service) exceptionFor(ctx context.Context, serviceID string) *Exception {
	if serviceID == "" {
		return nil
	}
	e, err := s.repo.GetException(ctx, serviceID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			s.log.Warn("read deploy policy exception; checking without it", zap.String("service_id", serviceID), zap.Error(err))
		}
		return nil
	}
	return e
}

// CheckDeploy checks a deploy before it is created: the service's volumes,
// pre-deploy commands, env var names and inline compose file, interpolated
// with the env vars the deploy will use. A compose file from a repository is
// checked later by CheckComposeRepo.
func (s *Service) CheckDeploy(ctx context.Context, in DeployCheck) Decision {
	values, keys := splitEnv(in.EnvVars)
	ev := s.evaluate(ctx, subject{
		serviceID:      in.ServiceID,
		serviceName:    in.ServiceName,
		sourceType:     in.SourceType,
		image:          in.Image,
		composeContent: in.ComposeContent,
		composeRepo:    in.ComposeFromRepo,
		envValues:      values,
		envKeys:        keys,
		secrets:        s.deploySecrets(ctx, in.ServiceID, values),
		volumes:        in.Volumes,
		preDeploy:      in.PreDeployCommands,
		memory:         in.Memory,
	})
	exc := s.exceptionFor(ctx, in.ServiceID)
	s.repin(ctx, in.ServiceID, exc, ev.violations)
	return s.decideDeploy(ev, exc)
}

// repin rewrites the pins a deploy matched by their plain fingerprint to
// the keyed one (D-126), so no deploy is refused for the change. A failure
// is logged: the plain pin keeps matching.
func (s *Service) repin(ctx context.Context, serviceID string, exc *Exception, vs []Violation) {
	if exc == nil {
		return
	}
	var old, keyed []string
	for _, v := range vs {
		if v.AltFingerprint == "" || slices.Contains(old, v.AltFingerprint) {
			continue
		}
		for _, p := range exc.AllowedValues {
			if p.Fingerprint == v.AltFingerprint {
				old, keyed = append(old, v.AltFingerprint), append(keyed, v.Fingerprint)
				break
			}
		}
	}
	if len(old) == 0 {
		return
	}
	if err := s.repo.RepinValues(ctx, serviceID, old, keyed); err != nil {
		s.log.Warn("rewrite the deploy policy's pinned values", zap.String("service_id", serviceID), zap.Error(err))
	}
}

// CheckComposeRepo checks a cloned infra repository and its compose file,
// exactly as docker compose will read them. For a remote deploy (Local
// false) the clone must already hold the .env file the deploy writes
// (composespec.StageDotEnv): the compose file is interpolated with the env
// vars alone, and files it includes from its directory read that .env.
func (s *Service) CheckComposeRepo(ctx context.Context, in RepoCheck) Decision {
	values, _ := splitEnv(in.EnvVars)
	scan, err := composespec.ScanRepo(in.RepoDir)
	ev := evaluation{violations: scan}
	if err != nil {
		ev.violations = append(ev.violations, Violation{Rule: composespec.RuleComposeInvalid, Field: "repository",
			Message: "the repository could not be checked: " + strings.ReplaceAll(err.Error(), in.RepoDir, ".")})
	}
	if len(ev.violations) == 0 {
		src := RepoComposeSource(in)
		src.AppNetworks = s.appNetworks
		ev.violations, ev.services, ev.fileMB, ev.deployLimits = composespec.InspectLimits(ctx, src)
		in.Memory = withFileLimits(in.Memory, ev.fileMB)
	}
	if in.Memory != nil {
		vs, ws := resources.CheckMemory(*in.Memory, s.now())
		ev.violations = append(ev.violations, vs...)
		ev.warnings = append(ev.warnings, ws...)
	}
	ev.violations = finish(ev.violations, s.deploySecrets(ctx, in.ServiceID, values), "", s.hashKey)
	s.saveCheck(ctx, in, ev.violations)
	exc := s.exceptionFor(ctx, in.ServiceID)
	s.repin(ctx, in.ServiceID, exc, ev.violations)
	return s.decideDeploy(ev, exc)
}

// saveCheck keeps the check's violations of grantable rules, for an
// operator to approve (the file is only read at deploy). A failure is
// logged: the deploy goes on, checked as always.
func (s *Service) saveCheck(ctx context.Context, in RepoCheck, vs []Violation) {
	if in.ServiceID == "" {
		return
	}
	values := []PinnedValue{}
	for _, v := range vs {
		if v.Fingerprint != "" {
			values = append(values, pinned(v))
		}
	}
	if err := s.repo.SaveLastCheck(ctx, in.ServiceID, in.CommitSHA, values); err != nil {
		s.log.Warn("record the deploy policy check", zap.String("service_id", in.ServiceID), zap.Error(err))
	}
}

// SaveCheck is a service configuration about to be saved.
type SaveCheck struct {
	// ServiceID is empty when the service is being created.
	ServiceID         string
	ServiceName       string
	SourceType        string
	ComposeContent    string
	ComposeRepoURL    string
	Volumes           []VolumeSpec
	PreDeployCommands string
	// Memory is the memory_limit input (D-41); nil = not checked.
	Memory *resources.MemoryCheck
}

// CheckSave checks a configuration before it is saved, for fast feedback.
// Compose files are interpolated with the service's current env vars; a file
// that cannot be loaded yet is left to the deploy-time check.
func (s *Service) CheckSave(ctx context.Context, in SaveCheck) Decision {
	values, keys, secrets, err := s.storedEnv(ctx, in.ServiceID)
	if err != nil {
		// Checked without env vars (the deploy check is authoritative); a
		// file that cannot be loaded without them is not reported.
		s.log.Warn("read env vars for the save-time policy check", zap.String("service_id", in.ServiceID), zap.Error(err))
	}
	ev := s.evaluate(ctx, subject{
		serviceID:      in.ServiceID,
		serviceName:    in.ServiceName,
		sourceType:     in.SourceType,
		composeContent: in.ComposeContent,
		composeRepo:    in.ComposeRepoURL != "",
		envValues:      values,
		envKeys:        keys,
		secrets:        secrets,
		volumes:        in.Volumes,
		preDeploy:      SplitLines(in.PreDeployCommands),
		saveTime:       true,
		memory:         in.Memory,
	})
	return s.decide(ev, s.exceptionFor(ctx, in.ServiceID), false)
}

// ─── Views ───────────────────────────────────────────────────────────────────

func (s *Service) isOperator(ctx context.Context, userID string) (bool, error) {
	if s.operators == nil {
		return false, nil
	}
	return s.operators.IsOperator(ctx, userID)
}

// RequireOperator is ErrNotOperator unless actorID is an instance operator.
func (s *Service) RequireOperator(ctx context.Context, actorID string) error {
	ok, err := s.isOperator(ctx, actorID)
	if err != nil {
		return fmt.Errorf("check operator: %w", err)
	}
	if !ok {
		return ErrNotOperator
	}
	return nil
}

// stored evaluates a stored service as its next deploy would see it.
func (s *Service) stored(ctx context.Context, svc *StoredService, keys, configPaths []string, exc *Exception) (Decision, bool) {
	ev := s.storedEval(ctx, svc, keys, configPaths)
	return s.decide(ev, exc, true), ev.atDeploy
}

func (s *Service) storedEval(ctx context.Context, svc *StoredService, keys, configPaths []string) evaluation {
	var (
		values  map[string]string
		secrets []string
	)
	if svc.SourceType == SourceCompose && svc.ComposeRepoURL == "" {
		var err error
		if values, _, secrets, err = s.storedEnv(ctx, svc.ID); err != nil {
			s.log.Warn("read env vars for policy check", zap.String("service_id", svc.ID), zap.Error(err))
		}
		// The deploy adds TOFT_URL (deployment.withServiceURL); so does the
		// view, which checks that it is set, not its scheme.
		if u := ServiceURL(svc.Hostnames, nil); u != "" && values["TOFT_URL"] == "" {
			if values == nil {
				values = map[string]string{}
			}
			values["TOFT_URL"] = u
		}
	}
	return s.evaluate(ctx, subject{
		serviceID:      svc.ID,
		serviceName:    svc.Name,
		sourceType:     svc.SourceType,
		image:          ServiceImage(svc.SourceType, svc.DockerImage, svc.GitRepoURL, svc.RepositoryID),
		composeContent: svc.ComposeContent,
		composeRepo:    svc.ComposeRepoURL != "",
		envValues:      values,
		envKeys:        keys,
		secrets:        secrets,
		volumes:        svc.Volumes,
		preDeploy:      SplitLines(svc.PreDeployCommands),
		configPaths:    configPaths,
	})
}

// AllowsControlPlane reports whether an instance operator granted the
// service the control_plane exception, which lifts the D-19 stopgap for it
// (deployment.Service). A read error counts as no (fail closed).
func (s *Service) AllowsControlPlane(ctx context.Context, serviceID string) bool {
	return s.exceptionFor(ctx, serviceID).allowed()[composespec.RuleControlPlane]
}

// AllowsNetRaw reports whether an operator granted the service the net_raw
// exception, which keeps raw sockets in its containers (D-93).
func (s *Service) AllowsNetRaw(ctx context.Context, serviceID string) bool {
	return s.exceptionFor(ctx, serviceID).allowed()[composespec.RuleNetRaw]
}

// ServicePolicy returns a service's policy status: what its stored
// configuration would be refused for on the next deploy, and its exception.
// Members of the service's workspace and instance operators may read it.
func (s *Service) ServicePolicy(ctx context.Context, actorID, orgID, serviceID string) (*ServicePolicy, error) {
	isOp, err := s.isOperator(ctx, actorID)
	if err != nil {
		s.log.Warn("operator check failed", zap.String("user_id", actorID), zap.Error(err))
		isOp = false
	}
	if !isOp {
		owned := false
		if s.ownership != nil && orgID != "" {
			owned, err = s.ownership.CheckOwnership(ctx, serviceID, orgID)
			if err != nil {
				owned = false
			}
		}
		if !owned {
			return nil, ErrServiceNotFound
		}
	}

	svc, err := s.repo.GetStoredService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	envKeys, err := s.repo.ListEnvKeys(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list env keys: %w", err)
	}
	paths, err := s.repo.ListConfigFilePaths(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list config files: %w", err)
	}
	exc, err := s.repo.GetException(ctx, serviceID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("read the exception: %w", err)
	}
	d, atDeploy := s.stored(ctx, svc, envKeys[serviceID], paths[serviceID], exc)
	var last *LastCheck
	if atDeploy {
		if last, err = s.repo.GetLastCheck(ctx, serviceID); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("read the last deploy check: %w", err)
		}
	}

	invalid := invalidKeys(envKeys[serviceID])
	if invalid == nil {
		invalid = []string{}
	}
	return &ServicePolicy{
		version:                versionOf(exc),
		ServiceID:              serviceID,
		Mode:                   s.mode,
		Violations:             d.Violations,
		Allowed:                d.Allowed,
		Exception:              toExceptionResponse(exc),
		ComposeCheckedAtDeploy: atDeploy,
		InvalidEnvKeys:         invalid,
		Rules:                  composespec.Rules(),
		CanManageExceptions:    isOp,
		ControlPlaneRestricted: s.controlPlaneStopgap && svc.ServerID == "" && !exc.allowed()[composespec.RuleControlPlane],
		ComposeServices:        d.ComposeServices,
		LastDeployCheck:        last.view(exc),
		ValuesDigest:           valuesDigest(withLastCheck(append(append([]Violation{}, d.Violations...), d.Allowed...), last)),
	}, nil
}

// withLastCheck adds what the last deploy found to vs.
func withLastCheck(vs []Violation, last *LastCheck) []Violation {
	if last != nil {
		for _, p := range last.Values {
			vs = append(vs, p.violation())
		}
	}
	return vs
}

// Report lists every service on the instance whose stored configuration
// violates the policy, is covered by an exception, has env var names to fix,
// or has a compose file from a repository (checked at deploy time).
// Operator-only.
func (s *Service) Report(ctx context.Context, actorID string) (*Report, error) {
	if err := s.RequireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	services, err := s.repo.ListStoredServices(ctx)
	if err != nil {
		return nil, err
	}
	exceptions, err := s.repo.ListExceptions(ctx)
	if err != nil {
		return nil, err
	}
	envKeys, err := s.repo.ListEnvKeys(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list env keys: %w", err)
	}
	paths, err := s.repo.ListConfigFilePaths(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list config files: %w", err)
	}
	checks, err := s.repo.ListLastChecks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list deploy checks: %w", err)
	}

	report := &Report{Mode: s.mode, GeneratedAt: s.now().UTC(), Services: []ReportEntry{}, Rules: composespec.Rules(), ServicesChecked: len(services)}
	servers := map[string]bool{}
	for i := range services {
		svc := &services[i]
		servers[svc.ServerID] = true
		exc := exceptions[svc.ID]
		d, atDeploy := s.stored(ctx, svc, envKeys[svc.ID], paths[svc.ID], exc)
		invalid := invalidKeys(envKeys[svc.ID])
		if len(d.Violations) == 0 && len(d.Allowed) == 0 && !atDeploy && len(invalid) == 0 && exc == nil {
			continue
		}
		var (
			rules     []Rule
			reason    string
			grantedBy string
			grantedAt *time.Time
		)
		if exc != nil {
			rules, reason = exc.AllowedRules, exc.Reason
			grantedBy = exc.GrantedByName
			if grantedBy == "" {
				grantedBy = exc.GrantedByEmail
			}
			at := exc.UpdatedAt
			grantedAt = &at
		}
		if rules == nil {
			rules = []Rule{}
		}
		if invalid == nil {
			invalid = []string{}
		}
		report.Services = append(report.Services, ReportEntry{
			ServiceID:              svc.ID,
			ServiceName:            svc.Name,
			OrgID:                  svc.OrgID,
			OrgName:                svc.OrgName,
			ProjectID:              svc.ProjectID,
			ProjectName:            svc.ProjectName,
			EnvironmentName:        svc.EnvironmentName,
			ServerID:               svc.ServerID,
			ServerName:             svc.ServerName,
			SourceType:             svc.SourceType,
			Violations:             d.Violations,
			Allowed:                d.Allowed,
			AllowedRules:           rules,
			ExceptionReason:        reason,
			ExceptionGrantedBy:     grantedBy,
			ExceptionGrantedAt:     grantedAt,
			ComposeCheckedAtDeploy: atDeploy,
			InvalidEnvKeys:         invalid,
			AllowedValues:          exc.values(),
		})
		if atDeploy {
			report.Services[len(report.Services)-1].LastDeployCheck = checks[svc.ID].view(exc)
		}
	}
	report.ServersChecked = len(servers)
	return report, nil
}

// ─── Exceptions ──────────────────────────────────────────────────────────────

// normalizeRules validates requested rules: at least one, each known and
// grantable. Returns them deduplicated and sorted.
func normalizeRules(requested []string) ([]Rule, error) {
	seen := map[Rule]bool{}
	var bad []string
	for _, r := range requested {
		rule := Rule(strings.TrimSpace(r))
		switch {
		case !composespec.Known(rule):
			bad = append(bad, fmt.Sprintf("%q is not a rule", r))
		case !composespec.Grantable(rule):
			bad = append(bad, fmt.Sprintf("%q can never be granted", r))
		default:
			seen[rule] = true
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrInvalidRules, strings.Join(bad, "; "))
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("%w: choose at least one rule", ErrInvalidRules)
	}
	out := make([]Rule, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// GrantException creates or replaces a service's exception: the listed
// grantable rules no longer block its deploys. It approves the service's
// values for them now, if digest still names them (ServicePolicy's
// values_digest), and keeps the values it approved before for rules it
// still grants, so a rollback to one stays approved. The change is written
// to the service workspace's audit log with {allowed_rules, allowed_values,
// reason} in the same transaction. version is the exception's as it was
// read (listversion.ErrRequired, listversion.ErrChanged): a grant replaces
// the rules, so one made on a stale read would drop what it didn't see.
// Instance operators only; their authority is instance-wide, so the service
// need not be in their active workspace.
func (s *Service) GrantException(ctx context.Context, actorID, serviceID string, rules []string, reason, digest, version string) (*ExceptionResponse, error) {
	if err := s.RequireOperator(ctx, actorID); err != nil {
		return nil, err
	}
	normalized, err := normalizeRules(rules)
	if err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ErrReasonRequired
	}
	if utf8.RuneCountInString(reason) > MaxReasonLength {
		return nil, fmt.Errorf("%w: keep it under %d characters", ErrReasonRequired, MaxReasonLength)
	}
	if version == "" {
		return nil, listversion.ErrRequired
	}
	vs, err := s.currentValues(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	if valuesDigest(vs) != digest {
		return nil, ErrValuesChanged
	}
	values := approve(vs, s.exceptionFor(ctx, serviceID), normalized)
	e, err := s.repo.GrantException(ctx, Grant{ServiceID: serviceID, AllowedRules: normalized, AllowedValues: values, Reason: reason, ActorID: actorID, Version: version})
	if err != nil {
		return nil, err
	}
	s.log.Info("deploy policy exception granted",
		zap.String("service_id", serviceID), zap.String("actor_id", actorID), zap.Any("allowed_rules", normalized), zap.Int("allowed_values", len(values)))
	return toExceptionResponse(e), nil
}

// currentValues are the violations the service has now, which a grant of
// their rules approves (K3): its saved configuration, checked as the
// service view checks it, and for a compose file from a repository what
// its last deploy found.
func (s *Service) currentValues(ctx context.Context, serviceID string) ([]Violation, error) {
	svc, err := s.repo.GetStoredService(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	envKeys, err := s.repo.ListEnvKeys(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list env keys: %w", err)
	}
	paths, err := s.repo.ListConfigFilePaths(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list config files: %w", err)
	}
	ev := s.storedEval(ctx, svc, envKeys[serviceID], paths[serviceID])
	if !ev.atDeploy {
		return ev.violations, nil
	}
	last, err := s.repo.GetLastCheck(ctx, serviceID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("read the last deploy check: %w", err)
	}
	return withLastCheck(ev.violations, last), nil
}

// approve is what a grant of rules pins: the values of vs for those rules,
// then those prev approved for them that vs no longer has.
func approve(vs []Violation, prev *Exception, rules []Rule) []PinnedValue {
	granted := map[Rule]bool{}
	for _, r := range rules {
		granted[r] = true
	}
	values, seen := []PinnedValue{}, map[string]bool{}
	add := func(p PinnedValue) {
		if granted[p.Rule] && p.Fingerprint != "" && !seen[p.Fingerprint] {
			seen[p.Fingerprint] = true
			values = append(values, p)
		}
	}
	for _, v := range vs {
		add(pinned(v))
	}
	for _, p := range prev.values() {
		add(p)
	}
	return values
}

// RevokeException removes a service's exception, audited with the rules it
// held and the given reason. Instance operators only.
func (s *Service) RevokeException(ctx context.Context, actorID, serviceID, reason string) error {
	if err := s.RequireOperator(ctx, actorID); err != nil {
		return err
	}
	reason = truncateRunes(strings.TrimSpace(reason), MaxReasonLength)
	e, err := s.repo.RevokeException(ctx, serviceID, actorID, reason)
	if err != nil {
		return err
	}
	s.log.Info("deploy policy exception revoked",
		zap.String("service_id", serviceID), zap.String("actor_id", actorID), zap.Any("allowed_rules", e.AllowedRules))
	return nil
}

// truncateRunes cuts s to at most n characters, never inside a UTF-8
// sequence.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// redactedSecret replaces secret values in violations.
const redactedSecret = "[secret]"

// maxMessageRunes caps a violation message (compose-go load errors can be
// long). Messages are cut after secrets are redacted, so a cut never leaves
// part of a secret that no longer matches.
const maxMessageRunes = 500

// redactSecrets replaces every secret value in the violations' fields and
// messages, then cuts long messages to maxMessageRunes. Compose interpolates
// secrets into the file, and messages quote the offending value (a host
// path, a capability, a value compose-go cannot cast), as is or escaped
// (secretForms). Longer forms are replaced first.
func redactSecrets(vs []Violation, secrets []string) []Violation {
	if len(vs) == 0 {
		return vs
	}
	needles := secretForms(secrets)
	redact := func(s string) string {
		if len(needles) == 0 {
			return s
		}
		for _, n := range needles {
			s = strings.ReplaceAll(s, n, "\x00")
		}
		return strings.ReplaceAll(s, "\x00", redactedSecret)
	}
	out := make([]Violation, len(vs))
	for i, v := range vs {
		v.Field = redact(v.Field)
		v.Message = redact(v.Message)
		v.Value = redact(v.Value)
		v.Image = redact(v.Image)
		if utf8.RuneCountInString(v.Message) > maxMessageRunes {
			v.Message = truncateRunes(v.Message, maxMessageRunes) + "…"
		}
		out[i] = v
	}
	return out
}

// secretForms returns every form in which a secret can appear in a
// violation, longest first: as is, trimmed, and path-cleaned (host paths are
// cleaned before they are shown), and each of those escaped as Go's %q
// (strconv.Quote) and %+q (strconv.QuoteToASCII) and as a JSON string would
// escape it, without the surrounding quotes. A secret with a quote, a
// backslash, a control character or a non-ASCII character differs in those
// escaped forms, which compose-go errors and checker messages use.
func secretForms(secrets []string) []string {
	set := map[string]bool{}
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		bases := []string{sec}
		for _, variant := range []string{strings.TrimSpace(sec), path.Clean(sec), path.Clean("/" + sec)} {
			if variant != "" && variant != "." && variant != "/" {
				bases = append(bases, variant)
			}
		}
		for _, b := range bases {
			set[b] = true
			for _, quoted := range []string{strconv.Quote(b), strconv.QuoteToASCII(b), jsonQuote(b, true), jsonQuote(b, false)} {
				if inner := strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`); inner != "" {
					set[inner] = true
				}
			}
		}
	}
	needles := make([]string, 0, len(set))
	for n := range set {
		needles = append(needles, n)
	}
	sort.Slice(needles, func(i, j int) bool {
		if len(needles[i]) != len(needles[j]) {
			return len(needles[i]) > len(needles[j])
		}
		return needles[i] < needles[j]
	})
	return needles
}

// jsonQuote returns s as a JSON string, with or without HTML escaping.
func jsonQuote(s string, escapeHTML bool) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(escapeHTML)
	if err := enc.Encode(s); err != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}
