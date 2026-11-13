// Package alert watches services, servers and deploys, and posts to each
// workspace's webhook when something breaks and when it is fixed
// (private/specs/2026-10-11-alerts-design.md).
package alert

import (
	"fmt"
	"strings"

	"github.com/Nickpap21/toft/backend/internal/service"
)

// Kind is what an alert is about.
type Kind string

const (
	KindDeployFailed      Kind = "deploy_failed"
	KindOOMKilled         Kind = "oom_killed" // a container killed for memory: a one-off, like a failed deploy
	KindServiceDown       Kind = "service_down"
	KindServiceRestarting Kind = "service_restarting"
	KindServiceUnhealthy  Kind = "service_unhealthy"
	KindContainerExited   Kind = "container_exited"
	KindServerUnreachable Kind = "server_unreachable"
	KindDiskFull          Kind = "disk_full"
	KindVolumeFull        Kind = "volume_full"
	KindBackupFailed      Kind = "backup_failed"
	// The panel's own (D-68), about ControlPlane: a panel container not
	// running or failing its health check, the panel's certificate not
	// renewing, and an upgrade that rolled back or stopped (a one-off).
	KindPanelContainer   Kind = "panel_container"
	KindPanelCertificate Kind = "panel_certificate"
	KindPanelUpgrade     Kind = "panel_upgrade_failed"
	// A repository's import or mirror whose last 3 syncs failed (D-79),
	// about the mirror.
	KindMirrorFailing Kind = "mirror_failing"
	// A server's agent away for 5 minutes (D-106), about the server.
	KindAgentOffline Kind = "agent_offline"
	// A server's agent holding another panel epoch's state (D-113): the
	// panel was restored from a backup, and someone must choose which state
	// to keep. About the server.
	KindAgentFrozen Kind = "agent_frozen"
	// A host's updates (D-133), about a server or ControlPlane: security
	// updates unattended-upgrades isn't installing, a reboot waiting, and
	// a Docker engine a published advisory affects.
	KindSecurityUpdates Kind = "security_updates"
	KindRebootRequired  Kind = "reboot_required"
	KindDockerAdvisory  Kind = "docker_advisory"
)

// serviceKinds are judged from a service's containers.
var serviceKinds = []Kind{KindServiceDown, KindServiceRestarting, KindServiceUnhealthy, KindContainerExited}

// ControlPlane is the subject of alerts about the control-plane server,
// which belongs to no workspace.
const ControlPlane = "control-plane"

// Disk thresholds, in percent used: fire at diskFire or more, resolve under
// diskResolve. In between, the alert stays as it is.
const (
	diskFire    = 85
	diskResolve = 80
)

// Shared volume thresholds (D-38), in percent of the volume's size limit:
// fire at volumeFire or more, resolve under volumeResolve.
const (
	volumeFire    = 80
	volumeResolve = 75
)

// rule is how many consecutive bad samples fire an alert, and how many
// consecutive good ones resolve it.
type rule struct{ fireAfter, resolveAfter int }

// rules: container states are sampled every minute, and a crash loop
// alternates between restarting and running, so they fire on the second bad
// sample and resolve on the third good one. A disk is sampled every five
// minutes, with hysteresis between diskResolve and diskFire instead.
var rules = map[Kind]rule{
	KindServiceDown:       {fireAfter: 2, resolveAfter: 3},
	KindServiceRestarting: {fireAfter: 2, resolveAfter: 3},
	KindServiceUnhealthy:  {fireAfter: 2, resolveAfter: 3},
	KindContainerExited:   {fireAfter: 2, resolveAfter: 3},
	KindServerUnreachable: {fireAfter: 2, resolveAfter: 1},
	KindDiskFull:          {fireAfter: 1, resolveAfter: 1},
	KindVolumeFull:        {fireAfter: 1, resolveAfter: 1},
	KindBackupFailed:      {fireAfter: 1, resolveAfter: 1},
	// The code module counts the failures in a row (code.MirrorFailingAfter).
	KindMirrorFailing: {fireAfter: 1, resolveAfter: 1},
	// An agent counts as away once it has been gone 5 minutes
	// (server.AgentsAway), so the first such sample fires (D-107).
	KindAgentOffline: {fireAfter: 1, resolveAfter: 1},
	KindAgentFrozen:  {fireAfter: 1, resolveAfter: 1},
	// Judged from the last updates read, every 6 hours (server.HostUpdates.Health).
	KindSecurityUpdates: {fireAfter: 1, resolveAfter: 1},
	KindRebootRequired:  {fireAfter: 1, resolveAfter: 1},
	KindDockerAdvisory:  {fireAfter: 1, resolveAfter: 1},
	// Sampled every minute, as services are.
	KindPanelContainer: {fireAfter: 2, resolveAfter: 3},
	// Every five minutes; three in a row, so a certificate Let's Encrypt is
	// issuing right after a domain change doesn't fire.
	KindPanelCertificate: {fireAfter: 3, resolveAfter: 1},
}

// key identifies an alert: one kind, about one subject (a service ID, a
// server ID or ControlPlane).
type key struct {
	kind    Kind
	subject string
}

// tracker counts consecutive bad and good samples per alert and says when an
// alert fires or resolves. open mirrors the open alerts in the database.
// It is not safe for concurrent use: the monitor calls it from one goroutine.
type tracker struct {
	open map[key]bool
	bad  map[key]int
	good map[key]int
}

func newTracker() *tracker {
	return &tracker{open: map[key]bool{}, bad: map[key]int{}, good: map[key]int{}}
}

// observe records one sample of k's condition (bad: it holds) and reports
// whether the alert should now fire or resolve. The caller then records the
// outcome with setOpen once it is stored.
func (t *tracker) observe(k key, bad bool) (fire, resolve bool) {
	r := rules[k.kind]
	if bad {
		delete(t.good, k)
		t.bad[k]++
		return !t.open[k] && t.bad[k] >= r.fireAfter, false
	}
	delete(t.bad, k)
	if !t.open[k] {
		return false, false
	}
	t.good[k]++
	return false, t.good[k] >= r.resolveAfter
}

// setOpen records that k is now open (fired) or closed (resolved).
func (t *tracker) setOpen(k key, open bool) {
	if open {
		t.open[k] = true
		return
	}
	delete(t.open, k)
	delete(t.bad, k)
	delete(t.good, k)
}

// conditions judges a running service from its containers: each kind that
// holds, with a detail for the message. A service with no container at all
// is down. containerHealth is whose check Docker runs in them
// (service.MetricsTarget.ContainerHealth), so an unhealthy container says
// which check failed: Toft's HTTP check (its probe's detail) or Docker's.
func conditions(states []service.ContainerState, containerHealth string) map[Kind]string {
	var running bool
	var restarting, unhealthy, exited []string
	for _, s := range states {
		switch s.State {
		case "running":
			running = true
			if s.Health == "unhealthy" {
				name := s.Name + " (Docker's check: " + dockerCheck(containerHealth) + ")"
				if s.HealthDetail != "" {
					name = s.Name + " (Toft's HTTP check: " + s.HealthDetail + ")"
				}
				unhealthy = append(unhealthy, name)
			}
		case "restarting":
			restarting = append(restarting, s.Name)
		case "exited":
			if s.ExitCode != 0 {
				exited = append(exited, fmt.Sprintf("%s (exit code %d)", s.Name, s.ExitCode))
			}
		case "dead":
			exited = append(exited, s.Name+" (dead)")
		}
	}
	c := map[Kind]string{}
	if !running && len(restarting) == 0 {
		c[KindServiceDown] = "no container is running"
	}
	if len(restarting) > 0 {
		c[KindServiceRestarting] = "restarting in a loop: " + strings.Join(restarting, ", ")
	}
	if len(unhealthy) > 0 {
		c[KindServiceUnhealthy] = "health check failing: " + strings.Join(unhealthy, ", ")
	}
	if len(exited) > 0 {
		c[KindContainerExited] = "exited with an error: " + strings.Join(exited, ", ")
	}
	return c
}

// dockerCheck names the check Docker runs in a service's containers, by its
// container check: a container turned off since it started still runs the
// image's until the next deploy. "" is a compose stack's.
func dockerCheck(containerHealth string) string {
	switch containerHealth {
	case service.ContainerHealthImage:
		return "the image's HEALTHCHECK"
	case service.ContainerHealthCommand:
		return "the service's container check command"
	case service.ContainerHealthOff:
		return "the image's HEALTHCHECK, which the next deploy turns off"
	}
	return "the compose file's or the image's HEALTHCHECK"
}

// fullestDisk returns the fullest of a server's filesystems.
func fullestDisk(usage []service.DiskUsage) (service.DiskUsage, bool) {
	var max service.DiskUsage
	for _, u := range usage {
		if u.UsedPercent >= max.UsedPercent {
			max = u
		}
	}
	return max, len(usage) > 0
}
