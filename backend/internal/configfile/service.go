package configfile

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// hostBasePath is where config files are written on a remote server, and on
// the control-plane server unless WithDataDir says otherwise.
const hostBasePath = "/var/paas/config"

// SSHProvider hands out the pooled SSH connection to a server, with its
// pinned host key. Satisfied by *server.Service.
type SSHProvider interface {
	SSH(srv *serverPkg.Server) sshpool.RemoteHost
}

// remoteExec runs a command on a server (sshpool.RemoteHost satisfies it).
type remoteExec interface {
	ExecInput(ctx context.Context, cmd string, stdin io.Reader) (string, error)
}

// Service contains the business logic for service config files.
type Service struct {
	repo      Repository
	ssh       SSHProvider // injected via WithSSH; nil skips remote writes
	localBase string      // config files on the control-plane server
	log       *zap.Logger
}

func NewService(repo Repository, log *zap.Logger) *Service {
	return &Service{repo: repo, localBase: hostBasePath, log: log}
}

// WithDataDir puts config files for the control-plane server under
// dataDir/config. dataDir must be mounted into the backend container at the
// same path as on the host (TOFT_DATA_DIR): Docker reads bind-mount sources
// on the host, so the backend has to write them where the host sees them.
func (s *Service) WithDataDir(dataDir string) {
	s.localBase = filepath.Join(dataDir, "config")
}

// WithSSH lets WriteToHost write to remote servers over their pooled SSH
// connections. Called from main.go.
func (s *Service) WithSSH(p SSHProvider) {
	s.ssh = p
}

func (s *Service) List(ctx context.Context, serviceID string) ([]ConfigFile, error) {
	return s.repo.List(ctx, serviceID)
}

// Upsert creates or replaces a config file. The path must pass ValidatePath
// (ErrInvalidPath otherwise).
func (s *Service) Upsert(ctx context.Context, serviceID string, in UpsertInput) (ConfigFile, error) {
	if err := ValidatePath(in.Path); err != nil {
		return ConfigFile{}, err
	}
	if len(in.Content) > maxContent {
		return ConfigFile{}, fmt.Errorf("%w: the file is larger than 1 MiB", ErrInvalidPath)
	}
	return s.repo.Upsert(ctx, serviceID, in)
}

// Delete removes one of the service's files and returns its path, for the
// audit log; "" when the service has no file id.
func (s *Service) Delete(ctx context.Context, id, serviceID string) (string, error) {
	return s.repo.Delete(ctx, id, serviceID)
}

// maxContent matches the database's CHECK on service_config_files.content.
const maxContent = 1 << 20

// VolumeMount mirrors service.VolumeMount to avoid a circular import.
// The deployment service converts these back to service.VolumeMount when injecting.
type VolumeMount struct {
	HostPath string
	Target   string
	ReadOnly bool
}

// WriteToHost writes all config files to the host filesystem and returns bind mounts.
// When srv is nil the files are written to the local filesystem; otherwise they are
// written over the server's pooled SSH connection. This is called by the deployment
// pipeline before container start.
// Errors are non-fatal: files that fail to write are logged and skipped.
func (s *Service) WriteToHost(ctx context.Context, serviceID string, srv *serverPkg.Server) ([]VolumeMount, error) {
	files, err := s.repo.List(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list config files: %w", err)
	}
	return s.WriteFiles(ctx, serviceID, srv, files)
}

// WriteFiles is WriteToHost with the files already read, so a deploy knows
// exactly what it wrote (its settings fingerprint, D-159).
func (s *Service) WriteFiles(ctx context.Context, serviceID string, srv *serverPkg.Server, files []ConfigFile) ([]VolumeMount, error) {

	var host remoteExec
	if srv != nil && len(files) > 0 {
		if s.ssh == nil {
			s.log.Warn("config files: SSH is not configured; remote files skipped",
				zap.String("service_id", serviceID), zap.String("server", srv.Name))
			return []VolumeMount{}, nil
		}
		host = s.ssh.SSH(srv)
	}

	mounts := make([]VolumeMount, 0, len(files))

	for _, f := range files {
		// Construct the host path preserving the full container directory
		// structure. A path stored before validation existed that would land
		// outside the service's directory is skipped, never written.
		base := s.localBase
		if srv != nil {
			base = hostBasePath
		}
		hostPath, err := hostPathFor(base, serviceID, f.Path)
		if err != nil {
			s.log.Warn("config file: invalid path skipped", zap.String("service_id", serviceID), zap.String("path", f.Path), zap.Error(err))
			continue
		}
		hostDir := filepath.Dir(hostPath)

		if srv == nil {
			// Local: write directly to the host filesystem.
			if err := os.MkdirAll(hostDir, 0755); err != nil {
				s.log.Warn("config file: failed to create dir", zap.String("dir", hostDir), zap.Error(err))
				continue
			}
			if err := os.WriteFile(hostPath, []byte(f.Content), 0644); err != nil {
				s.log.Warn("config file: failed to write file", zap.String("path", hostPath), zap.Error(err))
				continue
			}
		} else {
			if err := writeRemote(ctx, host, hostDir, hostPath, f.Content); err != nil {
				s.log.Warn("config file: remote write failed",
					zap.String("path", hostPath),
					zap.String("server", srv.Name),
					zap.Error(err),
				)
				continue
			}
		}

		mounts = append(mounts, VolumeMount{
			HostPath: hostPath,
			Target:   f.Path,
			ReadOnly: true,
		})
	}

	return mounts, nil
}

// RemoveFromHost deletes the service's config file directory from the host
// it runs on: the local filesystem when srv is nil, otherwise over the
// server's pooled SSH connection. Called when the service is deleted; a
// directory that is not there is not an error.
func (s *Service) RemoveFromHost(ctx context.Context, serviceID string, srv *serverPkg.Server) error {
	if _, err := uuid.Parse(serviceID); err != nil {
		return fmt.Errorf("config files: invalid service id %q", serviceID) // never rm -rf a path built from it
	}
	if srv == nil {
		if err := os.RemoveAll(filepath.Join(s.localBase, serviceID)); err != nil {
			return fmt.Errorf("remove config files: %w", err)
		}
		return nil
	}
	if s.ssh == nil {
		return nil
	}
	cmd := "rm -rf " + shellEscape(hostBasePath+"/"+serviceID)
	if _, err := s.ssh.SSH(srv).ExecInput(ctx, cmd, strings.NewReader("")); err != nil {
		return fmt.Errorf("remove config files on %s: %w", srv.Name, err)
	}
	return nil
}

// writeRemote writes file content on a remote host over SSH. The content goes
// on stdin: config files can hold secrets, and on the command line the remote
// process list would show them.
func writeRemote(ctx context.Context, host remoteExec, dir, path, content string) error {
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s", shellEscape(dir), shellEscape(path))
	if _, err := host.ExecInput(ctx, cmd, strings.NewReader(content)); err != nil {
		return fmt.Errorf("ssh exec: %w", err)
	}
	return nil
}

// shellEscape wraps a path in single quotes for safe use in shell commands,
// escaping any single quote inside it.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
