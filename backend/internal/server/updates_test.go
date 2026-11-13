package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/advisory"
	"github.com/Nickpap21/toft/backend/internal/apispec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// An Ubuntu 24.04 host as updatesScript read it on 2026-10-30, after apt's
// daily jobs had run, with a kernel waiting for a reboot and Docker's
// repository offering a newer engine (added).
const ubuntuUpdates = `apt:1
aptrc:0
Inst libaudit-common [1:3.1.2-2.1build1.1] (1:3.1.2-2.1ubuntu0.1 Ubuntu:24.04/noble-updates [all])
Inst liblzma5 [5.6.1+really5.4.5-1ubuntu0.3] (5.6.1+really5.4.5-1ubuntu0.4 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [arm64])
Inst docker-ce [5:29.4.3-1~ubuntu.24.04~noble] (5:29.8.2-1~ubuntu.24.04~noble Docker CE:noble [arm64])
Inst linux-image-6.8.0-90-generic (6.8.0-90.91 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [arm64])
Remv linux-image-6.8.0-86-generic [6.8.0-86.87]
lists:1793370046
listsdir:1793370040
uurun:1793370056
uuok:1793370056
uuunit:loaded active
uuerr:
reboot:1793370787
rebootpkg:linux-base
rebootpkg:linux-image-6.8.0-88-generic
docker:29.4.3
dockerpkg:docker-ce
now:1793370789
`

func TestParseUpdates(t *testing.T) {
	// The panel's clock is 10 seconds ahead of the host's.
	now := time.Unix(1793370799, 0).UTC()
	u, err := parseUpdates(ubuntuUpdates, now)
	require.NoError(t, err)
	at := func(n int64) *time.Time { t := time.Unix(n+10, 0).UTC(); return &t }

	assert.Equal(t, now, u.CheckedAt)
	assert.Equal(t, &now, u.ReadAt)
	assert.Equal(t, 2, u.Security)
	assert.Equal(t, 2, u.Other)
	assert.Equal(t, 1, u.Held, "docker-ce waits for Update Docker")
	assert.Equal(t, []string{"linux-image-6.8.0-86-generic"}, u.Removals)
	assert.Equal(t, []PendingPackage{
		{Name: "liblzma5", Current: "5.6.1+really5.4.5-1ubuntu0.3", Candidate: "5.6.1+really5.4.5-1ubuntu0.4", Security: true},
		{Name: "linux-image-6.8.0-90-generic", Current: "", Candidate: "6.8.0-90.91", Security: true},
		{Name: "docker-ce", Current: "5:29.4.3-1~ubuntu.24.04~noble", Candidate: "5:29.8.2-1~ubuntu.24.04~noble"},
		{Name: "libaudit-common", Current: "1:3.1.2-2.1build1.1", Candidate: "1:3.1.2-2.1ubuntu0.1"},
	}, u.Packages)
	assert.False(t, u.Truncated)
	assert.Equal(t, at(1793370046), u.ListsUpdatedAt)
	assert.Equal(t, RebootState{Required: true, Since: at(1793370787), Packages: []string{"linux-base", "linux-image-6.8.0-88-generic"}}, u.Reboot)
	assert.Equal(t, DockerEngine{Version: "29.4.3", Package: "docker-ce", Candidate: "29.8.2", Advisories: []advisory.Advisory{}}, u.Docker)
	assert.Equal(t, Unattended{State: "active", LastRunAt: at(1793370056), LastSuccessAt: at(1793370056), LastResult: "ok"}, u.Unattended)
}

func TestParseUpdatesStates(t *testing.T) {
	now := time.Now().UTC()

	// Debian 12 in a container: no systemd, apt's daily jobs never ran.
	u, err := parseUpdates("apt:1\nInst tzdata [2026c-0+deb12u1] (2026f-0+deb12u1 Debian-Security:12/oldstable-security [all])\nlists:\nuurun:\nuuok:\nuuunit: \nuuerr:\ndocker:\nnow:1793370798\n", now)
	require.NoError(t, err)
	assert.Equal(t, 1, u.Security)
	assert.Nil(t, u.ListsUpdatedAt)
	assert.Equal(t, Unattended{State: "missing", LastResult: "never"}, u.Unattended)
	assert.False(t, u.Reboot.Required)
	assert.Equal(t, []string{}, u.Reboot.Packages)

	// A run that ended without apt's daily job seeing it succeed.
	u, err = parseUpdates("apt:1\nuurun:2000\nuuok:1000\nuuunit:loaded inactive\nuuerr:2026-10-30 06:12:01,000 ERROR Installing the upgrades failed!\nnow:3000\n", now)
	require.NoError(t, err)
	assert.Equal(t, "inactive", u.Unattended.State)
	assert.Equal(t, "failed", u.Unattended.LastResult)
	assert.Contains(t, u.Unattended.LastError, "Installing the upgrades failed")

	// apt that can't work out the updates lists none: a failed read, not
	// an up-to-date host.
	_, err = parseUpdates("apt:1\naptrc:100\napterr:E: The package lists or status file could not be parsed or opened.\nlists:1000\nnow:3000\n", now)
	assert.EqualError(t, err, "apt couldn't work out the pending updates: The package lists or status file could not be parsed or opened.")
	_, err = parseUpdates("apt:1\naptrc:1\nnow:3000\n", now)
	assert.EqualError(t, err, "apt couldn't work out the pending updates: it exited 1")

	// Lists an update by hand brought down after apt's daily job count.
	u, err = parseUpdates("apt:1\naptrc:0\nlists:1000\nlistsdir:2500\nnow:3000\n", time.Unix(3000, 0))
	require.NoError(t, err)
	assert.Equal(t, time.Unix(2500, 0).UTC(), *u.ListsUpdatedAt)

	_, err = parseUpdates("noapt\n", now)
	assert.ErrorIs(t, err, errNoApt)
	assert.Equal(t, "This server doesn't use apt, so Toft can't read its updates.", UpdatesError(err))
	_, err = parseUpdates("sh: 1: base64: not found\n", now)
	assert.Error(t, err)

	var b strings.Builder
	b.WriteString("apt:1\n")
	for i := range maxPendingPackages + 20 {
		fmt.Fprintf(&b, "Inst pkg%03d [1] (2 Ubuntu:24.04/noble-updates [all])\n", i)
	}
	u, err = parseUpdates(b.String(), now)
	require.NoError(t, err)
	assert.Equal(t, maxPendingPackages+20, u.Other)
	assert.Len(t, u.Packages, maxPendingPackages)
	assert.True(t, u.Truncated)
}

// Docker's advisories say nothing of a distribution's docker.io, whose
// maintainers backport fixes under Docker's version.
func TestSetAdvisoriesSkipsDistroDocker(t *testing.T) {
	book := advisory.New("", zap.NewNop())
	u := &HostUpdates{Docker: DockerEngine{Version: "27.5.1", Package: "docker-ce"}}
	u.SetAdvisories(book)
	assert.NotEmpty(t, u.Docker.Advisories)
	u.Docker.Package = "docker.io"
	u.SetAdvisories(book)
	assert.Equal(t, []advisory.Advisory{}, u.Docker.Advisories)
	u.Docker.Package = "" // Docker's static binaries
	u.SetAdvisories(book)
	assert.NotEmpty(t, u.Docker.Advisories)
	u.SetAdvisories(nil)
	assert.Equal(t, []advisory.Advisory{}, u.Docker.Advisories)
}

// What both updates routes answer matches the contract: no read yet, a host
// whose only reads failed, and a full read with advisories.
func TestUpdatesMatchTheContract(t *testing.T) {
	envelope := func(v any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"data": v})
		require.NoError(t, err)
		out := map[string]any{}
		require.NoError(t, json.Unmarshal(raw, &out))
		return out
	}
	failed, err := decodeUpdates([]byte("{}"), "The read couldn't run: dial tcp: i/o timeout", time.Now())
	require.NoError(t, err)
	full, err := parseUpdates(ubuntuUpdates, time.Now())
	require.NoError(t, err)
	full.SetAdvisories(advisory.New("", zap.NewNop()))
	require.NotEmpty(t, full.Docker.Advisories)
	for _, path := range []string{"/api/v1/servers/7c27761d-4b1e-4f0a-9d52-1a2b3c4d5e6f/updates", "/api/v1/instance/panel/updates"} {
		apispec.Check(t, "GET", path, 200, envelope(nil))
		for _, u := range []*HostUpdates{failed, &full} {
			apispec.Check(t, "GET", path, 200, envelope(u))
			apispec.Check(t, "POST", path, 200, envelope(u))
		}
	}
}

func TestUpstreamVersion(t *testing.T) {
	assert.Equal(t, "29.8.2", upstreamVersion("5:29.8.2-1~ubuntu.24.04~noble"))
	assert.Equal(t, "28.2.2", upstreamVersion("28.2.2-0ubuntu1~24.04.1"))
	assert.Equal(t, "29.8.2", upstreamVersion("29.8.2"))
}

func TestUpdatesHealth(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	base := func() *HostUpdates {
		return &HostUpdates{Security: 2, ListsUpdatedAt: ago(6 * time.Hour),
			Unattended: Unattended{State: "active", LastRunAt: ago(7 * time.Hour), LastSuccessAt: ago(7 * time.Hour), LastResult: "ok"}}
	}

	// Waiting for tonight's run: the lists came down after the last one.
	h := base().Health(now)
	assert.True(t, h.Known)
	assert.Empty(t, h.Security)

	u := base()
	u.Unattended.LastRunAt, u.Unattended.LastSuccessAt = ago(5*time.Hour), ago(5*time.Hour)
	assert.Equal(t, "2 security updates waiting: unattended-upgrades ran since the package lists came down and didn't install them", u.Health(now).Security)

	u = base()
	u.Unattended.State = "inactive"
	assert.Contains(t, u.Health(now).Security, "isn't running")
	u = base()
	u.Unattended.LastResult = "failed"
	assert.Contains(t, u.Health(now).Security, "last run failed")
	u = base()
	u.Unattended.LastRunAt, u.Unattended.LastResult = nil, "never"
	assert.Contains(t, u.Health(now).Security, "never run")
	u = base()
	u.Security, u.Unattended.LastRunAt = 1, ago(49*time.Hour)
	assert.Equal(t, "1 security update waiting: unattended-upgrades hasn't run for 2 days", u.Health(now).Security)
	u = base()
	u.Security, u.Unattended.State = 0, "missing"
	assert.Empty(t, u.Health(now).Security, "nothing waits")

	u = base()
	u.Reboot = RebootState{Required: true, Since: ago(time.Hour), Packages: []string{"a", "b", "c", "d"}}
	assert.Equal(t, "a reboot is waiting since 30 Oct 11:00 UTC, for a, b, c and 1 more", u.Health(now).Reboot)

	u = base()
	u.Docker.Version = "29.4.3"
	u.Docker.Advisories = advisory.New("", zap.NewNop()).Affecting("29.4.3")
	assert.Equal(t, "Docker 29.4.3 is affected by 6 published security advisories, the worst high: iptables rules for overlay-network encryption are not reinstalled after a firewalld reload (GHSA-6jhp-7446-hf7f, fixed in 29.9.3)", u.Health(now).Docker)

	assert.False(t, (*HostUpdates)(nil).Health(now).Known)
	assert.False(t, (&HostUpdates{Error: "The read couldn't run: dial"}).Health(now).Known, "no read that worked")
}
