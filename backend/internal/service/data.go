package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"go.uber.org/zap"

	serverPkg "github.com/Nickpap21/toft/backend/internal/server"
)

// DataVolume is a volume deleting a service removes from its server.
type DataVolume struct {
	Name string `json:"name"`
	// Bytes is -1 when the size wasn't read in time: the volume is listed
	// all the same.
	Bytes int64 `json:"bytes"`
	// From is DataFromVolume for the service's own named volume,
	// DataFromCompose for its compose stack's.
	From string `json:"from"`
}

const (
	DataFromVolume  = "volume"
	DataFromCompose = "compose"
)

// ServiceData is what deleting one service removes from its server.
type ServiceData struct {
	ServiceID   string       `json:"service_id"`
	ServiceName string       `json:"service_name"`
	ServerName  string       `json:"server_name"`
	Read        bool         `json:"read"`
	Reason      string       `json:"reason,omitempty"`
	Volumes     []DataVolume `json:"volumes"`
}

// DataRow is a service as its data reads it: what names its data on its
// server (Repository.ListData).
type DataRow struct {
	ID, Name, ServerID, ServerName string
	Volumes                        []VolumeMount
}

// DataRef is a service whose data a server's orchestrator reads: its own
// named volumes' Docker names (ownVolumes), and its ID and name, which name
// its compose projects.
type DataRef struct {
	ID, Name string
	Volumes  []string
}

// dataReader reads what deleting services removes from one server, keyed
// by service ID (docker.LocalOrchestrator, docker.RemoteOrchestrator).
type dataReader interface {
	ServiceData(ctx context.Context, refs []DataRef, sizes bool) (map[string][]DataVolume, error)
}

// ErrConfirmName: deleting the service would remove data (or its data
// couldn't be read), and its name wasn't typed.
var ErrConfirmName = errors.New("type the service's name to confirm: deleting it deletes its data")

// ownVolumes are the Docker names of a service's own named volumes: what a
// delete removes besides its compose stack's, and what Data names. A bind
// mount's folder is the server's, not the service's.
func ownVolumes(serviceID string, mounts []VolumeMount) []string {
	out := make([]string, 0, len(mounts))
	for _, v := range mounts {
		if v.HostPath == "" {
			out = append(out, volumeDockerName(serviceID, v.Name))
		}
	}
	return out
}

// Data is what deleting the services of a scope removes from their
// servers: a service (scope "" and its ID), an environment or a project,
// sizes read unless !sizes,
// as Delete and DeleteIn tear them down, preview copies included. One
// server read per server; a server that can't be read gives the volumes
// Toft knows of, sizes unknown.
func (s *ServiceManager) Data(ctx context.Context, scope Scope, id string, sizes bool) ([]ServiceData, error) {
	ids := []string{id}
	if scope != "" {
		var err error
		if ids, err = s.repo.ListIDsIn(ctx, scope, id); err != nil {
			return nil, err
		}
	}
	rows, err := s.repo.ListData(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]ServiceData, 0, len(rows))
	byServer := map[string][]DataRow{}
	order := []string{}
	for _, r := range rows {
		if _, ok := byServer[r.ServerID]; !ok {
			order = append(order, r.ServerID)
		}
		byServer[r.ServerID] = append(byServer[r.ServerID], r)
	}
	// Servers are read side by side: each can take its connect timeout plus
	// the size read, and a project on three servers shouldn't wait for all
	// three in turn. Every goroutine ends with its server's read.
	parts := make([][]ServiceData, len(order))
	var wg sync.WaitGroup
	for i, serverID := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parts[i] = s.serverData(ctx, serverID, byServer[serverID], sizes)
		}()
	}
	wg.Wait()
	for _, p := range parts {
		out = append(out, p...)
	}
	// The service asked about first, then by name, as ListData orders them.
	pos := make(map[string]int, len(rows))
	for i, r := range rows {
		pos[r.ID] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return pos[out[i].ServiceID] < pos[out[j].ServiceID] })
	return out, nil
}

// serverData reads one server's share of Data.
func (s *ServiceManager) serverData(ctx context.Context, serverID string, rows []DataRow, sizes bool) []ServiceData {
	// The reason is the client's, fixed; the error is the log's (it can
	// name a server's address or a database failure).
	unread := func(reason string, err error) []ServiceData {
		if err != nil {
			s.log.Warn("read what a delete removes", zap.Error(err), zap.String("server_id", serverID))
		}
		out := make([]ServiceData, 0, len(rows))
		for _, r := range rows {
			vols := make([]DataVolume, 0, len(r.Volumes))
			for _, name := range ownVolumes(r.ID, r.Volumes) {
				vols = append(vols, DataVolume{Name: name, Bytes: -1, From: DataFromVolume})
			}
			out = append(out, ServiceData{ServiceID: r.ID, ServiceName: r.Name, ServerName: r.ServerName, Reason: reason, Volumes: vols})
		}
		return out
	}
	if s.factory == nil {
		return unread("no Docker access here", nil)
	}
	var srv *serverPkg.Server
	if serverID != "" {
		if s.serverFetcher == nil {
			return unread("its server is unknown", nil)
		}
		got, err := s.serverFetcher.GetServer(ctx, serverID)
		if err != nil {
			return unread("its server couldn't be loaded", err)
		}
		srv = got
	}
	orch, cleanup, err := s.factory.ForServer(srv)
	if err != nil {
		return unread(fmt.Sprintf("couldn't reach %s", serverName(srv)), err)
	}
	defer cleanup()
	reader, ok := orch.(dataReader)
	if !ok {
		return unread("this server's data can't be read", nil)
	}
	refs := make([]DataRef, 0, len(rows))
	for _, r := range rows {
		refs = append(refs, DataRef{ID: r.ID, Name: r.Name, Volumes: ownVolumes(r.ID, r.Volumes)})
	}
	got, err := reader.ServiceData(ctx, refs, sizes)
	if err != nil {
		return unread(fmt.Sprintf("couldn't read %s", serverName(srv)), err)
	}
	out := make([]ServiceData, 0, len(rows))
	for _, r := range rows {
		vols := got[r.ID]
		if vols == nil {
			vols = []DataVolume{}
		}
		out = append(out, ServiceData{ServiceID: r.ID, ServiceName: r.Name, ServerName: r.ServerName, Read: true, Volumes: vols})
	}
	return out
}

func serverName(srv *serverPkg.Server) string {
	if srv == nil {
		return "the panel host"
	}
	return srv.Name
}

// DeleteConfirmed deletes a service as Delete does, once its name is typed
// when deleting it would remove data: any volume Data names (its preview
// copies' included), or data that couldn't be read. Without it,
// ErrConfirmName. Holds for every caller of DELETE /services/{id}.
func (s *ServiceManager) DeleteConfirmed(ctx context.Context, id, confirm string) error {
	svc, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("get service: %w", err)
	}
	if confirm != svc.Name {
		data, err := s.Data(ctx, "", id, false)
		if err != nil {
			return err
		}
		for _, d := range data {
			if !d.Read || len(d.Volumes) > 0 {
				return ErrConfirmName
			}
		}
	}
	return s.Delete(ctx, id)
}
