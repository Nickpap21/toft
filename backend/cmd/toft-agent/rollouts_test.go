package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"golang.org/x/crypto/ssh"
)

const testSvc = "1234abcd-0000-0000-0000-000000000001"

func testRollouts(f *fakedocker.Docker) *rollouts {
	m := &rollouts{dc: f.Client(), live: map[string]*liveRollout{}, queues: map[string][]func(){}, idle: make(chan struct{}), resumed: make(chan struct{})}
	close(m.idle)
	m.routes = newRoutes(m.dc, m.busy, activeDeployments)
	m.metrics = newMetrics()
	return m
}

func testRequest(id string) rollout.Request {
	return rollout.Request{Spec: rollout.Spec{
		DeploymentID: id,
		Slot: rollout.Slot{ServiceID: testSvc, ServiceName: "web", Image: "app:2",
			Labels: map[string]string{rollout.LabelServiceID: testSvc, rollout.LabelDeploymentID: id}},
		Plan: netplan.LegacyPlan(testSvc, "web", "", ""),
		Auth: &rollout.RegistryAuth{Server: "ghcr.io", Username: "u", Password: "hunter2"},
	}}
}

// events splits a rollout request's answer into its log lines and result.
func events(t *testing.T, out string) ([]string, *rollout.Result) {
	t.Helper()
	var logs []string
	var res *rollout.Result
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var ev rollout.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if ev.Result != nil {
			res = ev.Result
		} else {
			logs = append(logs, ev.Log)
		}
	}
	return logs, res
}

// The agent runs a rollout it is handed and streams it to its result; the
// same request sent again (the panel restarted) runs nothing twice, and an
// attach or a list finds it.
func TestAgentRunsTheRolloutItIsHanded(t *testing.T) {
	f := fakedocker.New(t)
	stateDir = t.TempDir()
	m := testRollouts(f)
	pem, pub := panelKey(t)
	c, err := connectWith(t, pem, ssh.FingerprintSHA256(pub), m)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	raw, _ := json.Marshal(testRequest("dep-2"))

	out, err := c.SubsystemContext(ctx, agentproto.SubsystemRollout, strings.NewReader(string(raw)+"\n"))
	if err != nil {
		t.Fatalf("rollout: %v", err)
	}
	logs, res := events(t, out)
	if res == nil || res.Step != "" || res.New == "" {
		t.Fatalf("result %+v, log %q", res, logs)
	}
	if !strings.Contains(logs[0], "agent runs this rollout") {
		t.Fatalf("first line %q", logs[0])
	}
	created := len(f.Names())

	again, err := c.SubsystemContext(ctx, agentproto.SubsystemRollout, strings.NewReader(string(raw)+"\n"))
	if err != nil || again != out {
		t.Fatalf("a resent request must answer the same rollout: %q %v", again, err)
	}
	if len(f.Names()) != created {
		t.Fatal("a resent request ran the rollout again")
	}

	tail, err := c.SubsystemContext(ctx, agentproto.SubsystemRolloutAttach, strings.NewReader(`{"ID":"dep-2","Offset":1}`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := events(t, tail); len(got) != len(logs)-1 {
		t.Fatalf("attach from line 1: %d lines, want %d", len(got), len(logs)-1)
	}

	listed, err := c.SubsystemContext(ctx, agentproto.SubsystemRolloutList, nil)
	if err != nil {
		t.Fatal(err)
	}
	var sums []rollout.Summary
	if err := json.Unmarshal([]byte(listed), &sums); err != nil || len(sums) != 1 || sums[0].ID != "dep-2" || sums[0].Result == nil {
		t.Fatalf("list %q %v", listed, err)
	}
	rec, _ := os.ReadFile(recordFile("dep-2"))
	if strings.Contains(string(rec), `"Request"`) {
		t.Fatal("an ended rollout's record must drop its request (the service's environment)")
	}

	if out, err := c.SubsystemContext(ctx, agentproto.SubsystemRolloutAttach, strings.NewReader(`{"ID":"../x"}`+"\n")); err == nil {
		t.Fatalf("a bad ID must be refused: %q", out)
	}
}

// A rollout the agent's restart interrupted resumes from its journal, which
// never held the registry's password.
func TestAgentResumesAnInterruptedRollout(t *testing.T) {
	f := fakedocker.New(t)
	stateDir = t.TempDir()
	m := testRollouts(f)
	// The service's queue is held, so the rollout is journaled but never runs:
	// the agent stopped.
	m.queues[testSvc] = []func(){func() {}}
	if err := m.start(testRequest("dep-3")); err != nil {
		t.Fatal(err)
	}
	rec, _ := os.ReadFile(recordFile("dep-3"))
	if !strings.Contains(string(rec), `"Request"`) || strings.Contains(string(rec), "hunter2") {
		t.Fatalf("the journal holds the request without the password: %s", rec)
	}

	m = testRollouts(f)
	m.resume(context.Background())
	<-m.whenIdle()
	got, err := readRecord("dep-3")
	if err != nil || got.Progress.Result == nil || got.Progress.Result.Step != "" {
		t.Fatalf("resumed: %+v %v", got.Progress, err)
	}
	if got.Request != nil {
		t.Fatal("the ended rollout keeps no request")
	}
}

// The deploy's time limit travels in the journal: a rollout resumed past
// it ends there, worded as a timeout (D-125).
func TestAgentKeepsTheDeployTimeLimitAcrossAResume(t *testing.T) {
	f := fakedocker.New(t)
	stateDir = t.TempDir()
	m := testRollouts(f)
	m.queues[testSvc] = []func(){func() {}}
	req := testRequest("dep-4")
	req.TimeLeft = time.Millisecond
	if err := m.start(req); err != nil {
		t.Fatal(err)
	}
	rec, err := readRecord("dep-4")
	if err != nil || rec.Deadline.Sub(rec.Created) != time.Millisecond {
		t.Fatalf("the journal keeps the deadline: %+v %v", rec, err)
	}
	time.Sleep(5 * time.Millisecond)

	m = testRollouts(f)
	m.resume(context.Background())
	<-m.whenIdle()
	got, err := readRecord("dep-4")
	if err != nil || got.Progress.Result == nil || !strings.HasPrefix(got.Progress.Result.Err, "timed out at the deploy time limit (at the ") {
		t.Fatalf("resumed past the limit: %+v %v", got.Progress.Result, err)
	}
}

// After a reboot the agent can start before Docker: it resumes once Docker
// answers, and a rollout it holds no way to finish then reads as unknown,
// so the panel stops waiting for it.
func TestAgentResumesOnceDockerAnswers(t *testing.T) {
	f := fakedocker.New(t)
	stateDir = t.TempDir()
	m := testRollouts(f)
	m.queues[testSvc] = []func(){func() {}}
	if err := m.start(testRequest("dep-3")); err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(record{ID: "dep-9", ServiceID: testSvc, Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	appendLogLine("dep-9", "a rollout whose request was lost")

	f.Mu.Lock()
	f.PingDown = true
	f.Mu.Unlock()
	m = testRollouts(f)
	pem, pub := panelKey(t)
	c, err := connectWith(t, pem, ssh.FingerprintSHA256(pub), m)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	attach := func() error {
		_, err := c.SubsystemContext(ctx, agentproto.SubsystemRolloutAttach, strings.NewReader(`{"ID":"dep-9"}`+"\n"))
		return err
	}
	go m.resume(ctx)

	time.Sleep(200 * time.Millisecond)
	m.mu.Lock()
	queued := len(m.live)
	m.mu.Unlock()
	if queued != 0 {
		t.Fatal("nothing resumes before Docker answers")
	}
	if err := attach(); err == nil || strings.Contains(err.Error(), rollout.UnknownRolloutPrefix) {
		t.Fatalf("before the resume a rollout without a result may still resume: %v", err)
	}

	f.Mu.Lock()
	f.PingDown = false
	f.Mu.Unlock()
	<-m.resumed
	<-m.whenIdle()
	if got, err := readRecord("dep-3"); err != nil || got.Progress.Result == nil {
		t.Fatalf("resumed once Docker answered: %+v %v", got.Progress, err)
	}
	if err := attach(); err == nil || !strings.Contains(err.Error(), rollout.UnknownRolloutPrefix) {
		t.Fatalf("after the resume nothing finishes it: %v", err)
	}
}

// The agent says it applies a service's container check, so the panel
// hands it rollouts with one (an agent that doesn't is refused them).
func TestAgentAnnouncesTheContainerCheck(t *testing.T) {
	if !slices.Contains(strings.Fields((&rollouts{}).caps()), agentproto.CapContainerCheck) {
		t.Fatalf("caps %q leave out %s", (&rollouts{}).caps(), agentproto.CapContainerCheck)
	}
}
