package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/sshpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func port(n int) *int { return &n }

func dest(cidr string, p int32) BuildDestination {
	d := BuildDestination{ID: cidr, Prefix: netip.MustParsePrefix(cidr)}
	if p != 0 {
		d.Port = &p
	}
	return d
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var refused *BuildDestinationError
	require.ErrorAs(t, err, &refused)
	return refused.Code
}

func TestParseBuildDestination(t *testing.T) {
	p, pt, note, err := parseBuildDestination(BuildDestinationInput{Address: " 10.0.5.20 ", Port: port(5000), Note: " our registry "})
	require.NoError(t, err)
	assert.Equal(t, "10.0.5.20/32", p.String(), "one address is a /32")
	assert.Equal(t, int32(5000), *pt)
	assert.Equal(t, "our registry", note)

	p, pt, _, err = parseBuildDestination(BuildDestinationInput{Address: "100.64.0.0/10"})
	require.NoError(t, err)
	assert.Equal(t, "100.64.0.0/10", p.String())
	assert.Nil(t, pt, "no port is every TCP port")

	for name, c := range map[string]struct {
		in    BuildDestinationInput
		field string
	}{
		"nothing":                  {BuildDestinationInput{}, "address"},
		"a name":                   {BuildDestinationInput{Address: "registry.internal"}, "address"},
		"loopback":                 {BuildDestinationInput{Address: "127.0.0.1"}, "address"},
		"link-local, the metadata": {BuildDestinationInput{Address: "169.254.169.254"}, "address"},
		"public":                   {BuildDestinationInput{Address: "8.8.8.8"}, "address"},
		"just outside 172.16/12":   {BuildDestinationInput{Address: "172.32.0.1"}, "address"},
		"wider than a range":       {BuildDestinationInput{Address: "10.0.0.0/7"}, "address"},
		"everything":               {BuildDestinationInput{Address: "0.0.0.0/0"}, "address"},
		"IPv6":                     {BuildDestinationInput{Address: "fd00::1"}, "address"},
		"IPv4 inside IPv6":         {BuildDestinationInput{Address: "::ffff:10.0.0.1"}, "address"},
		"bits beyond the prefix":   {BuildDestinationInput{Address: "10.0.5.20/24"}, "address"},
		"nft text":                 {BuildDestinationInput{Address: "10.0.5.20 accept; }"}, "address"},
		"port 0":                   {BuildDestinationInput{Address: "10.0.5.20", Port: port(0)}, "port"},
		"port 65536":               {BuildDestinationInput{Address: "10.0.5.20", Port: port(65536)}, "port"},
		"the mail port":            {BuildDestinationInput{Address: "10.0.5.20", Port: port(25)}, "port"},
		"a long note":              {BuildDestinationInput{Address: "10.0.5.20", Note: strings.Repeat("é", 201)}, "note"},
		"a note of two lines":      {BuildDestinationInput{Address: "10.0.5.20", Note: "a\nb"}, "note"},
	} {
		_, _, _, err := parseBuildDestination(c.in)
		var refused *BuildDestinationError
		require.ErrorAs(t, err, &refused, name)
		assert.Equal(t, codeBuildDestinationInvalid, refused.Code, name)
		assert.Equal(t, c.field, refused.Field, name)
	}
	_, _, _, err = parseBuildDestination(BuildDestinationInput{Address: "10.0.5.20/24"})
	assert.ErrorContains(t, err, "10.0.5.0/24", "the refusal says what to write")
}

// A host says what it must never open; an entry holding any of it is
// refused, whatever the list says.
func TestAHostRefusesWhatItMustNeverOpen(t *testing.T) {
	h := hostNetworks{
		Own:    []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("203.0.113.9")},
		Docker: []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16"), netip.MustParsePrefix("10.203.0.0/16")},
	}
	panel := []netip.Addr{netip.MustParseAddr("10.0.0.2")}
	for cidr, want := range map[string]string{
		"10.0.0.2/32":    codeBuildDestinationPanel,
		"10.0.0.0/30":    codeBuildDestinationPanel,
		"10.0.0.5/32":    codeBuildDestinationServer,
		"10.0.0.4/30":    codeBuildDestinationServer,
		"172.18.4.2/32":  codeBuildDestinationDocker, // a container of another app
		"172.16.0.0/12":  codeBuildDestinationDocker,
		"10.203.9.0/24":  codeBuildDestinationDocker, // the pool: networks to come
		"10.0.5.20/32":   "",
		"192.168.0.0/16": "",
	} {
		refused := h.refuses(netip.MustParsePrefix(cidr), panel, false)
		if want == "" {
			assert.Nil(t, refused, cidr)
			continue
		}
		require.NotNil(t, refused, cidr)
		assert.Equal(t, want, refused.Code, cidr)
	}
	// On the panel's own server its addresses are the panel's.
	assert.Equal(t, codeBuildDestinationPanel, h.refuses(netip.MustParsePrefix("10.0.0.5/32"), nil, true).Code)
}

func TestBuildDestinationRules(t *testing.T) {
	list := []BuildDestination{dest("10.0.5.0/24", 5000), dest("192.168.7.7/32", 0)}
	list[0].Note = "accept; }"
	h := hostNetworks{Docker: []netip.Prefix{netip.MustParsePrefix("10.203.0.0/16"), netip.MustParsePrefix("203.0.113.0/24")}}
	got := buildDestinationRules(list, h.never([]netip.Addr{netip.MustParseAddr("10.9.9.9"), netip.MustParseAddr("198.51.100.7")}))
	assert.Equal(t, `# The private addresses builds on this server may reach (Toft, build
# destinations). The panel writes this file whole; an edit is lost.
flush chain ip toft build_destinations
table ip toft {
	chain build_destinations {
		ip daddr 10.9.9.9 counter drop
		ip daddr 10.203.0.0/16 counter drop
		ip daddr 10.0.5.0/24 tcp dport 5000 counter accept
		ip daddr 192.168.7.7 meta l4proto tcp counter accept
	}
}
`, got, "the drops first; public addresses and the note never get into the rules")
}

// The list never stops the firewall: the table doesn't include it, the
// unit loads it after the table and may fail, and only the builder's
// bridge jumps to its chain. (Run on a server: private/README.md, D-170.)
func TestTheListIsLoadedApartFromTheFirewall(t *testing.T) {
	assert.NotContains(t, hostFirewallRules, "include")
	assert.Contains(t, hostFirewallRules, "\tchain build_destinations {\n\t}\n")
	assert.Equal(t, 1, strings.Count(hostFirewallRules, "jump build_destinations"))
	assert.Contains(t, hostFirewallRules, "\t\ttcp dport 25 counter drop\n\t\tiifname \"toft-ubuild\" jump build_destinations\n\t\tip daddr { 10.0.0.0/8,",
		"the builder's bridge by its exact name, just ahead of the private ranges' drop")
	assert.Contains(t, hostFirewallUnit, "ExecStart=/usr/sbin/nft -f /etc/toft/firewall.nft\n")
	assert.Contains(t, hostFirewallUnit, "\nExecStartPost=-/bin/sh -c '[ ! -e "+buildDestinationsPath+" ] || exec /usr/sbin/nft -f "+buildDestinationsPath+"'\n",
		"a list nft refuses fails this line only: the unit and the table stay up")
}

func TestReadHostNetworks(t *testing.T) {
	sh := &fakeShell{answer: func(string) string {
		return "own:10.0.0.5/24\nown:203.0.113.9/32\nown:172.18.0.1/16\nown:192.168.1.4/24\nown:10.9.0.1\n" +
			"net:172.18.0.0/16\nnet:fd00:1::/64\npool:10.203.0.0/16\npool:172.18.0.0/16\npool:192.168.0.0/16\nread"
	}}
	h, err := readHostNetworks(sh)
	require.NoError(t, err)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("10.0.0.5"), netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("192.168.1.4"), netip.MustParseAddr("10.9.0.1")}, h.Own,
		"every address of the host's own (one on a bridge, one inside a pool, one with a peer), but a Docker network's gateway")
	assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16"), netip.MustParsePrefix("10.203.0.0/16"), netip.MustParsePrefix("192.168.0.0/16")}, h.Docker)
	assert.NotContains(t, hostNetworksScript, "/bridge", "a host's private address may be on a bridge of its own")

	assert.Contains(t, hostNetworksScript, "172.17.0.0/16 172.18.0.0/16 172.19.0.0/16 172.20.0.0/14 172.24.0.0/14 172.28.0.0/14 192.168.0.0/16",
		"a Docker with no pool set allocates from the ones built into it")

	_, err = readHostNetworks(&fakeShell{answer: func(string) string { return "own:10.0.0.5/24" }})
	assert.ErrorContains(t, err, "cut short", "half an answer is none")
	_, err = readHostNetworks(&fakeShell{answer: func(string) string { return "net:garbage\nread" }})
	assert.Error(t, err)
	_, err = readHostNetworks(&fakeShell{fails: func(string) bool { return true }})
	assert.Error(t, err)
}

// The script in a real sh, with ip and docker stubbed: a read that fails
// fails the script before its last line, so the host gave no answer. A
// list of the host's own addresses that came back empty because ip failed
// would refuse nothing.
func TestHostNetworksScriptGivesNoAnswerWhenAReadFails(t *testing.T) {
	bin := t.TempDir()
	stub := func(name, script string) {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	}
	run := func() (string, error) {
		c := exec.Command("sh", "-c", hostNetworksScript)
		c.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
		out, err := c.Output()
		return string(out), err
	}
	const (
		ip     = `echo "2: eth0    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0\\       valid_lft forever preferred_lft forever"`
		docker = `case "$1 $2" in "network ls") echo n1 ;; "network inspect") echo net:172.18.0.0/16 ;; esac`
	)

	stub("ip", ip)
	stub("docker", docker)
	out, err := run()
	require.NoError(t, err)
	h, err := readHostNetworks(&fakeShell{answer: func(string) string { return out }})
	require.NoError(t, err, out)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("10.0.0.5")}, h.Own)
	assert.Contains(t, h.Docker, netip.MustParsePrefix("172.18.0.0/16"))
	assert.Contains(t, h.Docker, netip.MustParsePrefix("192.168.0.0/16"), "the built-in pools")

	stub("ip", ":")
	out, err = run()
	require.NoError(t, err, "a host with no address of that kind has answered")
	assert.NotContains(t, out, "own:")
	assert.True(t, strings.HasSuffix(out, "read\n"), out)

	for name, break_ := range map[string]func(){
		"ip fails":                func() { stub("ip", `echo "10.0.0.5/24"; echo "Cannot open netlink socket" >&2; exit 1`) },
		"docker network ls fails": func() { stub("docker", `[ "$1 $2" != "network ls" ] || exit 1`) },
		"docker network inspect":  func() { stub("docker", `case "$1 $2" in "network ls") echo n1 ;; "network inspect") exit 1 ;; esac`) },
		"docker info fails":       func() { stub("docker", `case "$1" in network) echo n1 ;; info) exit 1 ;; esac`) },
	} {
		stub("ip", ip)
		stub("docker", docker)
		break_()
		out, err := run()
		require.Error(t, err, name)
		assert.NotContains(t, out, "read", name)
		_, err = readHostNetworks(&fakeShell{answer: func(string) string { return out }})
		assert.Error(t, err, name)
	}
}

func TestInstallBuildDestinations(t *testing.T) {
	h := hostNetworks{Own: []netip.Addr{netip.MustParseAddr("10.0.0.5")}, Docker: []netip.Prefix{netip.MustParsePrefix("10.203.0.0/16")}}
	panel := []netip.Addr{netip.MustParseAddr("10.0.0.2")}

	t.Run("one check, then one load of the whole list", func(t *testing.T) {
		sh := &fakeShell{}
		require.NoError(t, installBuildDestinations(sh, []BuildDestination{dest("10.0.5.20/32", 5000)}, h, panel, false, func(string) {}))
		assert.Equal(t, []string{"nft -c -f -", loadBuildDestinationsCmd}, sh.cmds)
		want := buildDestinationRules([]BuildDestination{dest("10.0.5.20/32", 5000)}, h.never(panel))
		assert.Equal(t, []string{want, want}, sh.inputs)
		assert.Equal(t, 1, strings.Count(loadBuildDestinationsCmd, "nft -f "), "one load, of a file that only then takes its name")
	})

	t.Run("at every write an entry the host refuses now is left out", func(t *testing.T) {
		sh := &fakeShell{}
		var lines []string
		list := []BuildDestination{dest("10.0.0.2/32", 0), dest("10.0.0.5/32", 0), dest("10.203.4.0/24", 0), dest("10.0.5.20/32", 5000)}
		require.NoError(t, installBuildDestinations(sh, list, h, panel, false, func(l string) { lines = append(lines, l) }))
		assert.Equal(t, buildDestinationRules(list[3:], h.never(panel)), sh.inputs[1])
		assert.Len(t, slices.DeleteFunc(lines, func(l string) bool { return !strings.HasPrefix(l, "Build destination left out: ") }), 3)
	})

	t.Run("an empty list leaves no file and an empty chain, and installs nothing", func(t *testing.T) {
		for _, list := range [][]BuildDestination{nil, {dest("10.0.0.5/32", 0)}} {
			sh := &fakeShell{}
			require.NoError(t, installBuildDestinations(sh, list, h, panel, false, func(string) {}))
			assert.Equal(t, []string{clearBuildDestinationsCmd}, sh.cmds)
		}
	})

	t.Run("rules nft refuses are never written", func(t *testing.T) {
		sh := &fakeShell{fails: func(cmd string) bool { return cmd == "nft -c -f -" }}
		err := installBuildDestinations(sh, []BuildDestination{dest("10.0.5.20/32", 0)}, h, panel, false, func(string) {})
		assert.ErrorContains(t, err, "nothing was changed")
		assert.Equal(t, []string{"nft -c -f -"}, sh.cmds)
	})
}

// ─── The service ──────────────────────────────────────────────────────────────

// destStore is the lists in memory; fakeTx rolls it and the audit rows
// back when the function fails, as a transaction does.
type destStore struct {
	lists map[string][]BuildDestination
	locks []string
	next  int
}

func (s *destStore) LockBuildDestinations(_ context.Context, serverID string) error {
	s.locks = append(s.locks, serverID)
	return nil
}

func (s *destStore) ListBuildDestinations(_ context.Context, serverID string) ([]BuildDestination, error) {
	return slices.Clone(s.lists[serverID]), nil
}

func (s *destStore) AddBuildDestination(_ context.Context, serverID string, p netip.Prefix, port *int32, note, _ string) (BuildDestination, error) {
	for _, d := range s.lists[serverID] {
		if d.Prefix == p && ((d.Port == nil && port == nil) || (d.Port != nil && port != nil && *d.Port == *port)) {
			return BuildDestination{}, &BuildDestinationError{Code: codeBuildDestinationExists, Message: "exists"}
		}
	}
	s.next++
	d := BuildDestination{ID: "d" + string(rune('0'+s.next)), Prefix: p, Port: port, Note: note, CreatedAt: time.Unix(0, 0)}
	s.lists[serverID] = append(s.lists[serverID], d)
	return d, nil
}

func (s *destStore) RemoveBuildDestination(_ context.Context, serverID, id string) (BuildDestination, error) {
	for i, d := range s.lists[serverID] {
		if d.ID == id {
			s.lists[serverID] = slices.Delete(slices.Clone(s.lists[serverID]), i, i+1)
			return d, nil
		}
	}
	return BuildDestination{}, ErrBuildDestinationNotFound
}

type destAudit struct {
	workspace, instance []string // "org actor action resource detail…"
}

func (a *destAudit) RecordAuditLogDetail(_ context.Context, orgID, actorID, action, resource string, detail map[string]any) error {
	a.workspace = append(a.workspace, strings.Join([]string{orgID, actorID, action, resource, detail["address"].(string)}, " "))
	return nil
}

func (a *destAudit) RecordAudit(_ context.Context, actorID, action string, detail map[string]any) error {
	server, _ := detail["server"].(string)
	a.instance = append(a.instance, strings.Join([]string{actorID, action, detail["address"].(string), server}, " "))
	return nil
}

type fakeTx struct {
	store *destStore
	audit *destAudit
}

func (f fakeTx) InTx(ctx context.Context, fn func(context.Context) error) error {
	lists := map[string][]BuildDestination{}
	for k, v := range f.store.lists {
		lists[k] = slices.Clone(v)
	}
	audit := *f.audit
	err := fn(ctx)
	if err != nil {
		f.store.lists, *f.audit = lists, audit
	}
	return err
}

// destHost is a host a list is written to: what it says of its networks,
// and whether it answers at all.
type destHost struct {
	firewallHost
	own, nets []string
	down      bool
	sh        *fakeShell
}

func newDestHost(own, nets []string) *destHost {
	h := &destHost{own: own, nets: nets, firewallHost: firewallHost{hasNft: true, enabled: true, loaded: true, rules: hostFirewallRules, unit: hostFirewallUnit}}
	fw := h.firewallHost.shell()
	h.sh = &fakeShell{
		answer: func(cmd string) string {
			if cmd != hostNetworksScript {
				return fw.answer(cmd)
			}
			var b strings.Builder
			for _, a := range h.own {
				b.WriteString("own:" + a + "\n")
			}
			for _, n := range h.nets {
				b.WriteString("net:" + n + "\n")
			}
			return b.String() + "read"
		},
		fails: func(string) bool { return h.down },
	}
	return h
}

// loaded is the list the host last took, "" when none.
func (h *destHost) loaded() string {
	out := ""
	for i, cmd := range h.sh.cmds {
		switch cmd {
		case loadBuildDestinationsCmd:
			out = h.sh.inputs[slices.Index(h.inputCmds(), i)]
		case clearBuildDestinationsCmd:
			out = ""
		}
	}
	return out
}

// inputCmds are the indexes in cmds of the commands that took stdin.
func (h *destHost) inputCmds() []int {
	var at []int
	for i, cmd := range h.sh.cmds {
		if cmd == "nft -c -f -" || cmd == loadBuildDestinationsCmd || strings.HasPrefix(cmd, "mkdir -p") {
			at = append(at, i)
		}
	}
	return at
}

// shellHost is a pooled connection that runs on a fake shell.
type shellHost struct {
	sshpool.RemoteHost
	sh *fakeShell
}

func (h shellHost) Exec(_ context.Context, cmd string) (string, error) { return h.sh.Exec(cmd) }
func (h shellHost) ExecInput(_ context.Context, cmd string, in io.Reader) (string, error) {
	return h.sh.ExecInput(cmd, in)
}
func (h shellHost) ExecStream(_ context.Context, cmd string, logFn func(string)) error {
	return h.sh.ExecStream(cmd, logFn)
}

type shellConn map[string]*fakeShell // server ID → its shell

func (c shellConn) Handle(t sshpool.Target) sshpool.RemoteHost { return shellHost{sh: c[t.ServerID]} }
func (shellConn) Invalidate(string)                            {}

type panelShell struct{ sh *fakeShell }

func (p panelShell) PanelHostShell(context.Context) Shell {
	if p.sh == nil {
		return nil
	}
	return p.sh
}

const ada, bob = "ada", "bob" // an operator, and an owner who isn't one

type destFixture struct {
	svc           *Service
	store         *destStore
	audit         *destAudit
	panel, server *destHost
}

// newDestFixture is a panel at 10.0.0.2 and one server, web-1 of org-a, at
// 10.0.0.5, whose Docker has 172.18.0.0/16 and the pool 10.203.0.0/16.
func newDestFixture() *destFixture {
	f := &destFixture{
		store:  &destStore{lists: map[string][]BuildDestination{}},
		audit:  &destAudit{},
		panel:  newDestHost([]string{"10.0.0.2/24", "203.0.113.2/32"}, []string{"172.20.0.0/16"}),
		server: newDestHost([]string{"10.0.0.5/24"}, []string{"172.18.0.0/16", "10.203.0.0/16"}),
	}
	repo := &fakeRepo{servers: map[string]*Server{testServerID: {ID: testServerID, OrgID: "org-a", Name: "web-1"}}}
	f.svc = NewService(repo, "")
	f.svc.WithOperatorChecker(fakeOperators{ada: true})
	f.svc.WithSSH(shellConn{testServerID: f.server.sh}, nil)
	f.svc.WithBuildDestinations(f.store, fakeTx{f.store, f.audit}, panelShell{f.panel.sh}, f.audit, f.audit)
	return f
}

func TestOnlyOperatorsSeeOrChangeBuildDestinations(t *testing.T) {
	f := newDestFixture()
	ctx := context.Background()
	_, err := f.svc.ListBuildDestinations(ctx, bob, testServerID)
	assert.ErrorIs(t, err, ErrBuildDestinationsOperatorOnly)
	_, err = f.svc.AddBuildDestination(ctx, bob, testServerID, BuildDestinationInput{Address: "10.0.5.20"})
	assert.ErrorIs(t, err, ErrBuildDestinationsOperatorOnly)
	assert.ErrorIs(t, f.svc.RemoveBuildDestination(ctx, bob, testServerID, "d1"), ErrBuildDestinationsOperatorOnly)
	assert.Empty(t, f.server.sh.cmds, "nothing is asked of the server")
}

// A save is the entry, its audit rows and the server's firewall together.
func TestAddingABuildDestinationOpensItOnTheServer(t *testing.T) {
	f := newDestFixture()
	ctx := context.Background()
	f.server.firewallHost.rules = "an older release's rules" // no chain for the list yet

	d, err := f.svc.AddBuildDestination(ctx, ada, testServerID, BuildDestinationInput{Address: "10.0.5.20", Port: port(5000), Note: "registry"})
	require.NoError(t, err)
	assert.Equal(t, "10.0.5.20", d.Address())

	assert.Equal(t, []string{testServerID}, f.store.locks, "one change to a list at a time")
	assert.Equal(t, []string{"org-a ada server.build_destination_added server:" + testServerID + " 10.0.5.20"}, f.audit.workspace)
	assert.Equal(t, []string{"ada build_destination.added 10.0.5.20 web-1"}, f.audit.instance)
	assert.Equal(t, buildDestinationRules([]BuildDestination{dest("10.0.5.20/32", 5000)},
		[]netip.Prefix{netip.MustParsePrefix("10.0.0.2/32"), netip.MustParsePrefix("172.18.0.0/16"), netip.MustParsePrefix("10.203.0.0/16")}),
		f.server.loaded(), "the whole list, behind the drop of the panel's private address and Docker's networks")
	restart, load := indexOf(f.server.sh.cmds, "systemctl restart toft-firewall"), indexOf(f.server.sh.cmds, loadBuildDestinationsCmd)
	require.GreaterOrEqual(t, restart, 0, "a server of an older release gets the new firewall with the save, without Provision")
	assert.Greater(t, load, restart)

	// A second entry: the whole list again, never a difference.
	_, err = f.svc.AddBuildDestination(ctx, ada, testServerID, BuildDestinationInput{Address: "192.168.7.0/24"})
	require.NoError(t, err)
	assert.Contains(t, f.server.loaded(), "ip daddr 10.0.5.20 tcp dport 5000 counter accept\n\t\tip daddr 192.168.7.0/24 meta l4proto tcp counter accept\n")

	// Removing one closes it; removing the last leaves no list.
	require.NoError(t, f.svc.RemoveBuildDestination(ctx, ada, testServerID, d.ID))
	assert.NotContains(t, f.server.loaded(), "10.0.5.20")
	assert.Equal(t, "ada build_destination.removed 10.0.5.20 web-1", f.audit.instance[2])
	list, err := f.svc.ListBuildDestinations(ctx, ada, testServerID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NoError(t, f.svc.RemoveBuildDestination(ctx, ada, testServerID, list[0].ID))
	assert.Equal(t, clearBuildDestinationsCmd, f.server.sh.cmds[len(f.server.sh.cmds)-1])
	assert.Len(t, f.audit.workspace, 4, "a row a change, in the workspace's log")
	assert.Len(t, f.audit.instance, 4, "and in the instance's")

	assert.ErrorIs(t, f.svc.RemoveBuildDestination(ctx, ada, testServerID, "gone"), ErrBuildDestinationNotFound)
}

func TestThePanelsOwnServerHasItsOwnList(t *testing.T) {
	f := newDestFixture()
	_, err := f.svc.AddBuildDestination(context.Background(), ada, "", BuildDestinationInput{Address: "10.0.5.20"})
	require.NoError(t, err)
	assert.Empty(t, f.audit.workspace, "the panel's server is no workspace's")
	assert.Equal(t, []string{"ada build_destination.added 10.0.5.20 "}, f.audit.instance)
	assert.Contains(t, f.panel.loaded(), "ip daddr 10.0.5.20 meta l4proto tcp counter accept")
	assert.Contains(t, f.panel.loaded(), "ip daddr 172.20.0.0/16 counter drop")
	assert.Empty(t, f.server.sh.cmds)
	assert.Empty(t, f.store.lists[testServerID])
}

// A case a refusal, at the save: nothing is stored, no audit row is left
// and the server's list isn't touched.
func TestASaveRefusesWhatTheServerMustNeverOpen(t *testing.T) {
	for name, c := range map[string]struct {
		server string
		in     BuildDestinationInput
		code   string
	}{
		"loopback":                         {testServerID, BuildDestinationInput{Address: "127.0.0.1"}, codeBuildDestinationInvalid},
		"link-local":                       {testServerID, BuildDestinationInput{Address: "169.254.169.254"}, codeBuildDestinationInvalid},
		"public":                           {testServerID, BuildDestinationInput{Address: "203.0.113.9"}, codeBuildDestinationInvalid},
		"the panel's address":              {testServerID, BuildDestinationInput{Address: "10.0.0.2"}, codeBuildDestinationPanel},
		"a network holding the panel":      {testServerID, BuildDestinationInput{Address: "10.0.0.0/30"}, codeBuildDestinationPanel},
		"the server's own":                 {testServerID, BuildDestinationInput{Address: "10.0.0.5", Port: port(22)}, codeBuildDestinationServer},
		"a Docker network":                 {testServerID, BuildDestinationInput{Address: "172.18.0.9"}, codeBuildDestinationDocker},
		"the Docker pool":                  {testServerID, BuildDestinationInput{Address: "10.203.4.0/24"}, codeBuildDestinationDocker},
		"the panel's own, on the panel":    {"", BuildDestinationInput{Address: "10.0.0.2"}, codeBuildDestinationPanel},
		"the panel's Docker, on the panel": {"", BuildDestinationInput{Address: "172.20.0.3"}, codeBuildDestinationDocker},
	} {
		f := newDestFixture()
		_, err := f.svc.AddBuildDestination(context.Background(), ada, c.server, c.in)
		assert.Equal(t, c.code, codeOf(t, err), name)
		assert.Empty(t, f.store.lists[c.server], name)
		assert.Empty(t, f.audit.instance, name)
		assert.Empty(t, f.audit.workspace, name)
		for _, h := range []*destHost{f.server, f.panel} {
			assert.False(t, issued(h.sh.cmds, "nft -f"), name)
			assert.False(t, issued(h.sh.cmds, "cat >"), name)
		}
	}
}

func TestAListHoldsEachEntryOnceAndAHundred(t *testing.T) {
	f := newDestFixture()
	ctx := context.Background()
	in := BuildDestinationInput{Address: "10.0.5.20", Port: port(5000)}
	_, err := f.svc.AddBuildDestination(ctx, ada, testServerID, in)
	require.NoError(t, err)
	_, err = f.svc.AddBuildDestination(ctx, ada, testServerID, in)
	assert.Equal(t, codeBuildDestinationExists, codeOf(t, err))
	in.Port = nil
	_, err = f.svc.AddBuildDestination(ctx, ada, testServerID, in)
	require.NoError(t, err, "the same address with every port is another entry")

	for len(f.store.lists[testServerID]) < MaxBuildDestinations {
		f.store.lists[testServerID] = append(f.store.lists[testServerID], dest("192.168.1.0/24", int32(len(f.store.lists[testServerID])+1)))
	}
	_, err = f.svc.AddBuildDestination(ctx, ada, testServerID, BuildDestinationInput{Address: "10.0.5.21"})
	assert.Equal(t, codeBuildDestinationsFull, codeOf(t, err))
}

// A server that doesn't take the list refuses the save: no entry, no
// audit row. Nor is one saved where the panel's addresses can't be read.
func TestAServerThatDoesNotTakeTheListRefusesTheSave(t *testing.T) {
	ctx := context.Background()
	in := BuildDestinationInput{Address: "10.0.5.20"}
	for name, breakIt := range map[string]func(f *destFixture){
		"the server is unreachable": func(f *destFixture) { f.server.down = true },
		"its firewall refuses the rules": func(f *destFixture) {
			f.server.sh.fails = func(cmd string) bool { return cmd == loadBuildDestinationsCmd }
		},
		"the panel's host can't be asked": func(f *destFixture) { f.panel.down = true },
		"the panel has no host helper":    func(f *destFixture) { f.svc.dests.panel = panelShell{} },
		"its firewall can't be brought up": func(f *destFixture) {
			f.server.firewallHost.hasNft = false
			f.server.sh.fails = func(cmd string) bool { return strings.Contains(cmd, "apt-get") }
		},
	} {
		f := newDestFixture()
		breakIt(f)
		_, err := f.svc.AddBuildDestination(ctx, ada, testServerID, in)
		require.ErrorIs(t, err, ErrBuildDestinationsNotApplied, name)
		assert.Empty(t, f.store.lists[testServerID], name)
		assert.Empty(t, f.audit.instance, name)
		assert.Empty(t, f.audit.workspace, name)
	}

	// A removal too: the entry stays listed while it is still open.
	f := newDestFixture()
	d, err := f.svc.AddBuildDestination(ctx, ada, testServerID, in)
	require.NoError(t, err)
	f.server.down = true
	require.ErrorIs(t, f.svc.RemoveBuildDestination(ctx, ada, testServerID, d.ID), ErrBuildDestinationsNotApplied)
	assert.Len(t, f.store.lists[testServerID], 1)
	assert.Len(t, f.audit.instance, 1)

	// The save outlives its request: a list the server took is committed.
	f = newDestFixture()
	gone, cancel := context.WithCancel(ctx)
	cancel()
	_, err = f.svc.AddBuildDestination(gone, ada, testServerID, in)
	require.NoError(t, err)
	assert.Len(t, f.store.lists[testServerID], 1)
}

// A change that reached the server's firewall and wasn't saved is taken
// back there: the saved list is written again.
func TestAnUnsavedChangeIsTakenBackFromTheServer(t *testing.T) {
	ctx := context.Background()
	for name, breakIt := range map[string]func(f *destFixture){
		"the server took the list and its answer was lost": func(f *destFixture) {
			lost := false
			f.server.sh.fails = func(cmd string) bool {
				once := cmd == loadBuildDestinationsCmd && !lost
				lost = lost || once
				return once
			}
		},
		"the commit failed": func(f *destFixture) {
			f.svc.dests.tx = &failingCommit{fakeTx: fakeTx{f.store, f.audit}, left: 1}
		},
	} {
		f := newDestFixture()
		_, err := f.svc.AddBuildDestination(ctx, ada, testServerID, BuildDestinationInput{Address: "10.0.5.20"})
		require.NoError(t, err, name)
		breakIt(f)
		_, err = f.svc.AddBuildDestination(ctx, ada, testServerID, BuildDestinationInput{Address: "10.0.5.21"})
		require.Error(t, err, name)
		assert.Len(t, f.store.lists[testServerID], 1, name)
		assert.Equal(t, 3, countOf(f.server.sh.cmds, loadBuildDestinationsCmd), name)
		assert.Contains(t, f.server.loaded(), "ip daddr 10.0.5.20 ", name)
		assert.NotContains(t, f.server.loaded(), "10.0.5.21", name)
	}

	// A refusal, or a server that never answered, touched nothing: one ask.
	f := newDestFixture()
	f.server.down = true
	_, err := f.svc.AddBuildDestination(ctx, ada, testServerID, BuildDestinationInput{Address: "10.0.5.20"})
	require.ErrorIs(t, err, ErrBuildDestinationsNotApplied)
	assert.Equal(t, []string{hostNetworksScript}, f.server.sh.cmds)
}

// failingCommit fails its first left commits, each after its writes.
type failingCommit struct {
	fakeTx
	left int
}

func (f *failingCommit) InTx(ctx context.Context, fn func(context.Context) error) error {
	return f.fakeTx.InTx(ctx, func(ctx context.Context) error {
		if err := fn(ctx); err != nil || f.left == 0 {
			return err
		}
		f.left--
		return errors.New("commit: connection lost")
	})
}

// Two spellings of a server's ID are one list under one lock.
func TestAChangeLocksAServersListByItsOwnID(t *testing.T) {
	f := newDestFixture()
	repo := f.svc.repo.(*fakeRepo)
	repo.servers["{"+testServerID+"}"] = repo.servers[testServerID]
	_, err := f.svc.AddBuildDestination(context.Background(), ada, "{"+testServerID+"}", BuildDestinationInput{Address: "10.0.5.20"})
	require.NoError(t, err)
	assert.Equal(t, []string{testServerID}, f.store.locks)
	assert.Len(t, f.store.lists[testServerID], 1)
}

// ─── What loads a firewall writes the stored list ─────────────────────────────

func TestProvisionWritesTheStoredListAfterTheFirewall(t *testing.T) {
	answer := func(cmd string) string {
		switch {
		case strings.HasPrefix(cmd, "LC_ALL=C ufw status"):
			return "Status: active"
		case strings.HasPrefix(cmd, "nft list table ip toft"):
			return "loaded"
		}
		return ""
	}
	sh := &fakeShell{answer: answer}
	var at int
	var through Shell
	opts := provisionOpts{ACMEEmail: "ops@example.com", SSHPort: 22, BuildDestinations: func(sc Shell, _ func(string)) error {
		at, through = len(sh.cmds), sc
		return errors.New("the panel's host can't be asked")
	}}
	var lines []string
	warnings, err := provision(sh, opts, func(l string) { lines = append(lines, l) })
	require.NoError(t, err, "a list that isn't loaded is closed, never a failed Provision")
	assert.Empty(t, warnings)
	assert.Same(t, sh, through)
	assert.Greater(t, at, indexOf(sh.cmds, "systemctl restart toft-firewall"))
	assert.True(t, slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "Warning: the build destinations were not written") }))

	// Without the firewall there is no chain to write to.
	sh = &fakeShell{answer: answer, fails: func(cmd string) bool { return cmd == "nft -c -f -" }}
	called := false
	opts.BuildDestinations = func(Shell, func(string)) error { called = true; return nil }
	_, err = provision(sh, opts, func(string) {})
	require.NoError(t, err)
	assert.False(t, called)
}

func TestInstallGvisorWritesTheStoredListAfterTheFirewall(t *testing.T) {
	h := freshGvisorHost()
	loaded := false
	_, err := installGvisor(h, false, func(sc Shell, _ func(string)) error {
		loaded = h.firewall
		assert.Same(t, h, sc)
		return nil
	}, func(string) {})
	require.NoError(t, err)
	assert.True(t, loaded, "the list is written once the firewall is loaded")

	h = freshGvisorHost()
	var lines []string
	_, err = installGvisor(h, false, func(Shell, func(string)) error { return errors.New("refused") }, func(l string) { lines = append(lines, l) })
	require.NoError(t, err, "as at Provision: a list that isn't written doesn't stop the install")
	assert.True(t, slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "Warning: the build destinations were not written") }))
}

// The applier Provision and the gVisor installs get writes what is stored,
// under the list's lock, through the shell it is handed.
func TestTheApplierWritesTheStoredList(t *testing.T) {
	f := newDestFixture()
	ctx := context.Background()
	f.store.lists[testServerID] = []BuildDestination{dest("10.0.5.20/32", 5000), dest("10.0.0.5/32", 0)}
	f.store.lists[""] = []BuildDestination{dest("192.168.7.7/32", 0)}

	var lines []string
	require.NoError(t, f.svc.BuildDestinationsApplier(ctx, testServerID)(f.server.sh, func(l string) { lines = append(lines, l) }))
	assert.Contains(t, f.server.loaded(), "ip daddr 10.0.5.20 tcp dport 5000 counter accept")
	assert.NotContains(t, f.server.loaded(), "ip daddr 10.0.0.5", "an entry that came to hold the server's own address is left out")
	assert.Contains(t, strings.Join(lines, "\n"), "Build destination left out: 10.0.0.5 holds 10.0.0.5")
	assert.Len(t, f.store.lists[testServerID], 2, "and stays listed")
	assert.Equal(t, []string{testServerID}, f.store.locks)

	require.NoError(t, f.svc.BuildDestinationsApplier(ctx, "")(f.panel.sh, func(string) {}))
	assert.Contains(t, f.panel.loaded(), "ip daddr 192.168.7.7 meta l4proto tcp counter accept")

	assert.Nil(t, NewService(&fakeRepo{}, "").BuildDestinationsApplier(ctx, testServerID), "no lists, nothing to write")
}

// Every pass writes every server's stored list: a host that lost its file
// (a restore doesn't bring /etc/toft back) has it again, one that didn't
// answer is asked again, and a list edited by hand on a server is the
// stored one again after the next pass.
func TestEveryPassWritesEveryStoredList(t *testing.T) {
	f := newDestFixture()
	ctx := context.Background()
	f.store.lists[""] = []BuildDestination{dest("192.168.7.7/32", 0)}
	f.store.lists[testServerID] = []BuildDestination{dest("10.0.5.20/32", 5000)}
	f.server.down = true

	f.svc.applyAllBuildDestinations(ctx)
	assert.Contains(t, f.panel.loaded(), "ip daddr 192.168.7.7 meta l4proto tcp counter accept")
	assert.Empty(t, f.server.loaded(), "a server didn't answer")

	f.server.down = false
	f.svc.applyAllBuildDestinations(ctx)
	assert.Contains(t, f.server.loaded(), "ip daddr 10.0.5.20 tcp dport 5000 counter accept")

	// Someone opens another address on the server by hand, and empties
	// the panel host's list.
	byHand := func(h *destHost, list string) {
		h.sh.cmds = append(h.sh.cmds, loadBuildDestinationsCmd)
		h.sh.inputs = append(h.sh.inputs, list)
	}
	byHand(f.server, "table ip toft { chain build_destinations { ip daddr 10.9.9.9 counter accept } }")
	byHand(f.panel, "flush chain ip toft build_destinations")
	require.Contains(t, f.server.loaded(), "10.9.9.9")
	f.svc.applyAllBuildDestinations(ctx)
	assert.NotContains(t, f.server.loaded(), "10.9.9.9")
	assert.Contains(t, f.server.loaded(), "ip daddr 10.0.5.20 tcp dport 5000 counter accept")
	assert.Contains(t, f.panel.loaded(), "ip daddr 192.168.7.7 meta l4proto tcp counter accept")

	// A server with no stored entries: a list it still has is emptied, and
	// nothing is installed there.
	delete(f.store.lists, testServerID)
	f.server.sh.cmds, f.server.sh.inputs = nil, nil
	f.svc.applyAllBuildDestinations(ctx)
	assert.Equal(t, []string{"true", clearBuildDestinationsCmd}, f.server.sh.cmds)
}

// ─── The routes ───────────────────────────────────────────────────────────────

func TestBuildDestinationRoutes(t *testing.T) {
	const serverPath = "/api/v1/servers/" + testServerID + "/build-destinations"
	const panelPath = "/api/v1/instance/build-destinations"
	in := map[string]any{"address": "10.0.5.20", "port": 5000, "note": "registry"}
	code := func(body map[string]any) any { return body["data"].(map[string]any)["code"] }

	t.Run("an operator adds, lists and removes, on a server and on the panel's", func(t *testing.T) {
		for _, path := range []string{serverPath, panelPath} {
			f := newDestFixture()
			r := serverRouter(f.svc, &fakeAudit{}, ada, "org-a", true)
			status, body := callJSON(t, r, http.MethodPost, path, in)
			require.Equal(t, http.StatusCreated, status, body)
			d := body["data"].(map[string]any)
			assert.Equal(t, "10.0.5.20", d["address"])
			assert.Equal(t, float64(5000), d["port"])
			assert.Equal(t, "registry", d["note"])

			status, body = callJSON(t, r, http.MethodGet, path, nil)
			require.Equal(t, http.StatusOK, status, body)
			require.Len(t, body["data"], 1)

			status, _ = callJSON(t, r, http.MethodDelete, path+"/"+d["id"].(string), nil)
			assert.Equal(t, http.StatusNoContent, status)
			status, body = callJSON(t, r, http.MethodGet, path, nil)
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, []any{}, body["data"], "an empty list is a list")
			status, _ = callJSON(t, r, http.MethodDelete, path+"/"+d["id"].(string), nil)
			assert.Equal(t, http.StatusNotFound, status)
		}
	})

	t.Run("nobody else, the server's owner neither", func(t *testing.T) {
		f := newDestFixture()
		r := serverRouter(f.svc, &fakeAudit{}, bob, "org-a", true)
		for _, path := range []string{serverPath, panelPath} {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				status, _ := callJSON(t, r, method, path, in)
				assert.Equal(t, http.StatusForbidden, status, method+" "+path)
			}
			status, _ := callJSON(t, r, http.MethodDelete, path+"/d1", nil)
			assert.Equal(t, http.StatusForbidden, status, path)
		}
		assert.Empty(t, f.store.lists)
	})

	t.Run("a change needs the password again", func(t *testing.T) {
		f := newDestFixture()
		r := serverRouter(f.svc, &fakeAudit{}, ada, "org-a", false)
		status, body := callJSON(t, r, http.MethodPost, serverPath, in)
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "reauth_required", code(body))
		status, body = callJSON(t, r, http.MethodDelete, panelPath+"/d1", nil)
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "reauth_required", code(body))
		status, _ = callJSON(t, r, http.MethodGet, serverPath, nil)
		assert.Equal(t, http.StatusOK, status, "reading doesn't")
	})

	t.Run("a server of another workspace isn't there", func(t *testing.T) {
		f := newDestFixture()
		status, _ := callJSON(t, serverRouter(f.svc, &fakeAudit{}, ada, "org-b", true), http.MethodPost, serverPath, in)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Empty(t, f.store.lists)
	})

	t.Run("each refusal has its code", func(t *testing.T) {
		f := newDestFixture()
		r := serverRouter(f.svc, &fakeAudit{}, ada, "org-a", true)
		status, body := callJSON(t, r, http.MethodPost, serverPath, map[string]any{"address": "8.8.8.8"})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, codeBuildDestinationInvalid, code(body))
		assert.Equal(t, "address", body["data"].(map[string]any)["field"])
		status, body = callJSON(t, r, http.MethodPost, serverPath, map[string]any{"address": "10.0.5.20", "port": "ssh"})
		assert.Equal(t, http.StatusBadRequest, status, "a body that isn't an entry")
		assert.Equal(t, codeBuildDestinationInvalid, code(body))

		for address, want := range map[string]string{"10.0.0.2": codeBuildDestinationPanel, "10.0.0.5": codeBuildDestinationServer, "172.18.0.9": codeBuildDestinationDocker} {
			status, body = callJSON(t, r, http.MethodPost, serverPath, map[string]any{"address": address})
			assert.Equal(t, http.StatusConflict, status, address)
			assert.Equal(t, want, code(body), address)
		}
		status, _ = callJSON(t, r, http.MethodPost, serverPath, in)
		require.Equal(t, http.StatusCreated, status)
		status, body = callJSON(t, r, http.MethodPost, serverPath, in)
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, codeBuildDestinationExists, code(body))

		f.server.down = true
		status, body = callJSON(t, r, http.MethodPost, serverPath, map[string]any{"address": "10.0.5.21"})
		assert.Equal(t, http.StatusBadGateway, status)
		assert.Equal(t, codeBuildDestinationsUnset, code(body))
		assert.Equal(t, ErrBuildDestinationsNotApplied.Error(), body["error"], "the server's own words stay in the log")
		assert.Len(t, f.store.lists[testServerID], 1)
	})
}
