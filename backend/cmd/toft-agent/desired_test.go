package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"golang.org/x/crypto/ssh"
)

// The agent keeps the newest desired state the panel sends and answers with
// the drift against it: a running service's slot missing or restarting, a
// stack with nothing running, a slot of a service the panel doesn't have.
func TestAgentReportsDriftAgainstTheDesiredState(t *testing.T) {
	stateDir = t.TempDir()
	const (
		missing = "2222bbbb-0000-0000-0000-000000000002"
		stack   = "3333cccc-0000-0000-0000-000000000003"
		looping = "4444dddd-0000-0000-0000-000000000004"
		gone    = "9999ffff-0000-0000-0000-000000000009"
	)
	slot := func(svc, dep string) map[string]string {
		return map[string]string{rollout.LabelServiceID: svc, rollout.LabelDeploymentID: dep}
	}
	f := fakedocker.New(t)
	f.Add(rollout.BlueContainerName(testSvc, "web", ""), container.StateRunning, slot(testSvc, "dep-1"))
	f.Add(rollout.BlueContainerName(looping, "api", ""), container.StateRestarting, slot(looping, "dep-4"))
	f.Add(rollout.BlueContainerName(gone, "old", ""), container.StateRunning, slot(gone, "dep-9"))
	f.Add("paas-stack-3333cccc-db-1", container.StateExited, map[string]string{rollout.LabelComposeProject: "paas-stack-3333cccc"})
	f.Add("paas-traefik", container.StateRunning, nil)

	pem, pub := panelKey(t)
	c, err := connectWith(t, pem, ssh.FingerprintSHA256(pub), testRollouts(f))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ask := func(req agentproto.Desired) agentproto.DesiredAck {
		t.Helper()
		raw, _ := json.Marshal(req)
		out, err := c.SubsystemContext(context.Background(), agentproto.SubsystemDesired, strings.NewReader(string(raw)+"\n"))
		if err != nil {
			t.Fatalf("desired: %v %q", err, out)
		}
		var ack agentproto.DesiredAck
		if err := json.Unmarshal([]byte(out), &ack); err != nil {
			t.Fatalf("answer %q: %v", out, err)
		}
		return ack
	}

	full := agentproto.Desired{Epoch: "e1", Generation: 5, Services: []agentproto.DesiredService{
		{ID: testSvc, Running: true, Deployment: "dep-1"},
		{ID: missing, Running: true, Deployment: "dep-2"},
		{ID: stack, Compose: true, Running: true, Deployment: "dep-3"},
		{ID: looping, Running: true, Deployment: "dep-4"},
	}}
	ack := ask(full)
	want := []agentproto.Drift{
		{ServiceID: missing, Kind: agentproto.DriftMissing},
		{ServiceID: stack, Kind: agentproto.DriftStopped},
		{ServiceID: looping, Kind: agentproto.DriftCrashLoop},
		{ServiceID: gone, Kind: agentproto.DriftOrphan, Container: rollout.BlueContainerName(gone, "old", "")},
	}
	if ack.Generation != 5 || !sameDrift(ack.Drift, want) {
		t.Fatalf("ack %+v", ack)
	}

	if again := ask(agentproto.Desired{Epoch: "e1", Generation: 5}); again.Generation != 5 || !sameDrift(again.Drift, want) {
		t.Fatalf("a check against the held state: %+v", again)
	}
	if stale := ask(agentproto.Desired{Epoch: "e1", Generation: 4, Services: []agentproto.DesiredService{}}); stale.Generation != 5 {
		t.Fatalf("an older generation replaced the held one: %+v", stale)
	}
	if other := ask(agentproto.Desired{Epoch: "e2", Generation: 5}); other.Generation != 0 || len(other.Drift) != 0 {
		t.Fatalf("another epoch's check: %+v", other)
	}
	// A restored panel (D-113): its state isn't taken, and the agent answers
	// with what runs, refusing its orders, until the panel chooses.
	restored := agentproto.Desired{Epoch: "e2", Generation: 1, Services: []agentproto.DesiredService{}}
	frozen := ask(restored)
	if frozen.Generation != 0 || len(frozen.Drift) != 0 || len(frozen.Frozen) != 4 {
		t.Fatalf("a restored panel's state was taken: %+v", frozen)
	}
	web := slices.IndexFunc(frozen.Frozen, func(r agentproto.Running) bool { return r.Service == testSvc })
	if web < 0 || frozen.Frozen[web].Deployment != "dep-1" || frozen.Frozen[web].State != "running" || frozen.Frozen[web].Created == 0 {
		t.Fatalf("inventory: %+v", frozen.Frozen)
	}
	if i := slices.IndexFunc(frozen.Frozen, func(r agentproto.Running) bool { return r.Compose }); i < 0 || frozen.Frozen[i].Service != stack[:8] {
		t.Fatalf("the stack's containers: %+v", frozen.Frozen)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "desired.json")); err != nil {
		t.Fatal(err)
	}
	if err := checkEpoch("e2"); err == nil || !strings.HasPrefix(err.Error(), agentproto.ErrEpochFrozen) {
		t.Fatalf("an order of the restored panel: %v", err)
	}
	if err := checkEpoch("e1"); err != nil {
		t.Fatal(err)
	}
	restored.Unfreeze = true
	if chosen := ask(restored); chosen.Generation != 1 || len(chosen.Drift) != 3 || chosen.Frozen != nil {
		t.Fatalf("the restored panel's choice is taken, and every running slot is an orphan of its state: %+v", chosen)
	}
	if err := checkEpoch("e2"); err != nil {
		t.Fatal(err)
	}
}

// A frozen agent with nothing running still answers as frozen ([], which
// the panel reads apart from not frozen), and one running more than the
// panel stores lists the first containers only.
func TestFrozenAnswerIsNeverLeftOutAndFitsThePanel(t *testing.T) {
	raw, _ := json.Marshal(agentproto.DesiredAck{Frozen: inventory(nil)})
	var ack agentproto.DesiredAck
	if err := json.Unmarshal(raw, &ack); err != nil || ack.Frozen == nil {
		t.Fatalf("%s reads as not frozen (%v)", raw, err)
	}

	list := make([]container.Summary, 3000)
	for i := range list {
		id := fmt.Sprintf("%08x-0000-0000-0000-000000000000", i)
		list[i] = container.Summary{
			Names: []string{"/" + rollout.BlueContainerName(id, "web", "production")}, Image: "registry.example.com/team/app:1.2.3",
			State: container.StateRunning, Labels: map[string]string{rollout.LabelServiceID: id, rollout.LabelDeploymentID: id},
		}
	}
	got := inventory(list)
	raw, _ = json.Marshal(got)
	if len(got) == 0 || len(got) == len(list) || len(raw) > maxInventory {
		t.Fatalf("%d of %d containers listed, %d bytes", len(got), len(list), len(raw))
	}
}

func sameDrift(got, want []agentproto.Drift) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[agentproto.Drift]bool{}
	for _, d := range got {
		seen[d] = true
	}
	for _, d := range want {
		if !seen[d] {
			return false
		}
	}
	return true
}
