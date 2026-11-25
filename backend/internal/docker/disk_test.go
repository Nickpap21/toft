package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/Nickpap21/toft/backend/internal/builder"
	"github.com/Nickpap21/toft/backend/internal/disk"
)

// Free is df's Available, not size minus used: the blocks reserved for root
// are in neither Used nor Available.
func TestParseDFBytesFreeIsAvailable(t *testing.T) {
	out := "Filesystem 1B-blocks Used Available Capacity Mounted on\n/dev/sda1 80300000000 19500000000 57500000000 26% /\n"
	size, used, free, err := parseDFBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if size != 80_300_000_000 || used != 19_500_000_000 || free != 57_500_000_000 {
		t.Errorf("size, used, free = %d, %d, %d", size, used, free)
	}
}

// fakeBuilders answers buildctl for the builders' containers: each holds
// records of the given sizes, the last of them in use.
type fakeBuilders struct {
	caches map[string][]int64 // by container; absent = not running
	fails  map[string]error
	calls  []string
}

func (f *fakeBuilders) buildctl(_ context.Context, name string, args ...string) (string, bool, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if err := f.fails[name]; err != nil {
		return "", false, err
	}
	sizes, ok := f.caches[name]
	if !ok {
		return "", false, nil
	}
	rec := func(size int64, inUse bool) string {
		return `{"id":"x","size":` + strconv.FormatInt(size, 10) + `,"inUse":` + map[bool]string{true: "true", false: "false"}[inUse] + `,"shared":false}`
	}
	if args[0] == "du" {
		if len(sizes) == 0 {
			return "null\n", true, nil
		}
		recs := make([]string, 0, len(sizes))
		for i, s := range sizes {
			recs = append(recs, rec(s, i == len(sizes)-1))
		}
		return "[" + strings.Join(recs, ",") + "]\n", true, nil
	}
	// prune: --all removes every record not in use; --keep-storage N the
	// oldest until N MB or less are left.
	keep := int64(0)
	if i := slices.Index(args, "--keep-storage"); i >= 0 {
		keep, _ = strconv.ParseInt(args[i+1], 10, 64)
		keep *= 1_000_000
	}
	var out strings.Builder
	total := int64(0)
	for _, s := range sizes {
		total += s
	}
	for len(sizes) > 1 && total > keep {
		out.WriteString(rec(sizes[0], false) + "\n")
		total -= sizes[0]
		sizes = sizes[1:]
	}
	f.caches[name] = sizes
	return out.String(), true, nil
}

const (
	trustedCtr   = "buildx_buildkit_toft-build0"
	untrustedCtr = "buildx_buildkit_toft-build-untrusted0"
)

// The builders' caches are build cache, and their state volumes aren't
// apps' volumes.
func TestUsageCountsTheBuildersCaches(t *testing.T) {
	f := &fakeBuilders{caches: map[string][]int64{trustedCtr: {300, 200, 100}}}
	h := &diskHost{buildctl: f.buildctl}
	u := disk.Usage{BuildCacheBytes: 50, BuildCacheReclaimable: 50, Volumes: []disk.Volume{
		{Name: "paas-vol-1", Bytes: 7}, {Name: trustedCtr + "_state", Bytes: 900}, {Name: untrustedCtr + "_state", Bytes: 40},
	}}
	require.NoError(t, h.addBuilders(context.Background(), &u))
	assert.Equal(t, int64(50+600+40), u.BuildCacheBytes, "Docker's own, the running builder's records, the stopped one's volume")
	assert.Equal(t, int64(50+500), u.BuildCacheReclaimable, "but what a build is using")
	assert.Equal(t, []disk.Volume{{Name: "paas-vol-1", Bytes: 7}}, u.Volumes)

	// A builder whose buildctl fails is measured by its volume; a server
	// that stopped answering fails the read.
	f.fails = map[string]error{trustedCtr: errors.New("buildctl du: exit 1")}
	u = disk.Usage{Volumes: []disk.Volume{{Name: trustedCtr + "_state", Bytes: 900}}}
	require.NoError(t, h.addBuilders(context.Background(), &u))
	assert.Equal(t, disk.Usage{BuildCacheBytes: 900, Volumes: []disk.Volume{}}, u)
	f.fails[trustedCtr] = errors.Join(disk.ErrUnreachable, errors.New("connection lost"))
	assert.ErrorIs(t, h.addBuilders(context.Background(), &disk.Usage{}), disk.ErrUnreachable)

	recs, err := cacheRecords("null\n")
	require.NoError(t, err)
	assert.Empty(t, recs, "an empty cache")
	_, err = cacheRecords("Error: no such thing")
	assert.Error(t, err)
}

// A prune to a limit holds the builders' caches to it between them, the
// untrusted one first, and leaves Docker's own what they didn't use.
func TestPruneTrimsTheBuildersToOneLimit(t *testing.T) {
	mb := int64(1_000_000)
	f := &fakeBuilders{caches: map[string][]int64{untrustedCtr: {2 * mb, mb}, trustedCtr: {4 * mb, 3 * mb, 2 * mb, mb}}}
	h := &diskHost{buildctl: f.buildctl}
	freed, left, err := h.pruneBuilders(context.Background(), 6*mb, false)
	require.NoError(t, err)
	assert.Equal(t, []int64{2 * mb, mb}, f.caches[untrustedCtr], "under the limit: kept")
	assert.Equal(t, []int64{2 * mb, mb}, f.caches[trustedCtr], "what the untrusted one left of the limit")
	assert.Equal(t, 7*mb, freed)
	assert.Equal(t, int64(0), left)
	assert.Equal(t, []string{
		untrustedCtr + " prune --format {{json .}} --keep-storage 6", untrustedCtr + " du --format {{json .}}",
		trustedCtr + " prune --format {{json .}} --keep-storage 3", trustedCtr + " du --format {{json .}}",
	}, f.calls)

	// All of it: every record nothing uses, and no limit to hand on.
	f = &fakeBuilders{caches: map[string][]int64{trustedCtr: {4 * mb, mb}}}
	h = &diskHost{buildctl: f.buildctl}
	freed, _, err = h.pruneBuilders(context.Background(), 6*mb, true)
	require.NoError(t, err)
	assert.Equal(t, 4*mb, freed, "the record in use stays")
	assert.Equal(t, []string{untrustedCtr + " prune --format {{json .}} --all", trustedCtr + " prune --format {{json .}} --all"}, f.calls,
		"a builder that isn't running has nothing to prune")
}

// builderDocker is a Docker that holds one container under a builder's
// name: what inspect says of it, and a buildctl that prints out, or with
// hangs never exits. It counts the execs and the prunes of Docker's own
// cache.
type builderDocker struct {
	network string
	env     []string
	out     string
	exit    int
	hangs   bool

	mu     sync.Mutex
	execs  [][]string
	prunes int
}

// seen is the execs and the prunes so far.
func (f *builderDocker) seen() ([][]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.execs), f.prunes
}

func (f *builderDocker) client(t *testing.T) *client.Client {
	t.Helper()
	held := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		answer := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch path := r.URL.Path[strings.Index(r.URL.Path[1:], "/")+1:]; { // without /v1.48
		case path == "/containers/"+trustedCtr+"/json":
			answer(map[string]any{"Id": "c1", "HostConfig": map[string]any{"NetworkMode": f.network}, "Config": map[string]any{"Env": f.env}})
		case path == "/containers/c1/exec":
			var body struct{ Cmd []string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.execs = append(f.execs, body.Cmd)
			w.WriteHeader(http.StatusCreated)
			answer(map[string]string{"Id": "e1"})
		case path == "/exec/e1/start":
			_, _ = io.Copy(io.Discard, r.Body)
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_, _ = stdcopy.NewStdWriter(buf, stdcopy.Stdout).Write([]byte(f.out))
			_ = buf.Flush()
			if f.hangs {
				go func() { <-held; _ = conn.Close() }()
				return
			}
			_ = conn.Close()
		case path == "/exec/e1/json":
			answer(map[string]any{"ID": "e1", "ExitCode": f.exit})
		case path == "/build/prune":
			f.prunes++
			answer(map[string]any{"SpaceReclaimed": 5})
		default:
			w.WriteHeader(http.StatusNotFound)
			answer(map[string]string{"message": "No such container"})
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(held) })
	dc, err := client.NewClientWithOpts(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithVersion("1.48"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = dc.Close() })
	return dc
}

// buildctl runs only in a container that is the builder's by ensure.sh's
// test: a container that only has the builder's name is never run in,
// whatever its buildctl would do.
func TestBuildctlRunsOnlyInTheBuildersOwnContainer(t *testing.T) {
	theBuilders := &builderDocker{network: "toft-build", env: []string{"PATH=/bin", "TOFT_BUILDER_SPEC=img toft-build 1g slice 5gb"}, out: "[]\n"}
	out, ran, err := execBuildctl(context.Background(), theBuilders.client(t), builder.Trusted, "du", "--format", "{{json .}}")
	require.NoError(t, err)
	assert.True(t, ran)
	assert.Equal(t, "[]\n", out)
	execs, _ := theBuilders.seen()
	assert.Equal(t, [][]string{{"buildctl", "du", "--format", "{{json .}}"}}, execs)

	for name, f := range map[string]*builderDocker{
		"an app's network":            {network: "shop_default", env: []string{"TOFT_BUILDER_SPEC=img toft-build 1g slice 5gb"}, hangs: true},
		"no mark":                     {network: "toft-build", env: []string{"PATH=/bin"}, hangs: true},
		"the other builder's network": {network: "toft-build-untrusted", env: []string{"TOFT_BUILDER_SPEC=x"}, hangs: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, ran, err := execBuildctl(ctx, f.client(t), builder.Trusted, "prune", "--all")
			require.NoError(t, err, "as a builder that isn't running")
			assert.False(t, ran)
			execs, _ := f.seen()
			assert.Empty(t, execs, "nothing runs in it")
		})
	}

	_, ran, err = execBuildctl(context.Background(), theBuilders.client(t), builder.Untrusted, "du")
	require.NoError(t, err)
	assert.False(t, ran, "no container of that name")
}

// A buildctl that never exits is given up when the context ends.
func TestBuildctlThatNeverExitsEndsWithTheContext(t *testing.T) {
	f := &builderDocker{network: "toft-build", env: []string{"TOFT_BUILDER_SPEC=x"}, out: "partial", hangs: true}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, ran, err := execBuildctl(ctx, f.client(t), builder.Trusted, "prune", "--all")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, ran)
	assert.Less(t, time.Since(began), 5*time.Second)
}

// A builder whose prune fails is logged; the other builder's cache and
// Docker's own are pruned all the same. A server that stopped answering
// still fails the clean.
func TestABuildersFailedPruneDoesNotStopTheRest(t *testing.T) {
	mb := int64(1_000_000)
	core, logs := observer.New(zap.WarnLevel)
	f := &fakeBuilders{caches: map[string][]int64{trustedCtr: {4 * mb, mb}}, fails: map[string]error{untrustedCtr: errors.New("buildctl prune: exit 1: boom")}}
	docker := &builderDocker{}
	h := &diskHost{dc: docker.client(t), buildctl: f.buildctl, log: zap.New(core)}
	freed, err := h.PruneBuildCache(context.Background(), 6*mb, true)
	require.NoError(t, err)
	assert.Equal(t, 4*mb+5, freed, "the other builder's, and Docker's own")
	_, prunes := docker.seen()
	assert.Equal(t, 1, prunes)
	require.Equal(t, 1, logs.Len())
	assert.Equal(t, "toft-build-untrusted", logs.All()[0].ContextMap()["builder"])

	f.fails[untrustedCtr] = errors.Join(disk.ErrUnreachable, errors.New("connection lost"))
	_, err = h.PruneBuildCache(context.Background(), 6*mb, true)
	assert.ErrorIs(t, err, disk.ErrUnreachable)
	_, prunes = docker.seen()
	assert.Equal(t, 1, prunes)
}
