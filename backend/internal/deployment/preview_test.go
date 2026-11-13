package deployment

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"github.com/Nickpap21/toft/backend/internal/server"
	"github.com/Nickpap21/toft/backend/internal/service"
)

// roomServer: 4096 MiB effective, 3584 allocated, 512 free.
type roomServer struct{}

func (roomServer) GetCapacitySummaryFor(_ context.Context, serverID, _ string) (*server.CapacitySummary, error) {
	return &server.CapacitySummary{ServerID: serverID, CapacityDiscovered: true, EffectiveMemoryMB: 4096, AllocatedMemoryMB: 3584}, nil
}

type roomServices map[string]*service.Service

func (r roomServices) Get(_ context.Context, id string) (*service.Service, error) {
	if s, ok := r[id]; ok {
		return s, nil
	}
	return nil, errors.New("not found")
}

type evictor struct {
	cands   []RoomCandidate
	stopped []string
	notes   []string
}

func (e *evictor) RoomCandidates(context.Context, string, string) ([]RoomCandidate, error) {
	return e.cands, nil
}
func (e *evictor) StopForRoom(_ context.Context, pullID, note, _ string) error {
	e.stopped = append(e.stopped, pullID)
	e.notes = append(e.notes, note)
	return nil
}

func roomService() (*Service, *evictor) {
	s := NewService(nil, nil, nil, zap.NewNop())
	s.WithCapacitySvc(roomServer{})
	s.WithServiceReader(roomServices{
		// Stopped, so not in the 3584 allocated: the deploy adds them.
		"api":     {ID: "api", SourceType: "docker-image", MemoryLimit: "1g", RuntimeStatus: "stopped"},
		"small":   {ID: "small", SourceType: "docker-image", MemoryLimit: "256m", RuntimeStatus: "stopped"},
		"old-web": {ID: "old-web", SourceType: "docker-image", MemoryLimit: "512m"},
		"new-web": {ID: "new-web", SourceType: "docker-image", MemoryLimit: "512m"},
	})
	e := &evictor{cands: []RoomCandidate{{PullID: "pr-oldest", ServiceIDs: []string{"old-web"}}, {PullID: "pr-newer", ServiceIDs: []string{"new-web"}}}}
	s.WithPreviews(e, nil, nil)
	return s, e
}

func TestMakeRoomStopsTheOldestPreviewsFirst(t *testing.T) {
	s, e := roomService()
	in := TriggerInput{ServiceID: "api", ServerID: "srv", Server: &server.Server{ID: "srv", Name: "fsn1-prod-01"},
		ServiceName: "api", EnvironmentName: "production", OrgID: "ws"}
	s.MakeRoom(context.Background(), in)
	assert.Equal(t, []string{"pr-oldest"}, e.stopped, "1 GiB needs 512 MiB more: the oldest preview's 512 MiB is enough")
	assert.Equal(t, []string{"production’s api needed the memory on fsn1-prod-01"}, e.notes)

	s, e = roomService()
	in.ServiceID = "small"
	s.MakeRoom(context.Background(), in)
	assert.Empty(t, e.stopped, "a deploy that fits stops nothing")

	s, e = roomService()
	in.ServiceID, in.Preview = "api", true
	s.MakeRoom(context.Background(), in)
	assert.Empty(t, e.stopped, "a preview never stops another")
}

func TestPreviewRefusedWhenItDoesNotFit(t *testing.T) {
	s, _ := roomService()
	in := TriggerInput{ServiceID: "api", ServerID: "srv", Server: &server.Server{ID: "srv", Name: "fsn1-prod-01"}, Preview: true}
	err := s.previewRoom(context.Background(), in)
	var noRoom *PreviewNoRoomError
	assert.ErrorAs(t, err, &noRoom)
	assert.Equal(t, "fsn1-prod-01 has 512 MiB free; this preview service needs 1.0 GiB. A preview never stops another: Start again once there is room", err.Error())
	in.ServiceID = "small"
	assert.NoError(t, s.previewRoom(context.Background(), in))
}

type panelLedger struct{ apps, alloc float64 }

func (p panelLedger) AppsMemoryMB(context.Context) (float64, error)      { return p.apps, nil }
func (p panelLedger) AllocatedMemoryMB(context.Context) (float64, error) { return p.alloc, nil }

func TestMakeRoomOnThePanelsServer(t *testing.T) {
	s, e := roomService()
	s.WithPreviews(e, panelLedger{apps: 2048, alloc: 1792}, nil)
	s.MakeRoom(context.Background(), TriggerInput{ServiceID: "api", ServiceName: "api", OrgID: "ws"})
	assert.Equal(t, []string{"pr-oldest", "pr-newer"}, e.stopped, "the panel's apps share is the ledger there")
	assert.Equal(t, "api needed the memory on the panel's server", e.notes[0])
}
