package main

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerevents "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/network"
	"golang.org/x/net/dns/dnsmessage"
)

// fakeDNSDocker serves containers by ID: network ID → IP and DNSNames.
type fakeDNSDocker struct {
	mu         sync.Mutex
	containers map[string]map[string]*network.EndpointSettings
	lists      int
	inspects   int
}

func (f *fakeDNSDocker) ContainerList(context.Context, dockercontainer.ListOptions) ([]dockercontainer.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lists++
	out := make([]dockercontainer.Summary, 0, len(f.containers))
	for id := range f.containers {
		out = append(out, dockercontainer.Summary{ID: id})
	}
	return out, nil
}

func (f *fakeDNSDocker) ContainerInspect(_ context.Context, id string) (dockercontainer.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspects++
	nets, ok := f.containers[id]
	if !ok {
		return dockercontainer.InspectResponse{}, os.ErrNotExist
	}
	return dockercontainer.InspectResponse{
		// Starting: Docker hasn't called it running yet, and it holds its addresses.
		ContainerJSONBase: &dockercontainer.ContainerJSONBase{State: &dockercontainer.State{Status: "created"}},
		NetworkSettings:   &dockercontainer.NetworkSettings{Networks: nets},
	}, nil
}

func (f *fakeDNSDocker) Events(context.Context, dockerevents.ListOptions) (<-chan dockerevents.Message, <-chan error) {
	return nil, nil
}

func ep(netID, ip string, names ...string) *network.EndpointSettings {
	return &network.EndpointSettings{NetworkID: netID, IPAddress: ip, DNSNames: names}
}

// Two networks: web and db on A, admin on B, and api on both.
func dnsFixture(t *testing.T) (*dnsRelay, *fakeDNSDocker) {
	t.Helper()
	f := &fakeDNSDocker{containers: map[string]map[string]*network.EndpointSettings{
		"web":   {"a": ep("netA", "172.20.0.2", "web", "paas-web-blue", "site")},
		"db":    {"a": ep("netA", "172.20.0.3", "db")},
		"admin": {"b": ep("netB", "172.21.0.2", "admin")},
		"api":   {"a": ep("netA", "172.20.0.4", "api"), "b": ep("netB", "172.21.0.3", "api")},
	}}
	r := &dnsRelay{index: newDNSIndex(), dc: f}
	if err := r.index.rebuild(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	return r, f
}

func query(t *testing.T, name string, typ dnsmessage.Type) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 4242, RecursionDesired: true})
	_ = b.StartQuestions()
	_ = b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: typ, Class: dnsmessage.ClassINET})
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ask(t *testing.T, r *dnsRelay, src, name string, typ dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	out := r.answer(context.Background(), netip.MustParseAddr(src), query(t, name, typ), false)
	var m dnsmessage.Message
	if err := m.Unpack(out); err != nil {
		t.Fatalf("%s from %s: %v", name, src, err)
	}
	if m.Header.ID != 4242 || !m.Header.Response {
		t.Fatalf("%s: header %+v", name, m.Header)
	}
	return m
}

func aRecords(m dnsmessage.Message) []string {
	var ips []string
	for _, a := range m.Answers {
		if r, ok := a.Body.(*dnsmessage.AResource); ok {
			ips = append(ips, netip.AddrFrom4(r.A).String())
		}
	}
	return ips
}

// noUpstream makes forwarding fail fast, so a name the index doesn't hold
// reads as SERVFAIL.
func noUpstream(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := hostResolvConf
	hostResolvConf = filepath.Join(dir, "resolv.conf")
	t.Cleanup(func() { hostResolvConf = old })
}

func TestDNSRelayScope(t *testing.T) {
	noUpstream(t)
	r, _ := dnsFixture(t)
	cases := []struct {
		src, name string
		want      []string
	}{
		{"172.20.0.2", "db.", []string{"172.20.0.3"}},
		{"172.20.0.2", "Db.Example.", nil},               // not a container name
		{"172.20.0.3", "site.", []string{"172.20.0.2"}},  // an alias from DNSNames
		{"172.20.0.2", "api.", []string{"172.20.0.4"}},   // its address on the shared network
		{"172.21.0.2", "api.", []string{"172.21.0.3"}},   // and on the other one
		{"172.20.0.4", "admin.", []string{"172.21.0.2"}}, // api is on both
		{"172.20.0.4", "db.", []string{"172.20.0.3"}},    //
		{"172.20.0.2", "admin.", nil},                    // web isn't on B
		{"172.21.0.2", "db.", nil},                       // admin isn't on A
	}
	for _, c := range cases {
		m := ask(t, r, c.src, c.name, dnsmessage.TypeA)
		got := aRecords(m)
		if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
			t.Errorf("%s asking %s: got %v, want %v", c.src, c.name, got, c.want)
		}
		if c.want == nil && m.Header.RCode != dnsmessage.RCodeServerFailure {
			t.Errorf("%s asking %s: rcode %v, want it forwarded (SERVFAIL without resolvers)", c.src, c.name, m.Header.RCode)
		}
	}
}

func TestDNSIndexCase(t *testing.T) {
	r, _ := dnsFixture(t)
	for _, name := range []string{"DB", "db.", "Db."} {
		if ips := r.index.lookup(netip.MustParseAddr("172.20.0.2"), name); len(ips) != 1 {
			t.Errorf("%q: %v", name, ips)
		}
	}
}

func TestDNSRelayAAAAAndPTR(t *testing.T) {
	noUpstream(t)
	r, _ := dnsFixture(t)
	m := ask(t, r, "172.20.0.2", "db.", dnsmessage.TypeAAAA)
	if m.Header.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
		t.Errorf("AAAA db: %v %v, want NOERROR and no answers", m.Header.RCode, m.Answers)
	}
	m = ask(t, r, "172.20.0.3", "2.0.20.172.in-addr.arpa.", dnsmessage.TypePTR)
	if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.PTRResource).PTR.String() != "web." {
		t.Errorf("PTR 172.20.0.2: %v", m.Answers)
	}
	// admin's address isn't on a network web shares: forwarded.
	m = ask(t, r, "172.20.0.2", "2.0.21.172.in-addr.arpa.", dnsmessage.TypePTR)
	if len(m.Answers) != 0 {
		t.Errorf("PTR across networks answered: %v", m.Answers)
	}
}

func TestDNSRelayUnknownSource(t *testing.T) {
	r, _ := dnsFixture(t)
	m := ask(t, r, "10.9.9.9", agentproto.DNSRelayCheckName, dnsmessage.TypeA)
	if got := aRecords(m); len(got) != 1 || got[0] != agentproto.DNSRelayAddr {
		t.Errorf("check name: %v", got)
	}
	// busybox's nslookup asks AAAA too, and fails on the NXDOMAIN some
	// upstreams give: the relay answers its own name, never forwards it.
	noUpstream(t)
	m = ask(t, r, "10.9.9.9", agentproto.DNSRelayCheckName, dnsmessage.TypeAAAA)
	if m.Header.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
		t.Errorf("check name AAAA: %v %v, want NOERROR and no answers", m.Header.RCode, m.Answers)
	}
	m = ask(t, r, "10.9.9.9", "db.", dnsmessage.TypeA)
	if m.Header.RCode != dnsmessage.RCodeRefused || len(m.Answers) != 0 {
		t.Errorf("unknown source: %v %v, want REFUSED", m.Header.RCode, m.Answers)
	}
}

// A container's first lookups come before Docker's event that it started:
// the relay reads the containers it lacks, and only those. Two starting
// together (a compose stack) are both answered.
func TestDNSRelayRefreshOnMiss(t *testing.T) {
	r, f := dnsFixture(t)
	f.mu.Lock()
	f.containers["new"] = map[string]*network.EndpointSettings{"a": ep("netA", "172.20.0.9", "new")}
	lists, inspects := f.lists, f.inspects
	f.mu.Unlock()
	m := ask(t, r, "172.20.0.9", "db.", dnsmessage.TypeA)
	if got := aRecords(m); len(got) != 1 || got[0] != "172.20.0.3" {
		t.Errorf("a container that started since: %v %v", m.Header.RCode, got)
	}
	if f.lists != lists+1 || f.inspects != inspects+1 {
		t.Errorf("listed %d times and inspected %d, want 1 and 1 (the new one)", f.lists-lists, f.inspects-inspects)
	}

	f.mu.Lock()
	f.containers["next"] = map[string]*network.EndpointSettings{"a": ep("netA", "172.20.0.10", "next")}
	f.mu.Unlock()
	m = ask(t, r, "172.20.0.10", "new.", dnsmessage.TypeA)
	if got := aRecords(m); len(got) != 1 || got[0] != "172.20.0.9" {
		t.Errorf("a second container right after: %v %v", m.Header.RCode, got)
	}

}

func TestDNSRelayUpdateAndSet(t *testing.T) {
	r, f := dnsFixture(t)
	f.mu.Lock()
	delete(f.containers, "db")
	f.mu.Unlock()
	r.index.update(context.Background(), f, "db")
	if ips := r.index.lookup(netip.MustParseAddr("172.20.0.2"), "db"); len(ips) != 0 {
		t.Errorf("a removed container still resolves: %v", ips)
	}
	if r.index.knows(netip.MustParseAddr("172.20.0.3")) {
		t.Error("a removed container's address is still known")
	}
}

// fakeResolver answers every question with one A record, over UDP.
func fakeResolver(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			h, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, _ := p.Question()
			out := dnsReply(h, q, dnsmessage.RCodeSuccess, []netip.Addr{netip.MustParseAddr("93.184.216.34")}, "")
			_, _ = pc.WriteTo(out, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDNSRelayForwards(t *testing.T) {
	addr := fakeResolver(t)
	out, err := forward(context.Background(), query(t, "example.com.", dnsmessage.TypeA), false, []string{"127.0.0.1:1", addr})
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(out); err != nil {
		t.Fatal(err)
	}
	if m.Header.ID != 4242 {
		t.Errorf("ID %d, want the client's", m.Header.ID)
	}
	if got := aRecords(m); len(got) != 1 || got[0] != "93.184.216.34" {
		t.Errorf("forwarded answer: %v", got)
	}
}

func TestUpstreams(t *testing.T) {
	dir := t.TempDir()
	oldHost, oldResolved := hostResolvConf, resolvedResolvConf
	t.Cleanup(func() { hostResolvConf, resolvedResolvConf = oldHost, oldResolved })
	hostResolvConf, resolvedResolvConf = filepath.Join(dir, "host"), filepath.Join(dir, "resolved")
	_ = os.WriteFile(hostResolvConf, []byte("nameserver 127.0.0.53\noptions edns0\n"), 0o600)
	_ = os.WriteFile(resolvedResolvConf, []byte("# real\nnameserver 185.12.64.1\nnameserver 2a01:4ff:ff00::add:1\n"), 0o600)
	got := upstreams()
	if len(got) != 2 || got[0] != "185.12.64.1:53" || got[1] != "[2a01:4ff:ff00::add:1]:53" {
		t.Errorf("behind systemd-resolved: %v", got)
	}
	_ = os.WriteFile(hostResolvConf, []byte("nameserver 1.1.1.1\n"), 0o600)
	if got := upstreams(); len(got) != 1 || got[0] != "1.1.1.1:53" {
		t.Errorf("plain: %v", got)
	}
}

// One source gets dnsMaxPerSource questions at once; others aren't held
// back by it.
func TestDNSRelayPerSourceShare(t *testing.T) {
	r := &dnsRelay{}
	a, b := netip.MustParseAddr("172.18.0.2"), netip.MustParseAddr("172.18.0.3")
	for range dnsMaxPerSource {
		if !r.take(a) {
			t.Fatal("a share within the limit was refused")
		}
	}
	if r.take(a) || !r.take(b) {
		t.Fatal("one source's limit should hold it alone")
	}
	if r.done(a); !r.take(a) {
		t.Fatal("a returned share wasn't given again")
	}
}
