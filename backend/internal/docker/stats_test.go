package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/internal/hoststats"
	"github.com/Nickpap21/toft/backend/internal/rollout"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

const (
	appID    = "aaaa1111-0000-0000-0000-000000000001" // blue/green service "app"
	stackID  = "bbbb2222-0000-0000-0000-000000000002" // compose service "stack"
	victimID = "cccc3333-0000-0000-0000-000000000003" // blue/green service "victim"
	strayID  = "eeee5555-0000-0000-0000-000000000005" // a service that is not a target
)

var (
	appBlue       = rollout.BlueContainerName(appID, "app", "production")
	appGreen      = rollout.GreenContainerName(appID, "app", "production")
	victimBlue    = rollout.BlueContainerName(victimID, "victim", "")
	stackProject  = rollout.ComposeProjectName(stackID, "stack") // paas-stack-bbbb2222
	stackLegacy   = "paas-bbbb2222"
	statsTargets  = []service.MetricsTarget{{ID: appID, Name: "app"}, {ID: stackID, Name: "stack"}, {ID: victimID, Name: "victim"}}
	approxStatsMB = 0.01
)

// listing is a line of a container listing, as docker prints it: each
// field a JSON string. hostStatesCmd prints the names and two labels;
// hoststats.Cmd adds the compose service label and the full ID (more).
func listing(names, serviceLabel, project string, more ...string) string {
	fields := append([]string{names, serviceLabel, project}, more...)
	for i, f := range fields {
		b, err := json.Marshal(f)
		if err != nil {
			panic(err)
		}
		fields[i] = string(b)
	}
	return strings.Join(fields, "\t")
}

// Kill lines exactly as docker events 29.6.2 printed them through
// hoststats.KillFormat, from real memory kills (a 16 MiB container running tail
// /dev/zero): a slot of victim, and a container of the stack whose labels
// forge sections. Real tabs separate the fields; the tabs and newlines of
// a label are JSON-escaped (backslash, t).
const (
	recordedKillAt     = 1791821483
	recordedKillUntil  = "until\t1791821490"
	recordedVictimKill = "\"paas-victim-cccc3333-blue\"\t\"cccc3333-0000-0000-0000-000000000003\"\t\"\"\t\"\"\t\"db636b84538674f451a2a9465eb08b00d045417181b8a582edd6e6d2192db11a\"\t1791821483"
	recordedForgedKill = "\"paas-stack-bbbb2222-web-1\"\t\"x\\t@@\\n@@\\n0000000000000000000000000000000000000000000000000000000000000002\\t999999999999\\t1\\t1\\t100.00\\nhost\\t100.00\\n@@\\nuntil\\t9999999999\"\t\"paas-stack-bbbb2222\"\t\"x\\t@@\\n@@\\n0000000000000000000000000000000000000000000000000000000000000002\\t999999999999\\t1\\t1\\t100.00\\nhost\\t100.00\\n@@\\nuntil\\t9999999999\"\t\"a441badde1b7be583a87126e171315fe45a49dbdb0d7281fd423d1e39f0e55da\"\t1791821483"
)

// cid is a full container ID, as docker ps --no-trunc prints it.
func cid(n int) string { return fmt.Sprintf("%064x", n) }

func pct(v float64) *float64 { return &v }

func statRow(name, serviceLabel, project string, cpu, mem float64) hostStatRow {
	return hostStatRow{hostContainer: hostContainer{Name: name, ServiceID: serviceLabel, ComposeProject: project}, Stats: service.ContainerStats{CPUPercent: cpu, MemoryMB: mem}}
}

func buildStep(name, image, project string, cpu, mem float64) hostStatRow {
	r := statRow(name, "", project, cpu, mem)
	r.Image = image
	return r
}

// sumPerService attributes rows and sums each service's CPU and memory, as
// the collector stores them.
func sumPerService(rows []hostStatRow, targets []service.MetricsTarget) map[string]service.ContainerStats {
	return totals(attributeSample(hostSample{rows: rows}, targets))
}

func totals(h service.HostSample) map[string]service.ContainerStats {
	out := make(map[string]service.ContainerStats)
	for id, byCompose := range h.Containers {
		var sum service.ContainerStats
		for _, cs := range byCompose {
			for _, c := range cs {
				sum.CPUPercent += c.CPUPercent
				sum.MemoryMB += c.MemoryMB
			}
		}
		out[id] = service.ContainerStats{CPUPercent: round2(sum.CPUPercent), MemoryMB: round2(sum.MemoryMB)}
	}
	return out
}

// ─── parseHostStats ───────────────────────────────────────────────────────────

// An output with only the listing (as hostStatesCmd prints it, without the
// compose service and ID) and docker stats: CPU and memory, nothing else.
func TestParseHostStatsWithoutCountersOrKills(t *testing.T) {
	out := strings.Join([]string{
		listing(appBlue, appID, ""),
		listing(appGreen, appID, ""),
		listing("paas-stack-bbbb2222-web-1", "", stackProject),
		listing("paas-bbbb2222-db-1", "", stackLegacy) + "\r",
		listing("web,proxy/web", "", stackProject),
		listing("traefik", "", ""),
		"WARNING: a CLI warning on stderr",
		"",
		hoststats.Separator,
		appBlue + "\t0.50%\t45.2MiB / 1.95GiB",
		appGreen + "\t12.25%\t1.5GiB / 1.95GiB",
		"paas-stack-bbbb2222-web-1\t3.00%\t512kB / 1.95GiB",
		"paas-bbbb2222-db-1\t--\t-- / --", // stopping: no sample
		"web\t1%\t1024B / 1GiB",
		"traefik\t0.01%\t20MiB / 1.95GiB",
		"started-after-the-listing\t1%\t1MiB / 1GiB",
		"",
		"garbage line",
	}, "\n")

	s, err := parseHostStats(out)
	require.NoError(t, err)
	rows := s.rows
	require.Len(t, rows, 5)
	assert.Nil(t, s.cpuWaitPct)
	assert.Empty(t, s.kills)
	assert.Zero(t, s.until)

	assert.Equal(t, statRow(appBlue, appID, "", 0.5, 45.2), rows[0])
	assert.Equal(t, statRow(appGreen, appID, "", 12.25, 1536), rows[1])
	assert.Equal(t, statRow("paas-stack-bbbb2222-web-1", "", stackProject, 3, 0.5), rows[2])
	assert.Equal(t, "web", rows[3].Name, "the container's own name, not its link alias")
	assert.InDelta(t, 1.0/1024, rows[3].Stats.MemoryMB, 1e-9)
	assert.Equal(t, statRow("traefik", "", "", 0.01, 20), rows[4])
}

func TestParseHostStatsWithLabelsThatForgeLines(t *testing.T) {
	// Image labels are tenant-controlled and Docker does not validate them:
	// a value with newlines and tabs tries to add a listing line for the
	// victim's slot, an early separator and a sample.
	forged := "x\n" + victimBlue + "\t" + victimID + "\t\n" + hoststats.Separator + "\n" + victimBlue + "\t999.00%\t60GiB / 64GiB"
	out := strings.Join([]string{
		listing("attacker-web-1", forged, "attacker"),                 // the newest container is listed first
		listing("attacker-web-2", "", "x\n"+hoststats.Separator+"\n"), // tries to cut the listing short
		listing(appBlue, appID, ""),
		listing("tabbed", appID+"\t"+appID, "a\tb"),
		hoststats.Separator,
		"attacker-web-1\t1.00%\t10MiB / 1GiB",
		"attacker-web-2\t1.00%\t10MiB / 1GiB",
		appBlue + "\t0.50%\t45.2MiB / 1.95GiB",
		"tabbed\t1.00%\t1MiB / 1GiB",
	}, "\n")

	s, err := parseHostStats(out)
	require.NoError(t, err)
	rows := s.rows
	require.Len(t, rows, 4, "every real container is still listed and sampled")
	assert.Equal(t, statRow("attacker-web-1", "", "attacker", 1, 10), rows[0], "a label that is not a UUID is not kept")
	assert.Equal(t, statRow("attacker-web-2", "", "", 1, 10), rows[1])
	assert.Equal(t, statRow(appBlue, appID, "", 0.5, 45.2), rows[2])
	assert.Equal(t, statRow("tabbed", "", "", 1, 1), rows[3])

	got := sumPerService(rows, statsTargets)
	assert.Equal(t, map[string]service.ContainerStats{appID: {CPUPercent: 0.5, MemoryMB: 45.2}}, got,
		"nothing is attributed to the victim, and the app's sample survives")
}

func TestParseHostStatsWithNothingRunning(t *testing.T) {
	s, err := parseHostStats(hoststats.Separator)
	require.NoError(t, err)
	assert.Empty(t, s.rows)
}

func TestParseHostStatsNeedsTheListing(t *testing.T) {
	_, err := parseHostStats("Cannot connect to the Docker daemon")
	assert.Error(t, err)
}

// hostOutput is hoststats.Cmd's output on a server with kernel 6.8 (every
// counter, PSI): a blue/green slot and two compose services, one of which
// is no longer in the counters, and a memory kill of a slot that did not
// restart. The counter lines are the same under the systemd and cgroupfs
// drivers: the command finds the cgroup.
var hostOutput = strings.Join([]string{
	listing(appBlue, appID, "", "", cid(1)),
	listing(stackProject+"-web-1", "", stackProject, "web", cid(2)),
	listing(stackProject+"-db-1", "", stackProject, "db", cid(3)),
	hoststats.Separator,
	appBlue + "\t0.50%\t45.2MiB / 1.95GiB",
	stackProject + "-web-1\t3.00%\t100MiB / 512MiB",
	stackProject + "-db-1\t1.00%\t200MiB / 1GiB",
	hoststats.Separator,
	cid(1) + "\t1200\t300\t12.50",
	cid(2) + "\t0\t0\t0.00",
	cid(9) + "\t10\t10\t99.00", // started after the listing
	"host\t3.25",
	hoststats.Separator,
	recordedVictimKill,
	recordedKillUntil,
}, "\n")

func TestParseHostStatsReadsCountersAndKills(t *testing.T) {
	s, err := parseHostStats(hostOutput)
	require.NoError(t, err)

	require.Len(t, s.rows, 3)
	assert.Equal(t, hostContainer{ID: cid(1), Name: appBlue, ServiceID: appID}, s.rows[0].hostContainer)
	assert.Equal(t, service.ContainerStats{CPUPercent: 0.5, MemoryMB: 45.2, NrPeriods: 1200, NrThrottled: 300, CPUWaitPct: pct(12.5)}, s.rows[0].Stats)
	assert.Equal(t, hostContainer{ID: cid(2), Name: stackProject + "-web-1", ComposeProject: stackProject, ComposeService: "web"}, s.rows[1].hostContainer)
	assert.Equal(t, service.ContainerStats{CPUPercent: 3, MemoryMB: 100, CPUWaitPct: pct(0)}, s.rows[1].Stats)
	assert.Equal(t, service.ContainerStats{CPUPercent: 1, MemoryMB: 200}, s.rows[2].Stats, "gone from the counters: none read")

	assert.Equal(t, pct(3.25), s.cpuWaitPct)
	assert.Equal(t, int64(1791821490), s.until)
	assert.Equal(t, []hostKill{{
		hostContainer: hostContainer{ID: "db636b84538674f451a2a9465eb08b00d045417181b8a582edd6e6d2192db11a", Name: victimBlue, ServiceID: victimID},
		At:            time.Unix(recordedKillAt, 0).UTC(),
	}}, s.kills)
}

func TestParseHostStatsWithCRLF(t *testing.T) {
	want, err := parseHostStats(hostOutput)
	require.NoError(t, err)
	got, err := parseHostStats(strings.ReplaceAll(hostOutput, "\n", "\r\n"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// The fifth section's restart counts go on the rows by ID; a row without
// one (stopped first, or an older command) stays unread, and the kill
// window's end is still read from the fourth.
func TestParseHostStatsReadsRestartCounts(t *testing.T) {
	out := hostOutput + "\n" + strings.Join([]string{
		hoststats.Separator,
		cid(1) + " 3",
		cid(2) + " 0",
		cid(9) + " 7", // started after the listing
		cid(3) + " -1",
	}, "\n")
	s, err := parseHostStats(out)
	require.NoError(t, err)
	require.Len(t, s.rows, 3)
	assert.Equal(t, []int{3, 0, 0}, []int{s.rows[0].Stats.RestartCount, s.rows[1].Stats.RestartCount, s.rows[2].Stats.RestartCount})
	assert.Equal(t, []bool{true, true, false}, []bool{s.rows[0].Stats.RestartsRead, s.rows[1].Stats.RestartsRead, s.rows[2].Stats.RestartsRead})
	assert.Equal(t, int64(1791821490), s.until)
	assert.Equal(t, int64(1791821490), hoststats.Until(out), "the agent reads the window's end from the kill section")
	assert.Equal(t, int64(1791821490), hoststats.Until(hostOutput), "output without the restart section")

	old, err := parseHostStats(hostOutput)
	require.NoError(t, err)
	for _, r := range old.rows {
		assert.False(t, r.Stats.RestartsRead, "an older command prints no restart counts")
	}
}

func TestParseHostStatsLeavesUnreadCountersEmpty(t *testing.T) {
	out := strings.Join([]string{
		listing(appBlue, appID, "", "", cid(1)),
		listing(appGreen, appID, "", "", cid(2)),
		listing("short-id", "", "", "", "abc123"),
		hoststats.Separator,
		appBlue + "\t1%\t1MiB / 1GiB",
		appGreen + "\t1%\t1MiB / 1GiB",
		"short-id\t1%\t1MiB / 1GiB",
		hoststats.Separator,
		cid(1) + "\t-\t-\t-", // cgroup v1, or the container stopped while read
		cid(2) + "\t1\t2\tNaN",
		"abc123\t1\t1\t1.00", // not a full ID
		cid(3) + "\t1\t1",    // cut short
		"host\t-",            // a kernel without PSI
		hoststats.Separator,
		"Error response from daemon: a CLI error on stderr",
	}, "\n")

	s, err := parseHostStats(out)
	require.NoError(t, err)
	require.Len(t, s.rows, 3)
	assert.Equal(t, service.ContainerStats{CPUPercent: 1, MemoryMB: 1}, s.rows[0].Stats)
	assert.Equal(t, service.ContainerStats{CPUPercent: 1, MemoryMB: 1, NrPeriods: 1, NrThrottled: 2}, s.rows[1].Stats)
	assert.Equal(t, "", s.rows[2].ID, "an ID that is not 64 hex digits is not kept")
	assert.Equal(t, service.ContainerStats{CPUPercent: 1, MemoryMB: 1}, s.rows[2].Stats)
	assert.Nil(t, s.cpuWaitPct)
	assert.Empty(t, s.kills)
	assert.Zero(t, s.until, "docker events failed: the window is not closed")
}

func TestParseHostStatsWithoutTheKillSection(t *testing.T) {
	out := strings.Join([]string{
		listing(appBlue, appID, "", "", cid(1)),
		hoststats.Separator,
		appBlue + "\t1%\t1MiB / 1GiB",
		hoststats.Separator,
		cid(1) + "\t10\t1\t2.00",
		"host\t5.00",
	}, "\n")
	s, err := parseHostStats(out)
	require.NoError(t, err)
	require.Len(t, s.rows, 1)
	assert.Equal(t, uint64(10), s.rows[0].Stats.NrPeriods)
	assert.Equal(t, pct(5), s.cpuWaitPct)
	assert.Empty(t, s.kills)
	assert.Zero(t, s.until)
}

func TestParseHostStatsWithALabelThatForgesSections(t *testing.T) {
	// A tenant's image labels its containers with a value that tries to end
	// the listing, add counters for its container and the host, and close
	// the kill window far in the future (recordedForgedKill carries it too).
	forged := "x\t" + hoststats.Separator + "\n" + hoststats.Separator + "\n" +
		cid(2) + "\t999999999999\t1\t1\t100.00\nhost\t100.00\n" + hoststats.Separator + "\nuntil\t9999999999"
	web := stackProject + "-web-1"
	out := strings.Join([]string{
		listing(web, forged, stackProject, forged, cid(2)),
		hoststats.Separator,
		web + "\t1.00%\t10MiB / 1GiB",
		hoststats.Separator,
		cid(2) + "\t10\t1\t2.00",
		"host\t5.00",
		hoststats.Separator,
		recordedForgedKill,
		recordedKillUntil,
	}, "\n")

	s, err := parseHostStats(out)
	require.NoError(t, err)
	require.Len(t, s.rows, 1)
	assert.Equal(t, service.ContainerStats{CPUPercent: 1, MemoryMB: 10, NrPeriods: 10, NrThrottled: 1, CPUWaitPct: pct(2)}, s.rows[0].Stats)
	assert.Equal(t, pct(5), s.cpuWaitPct)
	assert.Equal(t, int64(1791821490), s.until)
	require.Len(t, s.kills, 1)
	assert.Equal(t, hostContainer{ID: "a441badde1b7be583a87126e171315fe45a49dbdb0d7281fd423d1e39f0e55da", Name: web, ComposeProject: stackProject, ComposeService: forged}, s.kills[0].hostContainer)

	got := attributeSample(s, statsTargets)
	assert.Equal(t, map[string]map[string][]service.ContainerStats{
		stackID: {"": {{ContainerID: cid(2), CPUPercent: 1, MemoryMB: 10, NrPeriods: 10, NrThrottled: 1, CPUWaitPct: pct(2)}}},
	}, got.Containers, "the forged compose service is not a compose service name")
	assert.Equal(t, []service.OOMEvent{{ServiceID: stackID, Container: web, At: time.Unix(recordedKillAt, 0).UTC()}}, got.OOMKills)
}

// docker events, unlike docker ps and docker stats, does not turn a literal
// \t in --format into a tab (the kill lines would be one field each, and
// every kill dropped): each separator must be template output, {{"\t"}}.
func TestHostKillFormatPrintsRealTabs(t *testing.T) {
	outside := regexp.MustCompile(`\{\{.*?\}\}`).ReplaceAllString(hoststats.KillFormat, "")
	assert.NotContains(t, outside, `\`, "a backslash outside {{…}} reaches the output as is")
	assert.Equal(t, 5, strings.Count(hoststats.KillFormat, `{{"\t"}}`), "six fields")
	assert.Contains(t, hoststats.Cmd(0), "--format '"+hoststats.KillFormat+"'")
}

func TestParseDockerSizeUnits(t *testing.T) {
	for in, want := range map[string]float64{
		"1.5GiB": 1536, "45.2MiB": 45.2, "512KiB": 0.5, "1TiB": 1024 * 1024,
		"2GB": 2048, "3MB": 3, "512kB": 0.5, "1048576B": 1,
	} {
		got, err := parseDockerSize(in)
		require.NoError(t, err, in)
		assert.InDelta(t, want, got, 1e-9, in)
	}
	_, err := parseDockerSize("--")
	assert.Error(t, err)
}

// ─── Attribution ──────────────────────────────────────────────────────────────

func TestIsSlotName(t *testing.T) {
	yes := []string{
		rollout.BlueContainerName(appID, "app", ""),
		rollout.GreenContainerName(appID, "app", "production"),
		rollout.BlueContainerName(appID, "old-name", "staging"), // left by a rename
		rollout.BlueContainerName(appID, "", ""),                // paas-svc-<id>-blue
		rollout.ContainerNameForService(appID),                  // pre-blue/green
	}
	for _, name := range yes {
		assert.True(t, rollout.IsSlotName(name, appID), name)
	}
	no := []string{
		"paas-stack-bbbb2222-web-1",               // compose's own naming
		"paas-app-aaaa1111-blue-1",                // compose service named "blue"
		"app-aaaa1111-blue",                       // a compose container_name (paas-* is reserved)
		"eager_turing",                            // an unnamed container
		victimBlue,                                // another service's slot
		rollout.ContainerNameForService(victimID), // another service's legacy name
		"paas-svc-" + appID + "-extra",            // not a name the platform gives
	}
	for _, name := range no {
		assert.False(t, rollout.IsSlotName(name, appID), name)
	}
	assert.False(t, rollout.IsSlotName(rollout.BlueContainerName(appID, "app", ""), ""))
}

func TestAttributeStatsSumsEachServicesContainers(t *testing.T) {
	got := sumPerService([]hostStatRow{
		statRow(appBlue, appID, "", 0.5, 45.2),    // a switch in progress:
		statRow(appGreen, appID, "", 12.25, 1536), // both slots count
		statRow(stackProject+"-web-1", "", stackProject, 3, 0.5),
		statRow(stackLegacy+"-db-1", "", stackLegacy, 1, 100), // the legacy project name
		statRow("traefik", "", "", 50, 20),
	}, statsTargets)

	require.Len(t, got, 2, "victim runs nothing, traefik is nobody's")
	assert.InDelta(t, 12.75, got[appID].CPUPercent, 1e-9)
	assert.InDelta(t, 1581.2, got[appID].MemoryMB, approxStatsMB)
	assert.InDelta(t, 4, got[stackID].CPUPercent, 1e-9)
	assert.InDelta(t, 100.5, got[stackID].MemoryMB, 1e-9)
	assert.Zero(t, got[appID].MemoryLimitMB, "limits do not add up")
}

// A tenant chooses its images, and a container inherits every LABEL of its
// image. No such label may move usage between services.
func TestAttributeStatsTrustsOnlyPlatformLabels(t *testing.T) {
	got := sumPerService([]hostStatRow{
		// The stack's own container, whose image claims to be victim's.
		statRow(stackProject+"-evil-1", victimID, stackProject, 7, 70),
		// A migrate-command container from the stack's image, claiming victim.
		statRow("eager_turing", victimID, "", 9, 90),
		// A container named like victim's slot but labelled app.
		statRow(victimBlue, appID, "", 11, 110),
		// app's slot, whose image claims the stack's compose project.
		statRow(appBlue, appID, stackProject, 1, 10),
		// A slot of a service that is not sampled, whose image claims the stack.
		statRow(rollout.BlueContainerName(strayID, "stray", ""), strayID, stackProject, 13, 130),
		// A build step of the control plane's builder, whose base image
		// claims the stack (A4).
		buildStep("sweet_allen", "295e12ea72db", stackProject, 17, 170),
		buildStep("eager_hopper", "sha256:295e12ea72dbdf9a98cbdffaa08e5eaa2bac3caa7a222252db6b8c5c8c3ddb18", stackProject, 19, 190),
	}, statsTargets)

	assert.Equal(t, map[string]service.ContainerStats{
		stackID: {CPUPercent: 7, MemoryMB: 70},
		appID:   {CPUPercent: 1, MemoryMB: 10},
	}, got)
	_, ok := got[victimID]
	assert.False(t, ok, "nothing is counted for victim")
}

func TestAttributeStatsIgnoresAProjectNameTwoServicesDerive(t *testing.T) {
	// Same 8-character ID prefix and same name: the same project names.
	one := service.MetricsTarget{ID: "dddd4444-0000-0000-0000-000000000001", Name: "x"}
	two := service.MetricsTarget{ID: "dddd4444-0000-0000-0000-000000000002", Name: "x"}
	got := sumPerService([]hostStatRow{
		statRow("paas-x-dddd4444-web-1", "", "paas-x-dddd4444", 5, 50),
		statRow("paas-dddd4444-web-1", "", "paas-dddd4444", 5, 50),
		statRow(rollout.GreenContainerName(two.ID, "x", ""), two.ID, "", 2, 20),
	}, []service.MetricsTarget{one, two})

	assert.Equal(t, map[string]service.ContainerStats{two.ID: {CPUPercent: 2, MemoryMB: 20}}, got)
}

func TestAttributeStatsWithNoTargets(t *testing.T) {
	assert.Empty(t, sumPerService([]hostStatRow{statRow(appBlue, appID, "", 1, 1)}, nil))
}

func TestAttributeSampleKeepsEachContainerUnderItsComposeService(t *testing.T) {
	row := func(id, name, serviceLabel, project, composeService string, periods uint64) hostStatRow {
		return hostStatRow{
			hostContainer: hostContainer{ID: id, Name: name, ServiceID: serviceLabel, ComposeProject: project, ComposeService: composeService},
			Stats:         service.ContainerStats{CPUPercent: 1, MemoryMB: 10, NrPeriods: periods},
		}
	}
	got := attributeSample(hostSample{rows: []hostStatRow{
		row(cid(1), appBlue, appID, "", "web", 1), // a slot: its image's compose label does not count
		row(cid(2), appGreen, appID, "", "", 2),   // the other slot, in a switch
		row(cid(3), stackProject+"-web-1", "", stackProject, "web", 3),
		row(cid(4), stackProject+"-web-2", "", stackProject, "web", 4), // a replica: counters not added up
		row(cid(5), stackLegacy+"-db-1", "", stackLegacy, "db", 5),
		row(cid(6), stackProject+"-x-1", "", stackProject, "bad name", 6), // not a compose service name
		row(cid(7), "traefik", "", "", "", 7),
	}}, statsTargets)

	stats := func(id, composeService string, periods uint64) service.ContainerStats {
		return service.ContainerStats{ContainerID: id, ComposeService: composeService, CPUPercent: 1, MemoryMB: 10, NrPeriods: periods}
	}
	assert.Equal(t, map[string]map[string][]service.ContainerStats{
		appID: {"": {stats(cid(1), "", 1), stats(cid(2), "", 2)}},
		stackID: {
			"web": {stats(cid(3), "web", 3), stats(cid(4), "web", 4)},
			"db":  {stats(cid(5), "db", 5)},
			"":    {stats(cid(6), "", 6)},
		},
	}, got.Containers)
}

// A container killed for memory that does not restart is gone from the
// listing: its event is attributed by the same rules, from the name and
// labels it carries.
func TestAttributeSampleKills(t *testing.T) {
	at := time.Unix(1760000030, 0).UTC()
	kill := func(name, serviceLabel, project, composeService string) hostKill {
		return hostKill{hostContainer: hostContainer{ID: cid(1), Name: name, ServiceID: serviceLabel, ComposeProject: project, ComposeService: composeService}, At: at}
	}
	got := attributeSample(hostSample{cpuWaitPct: pct(4), until: 1760000060, kills: []hostKill{
		kill(appBlue, appID, "", "web"),
		kill(stackProject+"-db-1", "", stackProject, "db"),
		kill(stackProject+"-evil-1", victimID, stackProject, "evil"),                 // its image claims victim
		kill("eager_turing", victimID, "", ""),                                       // claims victim by label alone
		kill(rollout.BlueContainerName(strayID, "stray", ""), strayID, "", ""),       // not a target
		kill("traefik", "", "", ""),                                                  // nobody's
		kill(stackProject+"-web-1", "", "paas-other-12345678", "web"),                // another stack
		kill(rollout.BlueContainerName(victimID, "victim", ""), appID, "", "victim"), // victim's slot name, app's label
	}}, statsTargets)

	assert.Equal(t, []service.OOMEvent{
		{ServiceID: appID, Container: appBlue, At: at},
		{ServiceID: stackID, ComposeService: "db", Container: stackProject + "-db-1", At: at},
		{ServiceID: stackID, ComposeService: "evil", Container: stackProject + "-evil-1", At: at},
	}, got.OOMKills)
	assert.Equal(t, pct(4), got.CPUWaitPct)
	assert.Equal(t, int64(1760000060), got.Until)
	assert.Empty(t, got.Containers)
}

// ─── Remote ───────────────────────────────────────────────────────────────────

func TestRemoteCollectServiceStatsRunsOneCommand(t *testing.T) {
	h := &fakeHost{exec: func(context.Context, string) (string, error) { return hostOutput, nil }}

	got, err := newTestRemote(h).CollectServiceStats(context.Background(), statsTargets, 1760000000)
	require.NoError(t, err)
	assert.Equal(t, map[string]service.ContainerStats{
		appID:   {CPUPercent: 0.5, MemoryMB: 45.2},
		stackID: {CPUPercent: 4, MemoryMB: 300},
	}, totals(got))
	assert.Equal(t, []service.ContainerStats{{ContainerID: cid(2), ComposeService: "web", CPUPercent: 3, MemoryMB: 100, CPUWaitPct: pct(0)}}, got.Containers[stackID]["web"])
	assert.Equal(t, pct(3.25), got.CPUWaitPct)
	assert.Equal(t, int64(1791821490), got.Until)
	assert.Equal(t, []service.OOMEvent{{ServiceID: victimID, Container: victimBlue, At: time.Unix(recordedKillAt, 0).UTC()}}, got.OOMKills,
		"victim's slot was killed and is gone from the listing")

	calls := h.recorded()
	require.Len(t, calls, 1, "one exec per server, whatever the number of services")
	assert.Equal(t, "exec", calls[0].kind)
	cmd := calls[0].cmd
	assert.Equal(t, hoststats.Cmd(1760000000), cmd)
	assert.Contains(t, cmd, `docker ps --no-trunc --format '{{json .Names}}\t{{json (.Label "paas.service_id")}}\t{{json (.Label "com.docker.compose.project")}}\t{{json (.Label "com.docker.compose.service")}}\t{{json .ID}}'`)
	assert.Contains(t, cmd, "docker stats --no-stream")
	assert.Contains(t, cmd, "docker ps -q --no-trunc")
	assert.Contains(t, cmd, "/sys/fs/cgroup/system.slice/docker-$id.scope", "the systemd cgroup driver")
	assert.Contains(t, cmd, "/sys/fs/cgroup/docker/$id", "the cgroupfs cgroup driver")
	assert.Contains(t, cmd, "/proc/pressure/cpu")
	assert.NotContains(t, cmd, "memory.peak", "it counts page cache: a database's peak would be its cache")
	assert.Contains(t, cmd, "now=$(date +%s)", "the window ends on the server's clock")
	assert.Contains(t, cmd, "s=1760000000 ")
	assert.Contains(t, cmd, `docker events --since "$s" --until "$now" --filter event=oom`)
	assert.Contains(t, cmd, `{{json (index .Actor.Attributes "com.docker.compose.service")}}`)
	assert.Contains(t, cmd, `docker inspect --format '{{.Id}} {{.RestartCount}}' $ids`)

	first := hoststats.Cmd(0)
	assert.Contains(t, first, "s=0 ")
	assert.Contains(t, first, "s=$((now-60))", "the first cycle reads the last minute")
}

// An agent's kept samples are read as one taken over SSH, each at its own
// time, and the request names where the panel's samples end (D-113).
func TestRemoteAgentServiceStatsReadsEachSampleAtItsTime(t *testing.T) {
	var asked string
	h := &fakeHost{subsystem: func(name, stdin string, lineFn func(string)) error {
		asked = name + " " + stdin
		for _, at := range []int64{1791821400, 1791821460} {
			raw, _ := json.Marshal(agentproto.MetricsSample{At: at, Out: hostOutput})
			lineFn(string(raw))
		}
		return nil
	}}
	got, err := newTestRemote(h).AgentServiceStats(context.Background(), statsTargets, 1791821340)
	require.NoError(t, err)
	assert.Equal(t, agentproto.SubsystemMetrics+" "+`{"since":1791821340}`+"\n", asked)
	require.Len(t, got, 2)
	for i, at := range []int64{1791821400, 1791821460} {
		assert.Equal(t, time.Unix(at, 0).UTC(), got[i].At)
		assert.Equal(t, map[string]service.ContainerStats{
			appID:   {CPUPercent: 0.5, MemoryMB: 45.2},
			stackID: {CPUPercent: 4, MemoryMB: 300},
		}, totals(got[i]))
		assert.Equal(t, int64(1791821490), got[i].Until)
	}

	h.subsystem = func(_, _ string, lineFn func(string)) error {
		lineFn("error: this agent can't reach Docker, so it keeps no metrics")
		return errors.New("exit status 1")
	}
	_, err = newTestRemote(h).AgentServiceStats(context.Background(), statsTargets, 0)
	assert.ErrorContains(t, err, "keeps no metrics")
}

func TestRemoteCollectServiceStatsStopsWithItsContext(t *testing.T) {
	h := &fakeHost{exec: func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := newTestRemote(h).CollectServiceStats(ctx, statsTargets, 0)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestRemoteCollectServiceStatsReportsAFailure(t *testing.T) {
	h := &fakeHost{exec: func(context.Context, string) (string, error) {
		return "", errors.New("docker: command not found")
	}}
	_, err := newTestRemote(h).CollectServiceStats(context.Background(), statsTargets, 0)
	assert.ErrorContains(t, err, "command not found")
}

// ─── Local ────────────────────────────────────────────────────────────────────

func TestLocalCollectServiceStatsSamplesOnlyTargetContainers(t *testing.T) {
	f := fakedocker.New(t)
	f.Add(appBlue, container.StateRunning, map[string]string{rollout.LabelServiceID: appID})
	f.Add(appGreen, container.StateRunning, map[string]string{rollout.LabelServiceID: appID})
	web := f.Add(stackProject+"-web-1", container.StateRunning, map[string]string{rollout.LabelComposeProject: stackProject, rollout.LabelComposeService: "web"})
	web.RestartCount = 2
	f.Add("traefik", container.StateRunning, nil)
	f.Add("eager_turing", container.StateRunning, map[string]string{rollout.LabelServiceID: victimID})
	f.Add(victimBlue, container.StateExited, map[string]string{rollout.LabelServiceID: victimID})
	f.Sample(appBlue, 10, 100)
	f.Sample(appGreen, 20, 200)
	f.Sample(stackProject+"-web-1", 5, 50)
	f.Sample("traefik", 1, 1)

	// A kill of a process in a running slot, one of a container that is
	// gone, one of a build step that carries a stack's labels, and Traefik's.
	killed := func(name string, at int64, attrs map[string]string) events.Message {
		attrs["name"] = name
		return events.Message{Type: events.ContainerEventType, Action: events.ActionOOM, Time: at,
			Actor: events.Actor{ID: "id-" + name, Attributes: attrs}}
	}
	f.Events = []events.Message{
		killed(appBlue, 1760000010, map[string]string{rollout.LabelServiceID: appID}),
		killed(stackProject+"-job-1", 1760000020, map[string]string{rollout.LabelComposeProject: stackProject, rollout.LabelComposeService: "job", "image": "job:1"}),
		killed("build-step", 1760000030, map[string]string{rollout.LabelComposeProject: stackProject, rollout.LabelComposeService: "web",
			"image": "sha256:" + strings.Repeat("a", 64)}),
		killed("traefik", 1760000040, map[string]string{}),
	}

	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop()}
	before := time.Now().Unix()
	got, err := o.CollectServiceStats(context.Background(), statsTargets, 1760000000)
	require.NoError(t, err)
	assert.Equal(t, map[string]service.ContainerStats{
		appID:   {CPUPercent: 30, MemoryMB: 300},
		stackID: {CPUPercent: 5, MemoryMB: 50},
	}, totals(got))
	assert.Equal(t, map[string][]service.ContainerStats{"web": {{ContainerID: web.ID, ComposeService: "web", CPUPercent: 5, MemoryMB: 50, RestartCount: 2, RestartsRead: true}}},
		got.Containers[stackID], "the compose service from its label, its restarts from inspect; no counters on the control plane")
	assert.Nil(t, got.CPUWaitPct)
	assert.Equal(t, []service.OOMEvent{
		{ServiceID: appID, Container: appBlue, At: time.Unix(1760000010, 0).UTC()},
		{ServiceID: stackID, ComposeService: "job", Container: stackProject + "-job-1", At: time.Unix(1760000020, 0).UTC()},
	}, got.OOMKills, "the targets' memory kills, from Docker's events")
	assert.GreaterOrEqual(t, got.Until, before, "the next sample reads kills from here")
	assert.Equal(t, "1760000000", f.EventsQuery.Get("since"))
	assert.Equal(t, strconv.FormatInt(got.Until, 10), f.EventsQuery.Get("until"), "a window that has ended: Docker answers at once")
	assert.Contains(t, f.EventsQuery.Get("filters"), `"oom"`)
	assert.Equal(t, []string{appBlue, appGreen, stackProject + "-web-1"}, f.SampledNames(),
		"only the targets' running containers are sampled")
}

func TestLocalCollectServiceStatsReadsTheKillWindowAgainWhenEventsFail(t *testing.T) {
	f := fakedocker.New(t)
	f.EventsErr = true
	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop()}
	got, err := o.CollectServiceStats(context.Background(), statsTargets, 1760000000)
	require.NoError(t, err, "the sample is kept without its kills")
	assert.Empty(t, got.OOMKills)
	assert.Zero(t, got.Until, "the collector reads the same window next time")
}

func TestLocalCollectServiceStatsBoundsParallelSamples(t *testing.T) {
	f := fakedocker.New(t)
	f.StatsDelay = 30 * time.Millisecond
	const n = 3 * localStatsConcurrency
	for i := range n {
		name := stackProject + "-worker-" + string(rune('a'+i))
		f.Add(name, container.StateRunning, map[string]string{rollout.LabelComposeProject: stackProject})
		f.Sample(name, 1, 10)
	}

	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop()}
	got, err := o.CollectServiceStats(context.Background(), statsTargets, 0)
	require.NoError(t, err)
	assert.Equal(t, service.ContainerStats{CPUPercent: n, MemoryMB: 10 * n}, totals(got)[stackID])
	assert.LessOrEqual(t, f.MaxStatsInFlight(), localStatsConcurrency)
	assert.Greater(t, f.MaxStatsInFlight(), 1, "samples run in parallel")
}

func TestLocalCollectServiceStatsReportsADaemonFailure(t *testing.T) {
	f := fakedocker.New(t)
	f.StatsErr = true
	f.Add(appBlue, container.StateRunning, map[string]string{rollout.LabelServiceID: appID})

	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop()}
	_, err := o.CollectServiceStats(context.Background(), statsTargets, 0)
	assert.ErrorContains(t, err, "cgroup read failed")
}

func TestLocalCollectServiceStatsWithNothingToSample(t *testing.T) {
	f := fakedocker.New(t)
	f.Add("traefik", container.StateRunning, nil)

	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop()}
	got, err := o.CollectServiceStats(context.Background(), statsTargets, 0)
	require.NoError(t, err)
	assert.Empty(t, got.Containers)
	assert.Empty(t, f.SampledNames())
}

// ─── Latency (D-156) ──────────────────────────────────────────────────────────

// latencyLines are one Traefik service's bucket lines with the same count
// in every bucket, as its metrics page prints them.
func latencyLines(service string, n int) []string {
	les := append(strings.Split(hoststats.LatencyBounds, ","), "+Inf")
	out := make([]string, 0, len(les))
	for _, le := range les {
		out = append(out, fmt.Sprintf(`traefik_service_request_duration_seconds_bucket{code="200",method="GET",protocol="http",service="%s",le="%s"} %d`, service, le, n))
	}
	return out
}

func latencyPage() string {
	lines := []string{"process_start_time_seconds 1.7e+09"}
	lines = append(lines, latencyLines("paas-svc-aaaa1111@docker", 3)...)
	lines = append(lines, latencyLines("paas-svc-bbbb2222@docker", 5)...)
	lines = append(lines, latencyLines("paas-svc-eeee5555@docker", 7)...) // not a target
	return strings.Join(lines, "\n")
}

// pageReading is latencyPage attributed to statsTargets: every series, and
// the targets' names.
func pageReading() *service.LatencyReading {
	return &service.LatencyReading{Start: 1.7e+09,
		Counts: map[string][]uint64{"paas-svc-aaaa1111": counts(3), "paas-svc-bbbb2222": counts(5), "paas-svc-eeee5555": counts(7)},
		Names:  map[string]string{appID: "paas-svc-aaaa1111", stackID: "paas-svc-bbbb2222", victimID: "paas-svc-cccc3333"}}
}

func counts(n uint64) []uint64 {
	out := make([]uint64, len(strings.Split(hoststats.LatencyBounds, ","))+1)
	for i := range out {
		out[i] = n
	}
	return out
}

// The sixth section's histograms go to the targets they route to, by the
// Traefik service name the platform gives each (paas-svc-<id8>).
func TestParseHostStatsReadsLatency(t *testing.T) {
	out := hostOutput + "\n" + hoststats.Separator + "\n" + cid(1) + " 0\n" + hoststats.Separator + "\n" + latencyPage()
	s, err := parseHostStats(out)
	require.NoError(t, err)
	got := attributeSample(s, statsTargets)
	assert.Equal(t, pageReading(), got.Latency)

	// The agent's compact form reads the same.
	s, err = parseHostStats(hoststats.CompactLatency(out))
	require.NoError(t, err)
	assert.Equal(t, got.Latency, attributeSample(s, statsTargets).Latency)
}

// Five sections: an agent older than the latency section.
func TestParseHostStatsWithoutTheLatencySection(t *testing.T) {
	s, err := parseHostStats(hostOutput + "\n" + hoststats.Separator + "\n" + cid(1) + " 0")
	require.NoError(t, err)
	assert.Equal(t, &service.LatencyReading{Unmeasured: hoststats.UnmeasuredAgent}, attributeSample(s, statsTargets).Latency)
}

// Two targets whose IDs start alike share a Traefik service name: neither
// gets the other's requests.
func TestLatencyOfAnAmbiguousTraefikServiceCountsForNeither(t *testing.T) {
	twin := "aaaa1111-0000-0000-0000-00000000000f"
	out := hostOutput + "\n" + hoststats.Separator + "\n" + hoststats.Separator + "\n" + latencyPage()
	s, err := parseHostStats(out)
	require.NoError(t, err)
	got := attributeSample(s, append([]service.MetricsTarget{{ID: twin, Name: "twin"}}, statsTargets...))
	assert.Equal(t, map[string]string{stackID: "paas-svc-bbbb2222", victimID: "paas-svc-cccc3333"}, got.Latency.Names)
}

// The panel host's Traefik is read over HTTP on the control plane's
// network; one that does not answer is not measured.
func TestLocalCollectServiceStatsReadsThePanelTraefiksLatency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("# HELP go_gc_duration_seconds ...\ngo_gc_duration_seconds 0\n" + latencyPage() + "\n"))
	}))
	defer srv.Close()
	f := fakedocker.New(t)
	o := &LocalOrchestrator{dockerClient: f.Client(), log: zap.NewNop(), latencyURL: srv.URL + "/metrics"}
	got, err := o.CollectServiceStats(context.Background(), statsTargets, 0)
	require.NoError(t, err)
	assert.Equal(t, pageReading(), got.Latency)

	o.latencyURL = srv.URL + "/gone"
	got, err = o.CollectServiceStats(context.Background(), statsTargets, 0)
	require.NoError(t, err, "a Traefik that does not answer never fails the sample")
	assert.Equal(t, &service.LatencyReading{Unmeasured: hoststats.UnmeasuredTraefik}, got.Latency)
}

// A panel host's Traefik that can't be read (a wrong
// TOFT_CONTROL_PLANE_SUBNET answers 403) is logged once per cause, not
// every minute, and again after it was read.
func TestLocalLatencyReadFailureIsLoggedOncePerCause(t *testing.T) {
	var status atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(latencyPage()))
	}))
	defer srv.Close()
	core, logs := observer.New(zap.WarnLevel)
	o := &LocalOrchestrator{dockerClient: fakedocker.New(t).Client(), log: zap.New(core), latencyURL: srv.URL}
	collect := func(code int) {
		status.Store(int32(code))
		_, err := o.CollectServiceStats(context.Background(), statsTargets, 0)
		require.NoError(t, err)
	}
	collect(http.StatusForbidden)
	collect(http.StatusForbidden)
	collect(http.StatusBadGateway)
	collect(http.StatusOK)
	collect(http.StatusForbidden)
	causes := make([]string, 0, logs.Len())
	for _, e := range logs.All() {
		causes = append(causes, fmt.Sprint(e.ContextMap()["cause"]))
	}
	assert.Equal(t, []string{"status 403", "status 502", "status 403"}, causes)
}
