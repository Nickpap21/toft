package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
)

// The desired state (D-111): the panel sends what should run, and the agent
// keeps the newest in desired.json and answers with the drift it sees. It
// acts on none of it yet, but for the routes (routes.go) and where it ships
// the logs (logs.go).
//
// The state is kept per panel epoch (D-113). A panel restored from a backup
// has a new epoch and an older idea of what runs: the agent then stays
// frozen, keeping everything as it is and refusing that panel's orders, and
// answers with what runs, until the panel sends its choice (Unfreeze).

// composeProjectRe takes the service ID's first 8 characters from a
// platform compose project's name (rollout.ComposeProjectName).
var composeProjectRe = regexp.MustCompile(`^paas-(?:.*-)?([0-9a-f]{8})$`)

func desiredFile() string { return filepath.Join(stateDir, "desired.json") }

func heldDesired() agentproto.Desired {
	var held agentproto.Desired
	if raw, err := os.ReadFile(desiredFile()); err == nil {
		_ = json.Unmarshal(raw, &held)
	}
	return held
}

// checkEpoch refuses an order from a panel epoch other than the one whose
// state the agent holds. An agent with none takes the first; a panel before
// v0.19.0 sends none.
func checkEpoch(epoch string) error {
	if held := heldDesired().Epoch; epoch != "" && held != "" && epoch != held {
		return errors.New(agentproto.ErrEpochFrozen + ": the panel was restored from a backup, and this server kept its own state; " +
			"choose which state to keep on the server's page")
	}
	return nil
}

// desired serves SubsystemDesired: it keeps req when it is newer than the
// stored state, or the first, or the panel's choice after a restore, and
// answers with the generation it holds and the drift against it, or, while
// it holds another epoch's state, with what runs.
func (m *rollouts) desired(ch ssh.Channel) error {
	var req agentproto.Desired
	if err := readRequest(ch, &req); err != nil {
		return err
	}
	held := heldDesired()
	if req.Services != nil && (held.Epoch == "" || req.Unfreeze || req.Epoch == held.Epoch && req.Generation >= held.Generation) {
		if held.Epoch != "" && req.Epoch != held.Epoch {
			slog.Info("the restored panel's state is taken: it chose which state to keep", "epoch", req.Epoch, "held", held.Epoch)
		}
		req.Unfreeze = false
		raw, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(desiredFile(), raw, 0o600); err != nil {
			return fmt.Errorf("record the desired state: %w", err)
		}
		held = req
		m.routes.soon() // a service's serving slot comes from the held state
	}
	list, err := m.dc.ContainerList(context.Background(), container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list the containers: %w", err)
	}
	ack := agentproto.DesiredAck{Drift: []agentproto.Drift{}}
	if held.Epoch != req.Epoch {
		slog.Warn("frozen: the panel has another epoch (restored from a backup?); waiting for its choice", "epoch", req.Epoch, "held", held.Epoch)
		ack.Frozen = inventory(list)
	} else {
		ack.Generation = held.Generation
		ack.Drift = drift(held.Services, list, m.busy())
	}
	if m.routes.liveAPI(context.Background()) != "" {
		n := m.routes.count()
		ack.Routes = &n
	}
	return json.NewEncoder(ch).Encode(ack)
}

// inventory lists the platform's containers, for a frozen agent's answer.
func inventory(list []container.Summary) []agentproto.Running {
	out := make([]agentproto.Running, 0, len(list))
	for _, c := range list {
		name := rollout.SummaryName(c)
		r := agentproto.Running{Container: name, Image: c.Image, State: string(c.State), Created: c.Created}
		if rollout.IsPlatformSlot(c.Labels, name) {
			r.Service, r.Deployment = c.Labels[rollout.LabelServiceID], c.Labels[rollout.LabelDeploymentID]
		} else if sm := composeProjectRe.FindStringSubmatch(c.Labels[rollout.LabelComposeProject]); sm != nil {
			r.Service, r.Compose = sm[1], true
		} else {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Container < out[j].Container })
	size := 2 // []
	for i, r := range out {
		raw, _ := json.Marshal(r)
		if size += len(raw) + 1; size > maxInventory {
			slog.Warn("frozen: too many containers to list; the panel sees the first ones", "listed", i, "running", len(out))
			return out[:i]
		}
	}
	return out
}

// maxInventory keeps a frozen agent's answer within what the panel stores
// of it (servers.agent_frozen, 256 KiB): about a thousand containers. The
// ones past it read as missing on the panel and are left running.
const maxInventory = 240 << 10

// activeDeployments returns each service's deployment in the held
// desired state.
func activeDeployments() map[string]string {
	held := heldDesired()
	out := make(map[string]string, len(held.Services))
	for _, s := range held.Services {
		out[s.ID] = s.Deployment
	}
	return out
}

// busy returns the services with a rollout running or queued: their
// containers are changing.
func (m *rollouts) busy() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.queues))
	for id := range m.queues {
		out[id] = true
	}
	return out
}

// drift compares the services with the containers that run.
func drift(services []agentproto.DesiredService, list []container.Summary, busy map[string]bool) []agentproto.Drift {
	known := map[string]bool{}  // service IDs
	known8 := map[string]bool{} // their first 8 characters, as compose projects carry them
	for _, s := range services {
		known[s.ID] = true
		if len(s.ID) >= 8 {
			known8[s.ID[:8]] = true
		}
	}
	type seen struct{ any, running, restarting bool }
	slots := map[string]*seen{}    // service ID + deployment ID
	projects := map[string]*seen{} // service ID's first 8 characters
	out := make([]agentproto.Drift, 0)
	note := func(m map[string]*seen, key string, state container.ContainerState) {
		s := m[key]
		if s == nil {
			s = &seen{}
			m[key] = s
		}
		s.any = true
		s.running = s.running || state == container.StateRunning
		s.restarting = s.restarting || state == container.StateRestarting
	}
	for _, c := range list {
		name := rollout.SummaryName(c)
		live := c.State == container.StateRunning || c.State == container.StateRestarting
		if rollout.IsPlatformSlot(c.Labels, name) {
			id := c.Labels[rollout.LabelServiceID]
			note(slots, id+" "+c.Labels[rollout.LabelDeploymentID], c.State)
			if live && !known[id] && !busy[id] {
				out = append(out, agentproto.Drift{ServiceID: id, Kind: agentproto.DriftOrphan, Container: name})
			}
			continue
		}
		sm := composeProjectRe.FindStringSubmatch(c.Labels[rollout.LabelComposeProject])
		if sm == nil {
			continue
		}
		note(projects, sm[1], c.State)
		if live && !known8[sm[1]] {
			out = append(out, agentproto.Drift{Kind: agentproto.DriftOrphan, Container: name})
		}
	}
	for _, s := range services {
		if !s.Running || s.Deployment == "" || busy[s.ID] {
			continue
		}
		got := slots[s.ID+" "+s.Deployment]
		if s.Compose && len(s.ID) >= 8 {
			got = projects[s.ID[:8]]
		}
		switch {
		case got == nil:
			out = append(out, agentproto.Drift{ServiceID: s.ID, Kind: agentproto.DriftMissing})
		case got.restarting:
			out = append(out, agentproto.Drift{ServiceID: s.ID, Kind: agentproto.DriftCrashLoop})
		case !got.running:
			out = append(out, agentproto.Drift{ServiceID: s.ID, Kind: agentproto.DriftStopped})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ServiceID != out[j].ServiceID {
			return out[i].ServiceID < out[j].ServiceID
		}
		return out[i].Container < out[j].Container
	})
	return out
}
