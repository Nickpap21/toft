package deployment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"sync"

	"github.com/Nickpap21/toft/backend/internal/service"
	"go.uber.org/zap"
)

// WithHashKey keys the settings fingerprints deploys record (D-159) with
// the instance's HMAC key. Without one no fingerprint is recorded, and Toft
// never redeploys a service for its certificate.
func (s *Service) WithHashKey(key []byte) { s.hashKey = key }

// fingerprintDomain keeps the fingerprint's HMAC apart from the instance
// key's other uses, and names its format.
const fingerprintDomain = "toft:settings-fingerprint:v1\x00"

// shipped is what a deploy ships of its service's settings (D-159), part by
// part: every field of the service but its state, how it is shown and the
// settings of deploys rather than of what runs; the variables it is given
// before the deploy adds TOFT_URL; its config files; a container's shared
// volumes and Loki, a stack's compose limits; its runtime security. Not its
// routing (domains, and which have a certificate), nor its network plan,
// which is the platform's topology recomputed at every deploy from the
// service's dependencies, alias and environment, which are in it.
//
// Trigger reads every part; the pipeline reads a part again where it uses
// it (check), and a part that changed in between means the deploy shipped
// something its parts don't say: it records no fingerprint.
type shipped struct {
	parts map[string]string
	mu    sync.Mutex
	drift bool
}

// check is a part as the pipeline reads it to use it.
func (p *shipped) check(part, now string) {
	if p == nil {
		return
	}
	if p.parts[part] != now {
		p.mu.Lock()
		p.drift = true
		p.mu.Unlock()
	}
}

func (p *shipped) sum(key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(fingerprintDomain))
	for _, k := range slices.Sorted(maps.Keys(p.parts)) {
		fmt.Fprintf(m, "%s\x00%s\x01", k, p.parts[k])
	}
	return hex.EncodeToString(m.Sum(nil))
}

// readShipped reads every part a deploy of svc to serverID with envVars
// ships.
func (s *Service) readShipped(ctx context.Context, svc *service.Service, orgID, serverID string, envVars []string) (*shipped, error) {
	settings := shippedFields(svc, serverID)
	p := &shipped{parts: map[string]string{
		"settings": fmt.Sprintf("%+v", settings),
		"vars":     fmt.Sprintf("%q", slices.Sorted(slices.Values(envVars))),
	}}
	var err error
	if p.parts["config_files"], err = s.configFilesPart(ctx, svc.ID); err != nil {
		return nil, err
	}
	sec, err := s.securityFor(ctx, svc.ID)
	if err != nil {
		return nil, err
	}
	p.parts["security"] = fmt.Sprintf("%+v", sec)
	if svc.SourceType == service.SourceTypeDockerCompose {
		limits, err := s.composeLimits(ctx, svc.ID, nil)
		if err != nil {
			return nil, err
		}
		p.parts["compose_limits"] = panelLimits(limits)
		return p, nil
	}
	if s.sharedMounts != nil {
		mounts, err := s.sharedMounts.MountsForDeploy(ctx, svc.ID, serverID)
		if err != nil {
			return nil, fmt.Errorf("read its shared volumes: %w", err)
		}
		p.parts["shared_mounts"] = fmt.Sprintf("%+v", mounts)
	}
	if s.observabilityQuerier != nil && orgID != "" {
		t, err := s.observabilityQuerier.LokiFor(ctx, orgID, svc.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("read its Loki: %w", err)
		}
		p.parts["loki"] = t.ShipTo(serverID, svc.ID)
	}
	return p, nil
}

// shippedFields is svc with only the fields that count in its fingerprint
// set, the rest zero; printed whole, so the text is what it was when the
// rest were cleared one by one. Left out: its state (runtime status, live
// deployment, deploying, alerts, last deploy failed, last health check,
// the compose service its last deploy routed, timestamps), how it is
// shown (isolation advice, kind, effective deploy strategy, connect
// address) and the settings of deploys rather than of what runs (deploy on
// push, rollback images kept, deploy timeout). A new field counts only once
// added here; TestShippedFieldsClassified fails until it is classified.
// The server is the deploy's: a move's, before the service records it.
func shippedFields(svc *service.Service, serverID string) service.Service {
	return service.Service{
		ID:                  svc.ID,
		Scope:               svc.Scope,
		ProjectID:           svc.ProjectID,
		EnvironmentID:       svc.EnvironmentID,
		ScopedOrgID:         svc.ScopedOrgID,
		ServerID:            serverID,
		Name:                svc.Name,
		Type:                svc.Type,
		SourceType:          svc.SourceType,
		GitRepoURL:          svc.GitRepoURL,
		GitBranch:           svc.GitBranch,
		RepositoryID:        svc.RepositoryID,
		BaseDirectory:       svc.BaseDirectory,
		DockerImage:         svc.DockerImage,
		InstallCommand:      svc.InstallCommand,
		BuildCommand:        svc.BuildCommand,
		StartCommand:        svc.StartCommand,
		BuildPack:           svc.BuildPack,
		Port:                svc.Port,
		Replicas:            svc.Replicas,
		CPULimit:            svc.CPULimit,
		MemoryLimit:         svc.MemoryLimit,
		PidsLimit:           svc.PidsLimit,
		ContainerHealth:     svc.ContainerHealth,
		Isolation:           svc.Isolation,
		EffectiveIsolation:  svc.EffectiveIsolation,
		ComposeContent:      svc.ComposeContent,
		ComposeRepoURL:      svc.ComposeRepoURL,
		ComposeFilePath:     svc.ComposeFilePath,
		ComposeRouteService: svc.ComposeRouteService,
		DeployStrategy:      svc.DeployStrategy,
		RegistryServer:      svc.RegistryServer,
		RegistryUsername:    svc.RegistryUsername,
		RegistryPassword:    svc.RegistryPassword,
		HasRegistryPassword: svc.HasRegistryPassword,
		AddonSlug:           svc.AddonSlug,
		NetworkAlias:        svc.NetworkAlias,
		Volumes:             svc.Volumes,
		GitUsername:         svc.GitUsername,
		GitToken:            svc.GitToken,
		HasGitToken:         svc.HasGitToken,
		PreDeployCommands:   svc.PreDeployCommands,
		PostDeployCommands:  svc.PostDeployCommands,
		HealthCheckPath:     svc.HealthCheckPath,
		HealthCheckTimeout:  svc.HealthCheckTimeout,
		HealthCheckRetries:  svc.HealthCheckRetries,
		RequiredEnvVars:     svc.RequiredEnvVars,
		MigrateCommand:      svc.MigrateCommand,
		Dependencies:        svc.Dependencies,
		BasicAuthUser:       svc.BasicAuthUser,
		BasicAuthPassword:   svc.BasicAuthPassword,
		IPAllowlist:         svc.IPAllowlist,

		ContainerHealthCommand: svc.ContainerHealthCommand,
	}
}

// configFilesPart is the service's config files, each path and content.
func (s *Service) configFilesPart(ctx context.Context, serviceID string) (string, error) {
	if s.configFileWriter == nil {
		return "", nil
	}
	files, err := s.configFileWriter.Contents(ctx, serviceID)
	if err != nil {
		return "", fmt.Errorf("read its config files: %w", err)
	}
	return filesPart(files), nil
}

func filesPart(files []string) string {
	return fmt.Sprintf("%q", slices.Sorted(slices.Values(files)))
}

// panelLimits are the panel's own limits among a stack's compose limits
// (composeLimits), by compose service: what its settings say, not its file.
func panelLimits(limits map[string]ComposeLimit) string {
	var out []string
	for name, l := range limits {
		if l.MemoryMB > 0 {
			out = append(out, name+":"+strconv.FormatInt(l.MemoryMB, 10)+":"+strconv.FormatFloat(l.CPUCores, 'g', -1, 64))
		}
	}
	sort.Strings(out)
	return fmt.Sprintf("%q", out)
}

// shippedSettings is Trigger's read of what input ships (D-159): of the
// service the caller built it from (input.Settings) or, without one, the
// service now. nil when there is no key or it can't be read: the deploy
// then records no fingerprint.
func (s *Service) shippedSettings(ctx context.Context, input TriggerInput) *shipped {
	if s.hashKey == nil || input.ServiceID == "" || input.Preview {
		return nil
	}
	svc := input.Settings
	if svc == nil {
		if s.serviceFetcher == nil {
			return nil
		}
		var err error
		if svc, err = s.serviceFetcher.Get(ctx, input.ServiceID); err != nil {
			s.log.Warn("read the deploy's settings", zap.String("service_id", input.ServiceID), zap.Error(err))
			return nil
		}
	}
	p, err := s.readShipped(ctx, svc, input.OrgID, input.ServerID, input.EnvVars)
	if err != nil {
		s.log.Warn("read the deploy's settings", zap.String("service_id", input.ServiceID), zap.Error(err))
		return nil
	}
	return p
}

// recordShipped records the fingerprint of what a deploy that went live
// shipped, unless a part changed while it ran.
func (s *Service) recordShipped(ctx context.Context, deploymentID string, input TriggerInput) {
	p := input.settings
	if p == nil {
		return
	}
	p.mu.Lock()
	drift := p.drift
	p.mu.Unlock()
	if drift {
		s.log.Info("settings changed while the deploy ran: no fingerprint recorded", zap.String("deployment_id", deploymentID))
		return
	}
	if err := s.repo.SetSettingsFingerprint(ctx, deploymentID, p.sum(s.hashKey)); err != nil {
		s.log.Warn("record the deploy's settings fingerprint", zap.String("deployment_id", deploymentID), zap.Error(err))
	}
}

// SettingsFingerprint is the fingerprint a deploy of serviceID now, for
// orgID with envVars, would record (D-159); "" without a key.
func (s *Service) SettingsFingerprint(ctx context.Context, serviceID, orgID string, envVars []string) (string, error) {
	if s.hashKey == nil || s.serviceFetcher == nil {
		return "", nil
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil {
		return "", fmt.Errorf("read the service: %w", err)
	}
	p, err := s.readShipped(ctx, svc, orgID, svc.ServerID, envVars)
	if err != nil {
		return "", err
	}
	return p.sum(s.hashKey), nil
}

// SettingsDeployed reports whether live, the service's live deployment,
// shipped its settings as they are now, for orgID with envVars (D-159):
// false when any was saved since, or live's weren't recorded.
func (s *Service) SettingsDeployed(ctx context.Context, live *Deployment, orgID string, envVars []string) (bool, error) {
	if live.SettingsFingerprint == "" {
		return false, nil
	}
	now, err := s.SettingsFingerprint(ctx, live.ServiceID, orgID, envVars)
	if err != nil {
		return false, err
	}
	return now != "" && hmac.Equal([]byte(now), []byte(live.SettingsFingerprint)), nil
}
