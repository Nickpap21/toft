package deployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
	"go.uber.org/zap"
)

// Moving a service to another server. A service that runs moves with a
// deploy to the new server (TriggerInput.MoveFrom): its server changes only
// once it runs there (completeMove), and then it is removed from the old
// one. A move that fails removes what it started on the new server and
// leaves the service as it was, still running on the old one (abortMove).
// So the service's server is always where its containers are, which Stop
// and Delete rely on (service.ErrMoveWhileRunning).

// MoveSource is the server a move deploy takes the service from.
type MoveSource struct {
	ServerID string         // "" is the control-plane server
	Server   *server.Server // nil for the control-plane server
}

// ServiceMover records the server a move deployed a service to. Satisfied
// by service.ServiceManager.
type ServiceMover interface {
	CompleteMove(ctx context.Context, serviceID, serverID string) error
	// MovedHostnames is hostnames with the temporary ones pointing at the
	// new server (D-150), which the move's deploy routes there.
	MovedHostnames(ctx context.Context, hostnames []string, fromServerID, toServerID string) []string
}

// errMovesNotConfigured refuses a move deploy when no ServiceMover is wired.
var errMovesNotConfigured = errors.New("moving a service is not configured")

// WithServiceMover lets deploys move a service to another server.
func (s *Service) WithServiceMover(m ServiceMover) { s.mover = m }

// serverLabel names a server in the deploy log.
func serverLabel(srv *server.Server) string {
	if srv == nil {
		return "the panel host"
	}
	return srv.Name
}

// completeMove finishes a move whose deploy succeeded: it records the new
// server, then removes the service from the old one. It reports false when
// the move could not be recorded; the deploy then failed (abortMove). A
// service that could not be removed from the old server is only logged:
// it runs on the new one, which is now its server.
func (s *Service) completeMove(ctx context.Context, deploymentID string, input TriggerInput, imageTag string, appendLog func(string)) bool {
	from, to := serverLabel(input.MoveFrom.Server), serverLabel(input.Server)
	if err := s.mover.CompleteMove(ctx, input.ServiceID, input.ServerID); err != nil {
		s.log.Error("could not record a move", zap.String("deployment_id", deploymentID), zap.Error(err))
		appendLog("✗ Could not record the move to " + to + ": " + err.Error())
		s.abortMove(ctx, deploymentID, input, imageTag, "could not record the move: "+err.Error())
		return false
	}
	appendLog("→ Moved to " + to + "; removing it from " + from)
	if err := s.removeFrom(ctx, input.MoveFrom.Server, input, appendLog); err != nil {
		s.log.Warn("could not remove a moved service from its old server", zap.String("deployment_id", deploymentID), zap.Error(err))
		appendLog(fmt.Sprintf("⚠ Could not remove it from %s: %v. It still runs there too; remove its containers on that server", from, err))
	} else {
		appendLog("■ Removed it from " + from)
	}
	return true
}

// abortMove records a move whose deploy failed: what it started on the new
// server is removed, and the service keeps its server, runtime status and
// active deployment, so it still runs on the old server as before.
func (s *Service) abortMove(ctx context.Context, deploymentID string, input TriggerInput, imageTag, errMsg string) {
	appendLog := func(msg string) {
		if _, err := s.repo.InsertLog(ctx, deploymentID, msg); err != nil {
			s.log.Warn("failed to insert deployment log", zap.Error(err))
		}
	}
	from, to := serverLabel(input.MoveFrom.Server), serverLabel(input.Server)
	if err := s.removeFrom(ctx, input.Server, input, appendLog); err != nil {
		s.log.Error("could not remove a failed move's containers", zap.String("deployment_id", deploymentID), zap.Error(err))
		appendLog(fmt.Sprintf("⚠ Could not remove what this deploy started on %s: %v", to, err))
	} else {
		appendLog("↩ Removed it from " + to)
	}
	appendLog("↩ Not moved: it stays on " + from + ", as it was")
	if _, err := s.repo.UpdateStatus(ctx, deploymentID, StatusFailed, imageTag, input.CommitSHA, errMsg); err != nil {
		s.log.Error("failed to update deployment status to failed", zap.Error(err), zap.String("deployment_id", deploymentID))
	}
	s.publishFailed(input, deploymentID, errMsg)
}

// ImageRemover deletes the images built for a service on its host
// (docker orchestrators).
type ImageRemover interface {
	RemoveServiceImages(ctx context.Context, serviceID, serviceName string) error
}

// removeFrom stops and removes the service's containers on srv (nil: the
// control-plane server), then the images built for it there, which no
// deploy on another server can run. Volumes stay, with their data. Images
// that cannot be removed are only logged: they take disk space, nothing
// runs them.
func (s *Service) removeFrom(ctx context.Context, srv *server.Server, input TriggerInput, appendLog func(string)) error {
	ctx = context.WithoutCancel(ctx)
	orch, cleanup, err := s.factory.ForServer(srv)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", serverLabel(srv), err)
	}
	defer cleanup()
	if input.SourceType == string(service.SourceTypeDockerCompose) {
		err = orch.StopCompose(ctx, input.ServiceID, input.ServiceName, input.ComposeContent)
	} else {
		err = orch.Stop(ctx, input.ServiceID)
	}
	if err != nil {
		return err
	}
	if images, ok := orch.(ImageRemover); ok {
		if err := images.RemoveServiceImages(ctx, input.ServiceID, input.ServiceName); err != nil {
			s.log.Warn("could not remove a moved service's images", zap.String("service_id", input.ServiceID), zap.Error(err))
			appendLog(fmt.Sprintf("⚠ Could not remove its images from %s: %v", serverLabel(srv), err))
		}
	}
	return nil
}
