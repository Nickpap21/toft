package deployment

import (
	"context"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/service"
)

// OriginPreview is a pull request's preview deploying one of its services
// (D-97). A refused one is recorded as a failed deployment, as a push's is.
const OriginPreview Origin = "preview"

// RoomCandidate is a running preview's services on a server.
type RoomCandidate struct {
	PullID     string
	ServiceIDs []string
}

// Evictor stops previews so a deploy fits (D-15, D-97). Satisfied by
// *preview.Service via main.go.
type Evictor interface {
	// RoomCandidates are the running previews of orgID ("" every
	// workspace's) with services on serverID ("" the panel's own), oldest
	// first.
	RoomCandidates(ctx context.Context, serverID, orgID string) ([]RoomCandidate, error)
	StopForRoom(ctx context.Context, pullID, note, actorID string) error
}

// PanelCapacity is the panel's own server's ledger (D-62): the memory its
// apps share holds, and what its services' limits take. Satisfied in
// main.go.
type PanelCapacity interface {
	AppsMemoryMB(ctx context.Context) (float64, error)
	// Allocated is each running service's limits on the panel's own
	// server, as requestedLimits counts them.
	AllocatedMemoryMB(ctx context.Context) (float64, error)
}

// PreviewCopies says whether a service is one of a preview's copies, which
// deploy only through their preview. Satisfied by *service.ServiceManager.
type PreviewCopies interface {
	IsPreviewCopy(ctx context.Context, id string) (bool, error)
}

// WithPreviews lets a deploy that doesn't fit stop previews first, and
// checks a preview's own deploy fits. Called from main.go.
func (s *Service) WithPreviews(e Evictor, panel PanelCapacity, copies PreviewCopies) {
	s.evictor, s.panelCapacity, s.copies = e, panel, copies
}

// ErrPreviewService: a preview's service deploys from its preview only.
var ErrPreviewService = errors.New("a preview's services deploy from the pull request's preview: Redeploy there")

// IsPreviewCopy reports whether a service is a preview's.
func (s *Service) IsPreviewCopy(ctx context.Context, id string) (bool, error) {
	if s.copies == nil {
		return false, nil
	}
	return s.copies.IsPreviewCopy(ctx, id)
}

// PreviewNoRoomError: a preview's deploy doesn't fit on its server. Previews
// never stop other previews (D-97).
type PreviewNoRoomError struct {
	Server              string
	FreeMB, RequestedMB float64
}

func (e *PreviewNoRoomError) Error() string {
	return fmt.Sprintf("%s has %s free; this preview service needs %s. A preview never stops another: Start again once there is room",
		e.Server, mib(e.FreeMB), mib(e.RequestedMB))
}

func mib(mb float64) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GiB", mb/1024)
	}
	return fmt.Sprintf("%.0f MiB", mb)
}

// room is the memory free on serverID ("" the panel's own) for serviceID
// of orgID, its own allocation counted as free (a redeploy replaces it).
// known is false when the ledger can't say.
func (s *Service) room(ctx context.Context, serverID, orgID, serviceID string) (free float64, known bool) {
	var effective, allocated float64
	if serverID == "" {
		if s.panelCapacity == nil {
			return 0, false
		}
		apps, err := s.panelCapacity.AppsMemoryMB(ctx)
		if err != nil || apps <= 0 {
			return 0, false
		}
		alloc, err := s.panelCapacity.AllocatedMemoryMB(ctx)
		if err != nil {
			return 0, false
		}
		effective, allocated = apps, alloc
	} else {
		if s.capacitySvc == nil {
			return 0, false
		}
		sum, err := s.capacitySvc.GetCapacitySummaryFor(ctx, serverID, orgID)
		if err != nil || !sum.CapacityDiscovered {
			return 0, false
		}
		effective, allocated = float64(sum.EffectiveMemoryMB), sum.AllocatedMemoryMB
	}
	_, own := s.ownAllocation(ctx, serviceID)
	return effective - max(allocated-own, 0), true
}

// ownAllocation is what serviceID itself holds of its server's allocated
// sum, which a redeploy replaces: its limits, unless it is stopped (the sum
// leaves stopped services out).
func (s *Service) ownAllocation(ctx context.Context, serviceID string) (cpuCores, memoryMB float64) {
	if s.serviceFetcher == nil || serviceID == "" {
		return 0, 0
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil || svc == nil || svc.RuntimeStatus == "stopped" {
		return 0, 0
	}
	return s.requestedLimits(ctx, svc)
}

// requested is what a deploy of serviceID asks of its server, in MiB.
func (s *Service) requested(ctx context.Context, serviceID string) (*service.Service, float64) {
	if s.serviceFetcher == nil {
		return nil, 0
	}
	svc, err := s.serviceFetcher.Get(ctx, serviceID)
	if err != nil {
		return nil, 0
	}
	_, mem := s.requestedLimits(ctx, svc)
	return svc, mem
}

// previewRoom refuses a preview's deploy that doesn't fit on its server.
func (s *Service) previewRoom(ctx context.Context, input TriggerInput) error {
	_, need := s.requested(ctx, input.ServiceID)
	if need <= 0 {
		return nil
	}
	free, known := s.room(ctx, input.ServerID, input.OrgID, input.ServiceID)
	if !known || need <= free {
		return nil
	}
	name := "The panel's server"
	if input.Server != nil {
		name = input.Server.Name
	}
	return &PreviewNoRoomError{Server: name, FreeMB: max(free, 0), RequestedMB: need}
}

// MakeRoom stops previews on the server of a deploy that doesn't fit,
// oldest first, until it does (D-15, D-97): never production or staging,
// and never for a preview. A deploy that still doesn't fit goes on as it
// would have (the interactive gate refuses it).
func (s *Service) MakeRoom(ctx context.Context, input TriggerInput) {
	if s.evictor == nil || input.Preview || input.ServiceID == "" {
		return
	}
	_, need := s.requested(ctx, input.ServiceID)
	if need <= 0 {
		return
	}
	free, known := s.room(ctx, input.ServerID, input.OrgID, input.ServiceID)
	if !known || need <= free {
		return
	}
	orgID := input.OrgID
	if input.ServerID == "" {
		orgID = "" // the panel's apps share is the whole instance's
	}
	cands, err := s.evictor.RoomCandidates(ctx, input.ServerID, orgID)
	if err != nil {
		s.log.Warn("previews to stop for room", zap.Error(err))
		return
	}
	where := "the panel's server"
	if input.Server != nil {
		where = input.Server.Name
	} else if input.ServerID != "" && s.serverReader != nil {
		if srv, err := s.serverReader.GetServer(ctx, input.ServerID); err == nil {
			where = srv.Name
		}
	}
	note := fmt.Sprintf("%s’s %s needed the memory on %s", input.EnvironmentName, input.ServiceName, where)
	if input.EnvironmentName == "" {
		note = fmt.Sprintf("%s needed the memory on %s", input.ServiceName, where)
	}
	for _, c := range cands {
		if free >= need {
			return
		}
		freed := 0.0
		for _, id := range c.ServiceIDs {
			_, mem := s.requested(ctx, id)
			freed += mem
		}
		if err := s.evictor.StopForRoom(ctx, c.PullID, note, input.ActorID); err != nil {
			s.log.Warn("stop a preview for room", zap.Error(err), zap.String("pull_request_id", c.PullID))
			continue
		}
		free += freed
	}
}
