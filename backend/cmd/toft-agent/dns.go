package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/agentproto"
	dockercontainer "github.com/docker/docker/api/types/container"
	dockerevents "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"golang.org/x/net/dns/dnsmessage"
)

// The DNS relay (D-115). A container that runs Sandboxed with a strict
// network uses gVisor's own network stack, which can't reach Docker's DNS
// at 127.0.0.11, so its /etc/resolv.conf points at the agent instead. The
// agent answers as Docker's DNS would: a name from the DNSNames of a
// container on a network the asker is on, with that container's address
// there. Anything else goes to the host's resolvers. The asker is known by
// its source address: Docker's networks on one host don't overlap.

const (
	dnsTTL = 600 // as Docker's DNS
	// dnsResync rebuilds the index besides Docker's events.
	dnsResync = 5 * time.Minute
	// dnsUpstreamWait bounds one question to one of the host's resolvers.
	dnsUpstreamWait = 2 * time.Second
	// dnsMaxInFlight bounds the questions answered at once, and
	// dnsMaxPerSource one source's share, so a container asking for names
	// whose servers never answer can't starve the others.
	// ponytail: per address, so one tenant with many containers gets many
	// shares; per workspace if that's ever seen.
	dnsMaxInFlight  = 256
	dnsMaxPerSource = 16
	// dnsDockerWait bounds a refresh's reads of Docker.
	dnsDockerWait = 5 * time.Second
	// dnsRefreshGap spaces the reads an unknown source starts, so a flood
	// from one can't keep Docker busy.
	dnsRefreshGap = 100 * time.Millisecond
)

// The host's resolvers, and systemd-resolved's real ones behind its stub.
var (
	hostResolvConf     = "/etc/resolv.conf"
	resolvedResolvConf = "/run/systemd/resolve/resolv.conf"
)

// dnsDocker is what the relay asks Docker; *client.Client satisfies it.
type dnsDocker interface {
	ContainerList(ctx context.Context, opts dockercontainer.ListOptions) ([]dockercontainer.Summary, error)
	ContainerInspect(ctx context.Context, id string) (dockercontainer.InspectResponse, error)
	Events(ctx context.Context, opts dockerevents.ListOptions) (<-chan dockerevents.Message, <-chan error)
}

// endpoint is one container's place on one network.
type endpoint struct {
	network string
	ip      netip.Addr
	names   []string
}

// dnsIndex is every running container's endpoints. Safe for concurrent use.
type dnsIndex struct {
	mu          sync.RWMutex
	byContainer map[string][]endpoint
	byIP        map[netip.Addr]string // the container with this address

	refreshMu   sync.Mutex
	refreshedAt time.Time // when the last refresh began reading
}

func newDNSIndex() *dnsIndex {
	return &dnsIndex{byContainer: map[string][]endpoint{}, byIP: map[netip.Addr]string{}}
}

// set replaces a container's endpoints; none removes it.
func (x *dnsIndex) set(id string, eps []endpoint) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, ep := range x.byContainer[id] {
		if x.byIP[ep.ip] == id {
			delete(x.byIP, ep.ip)
		}
	}
	delete(x.byContainer, id)
	if len(eps) == 0 {
		return
	}
	x.byContainer[id] = eps
	for _, ep := range eps {
		x.byIP[ep.ip] = id
	}
}

// replace swaps in a whole new index.
func (x *dnsIndex) replace(all map[string][]endpoint) {
	byIP := map[netip.Addr]string{}
	for id, eps := range all {
		for _, ep := range eps {
			byIP[ep.ip] = id
		}
	}
	x.mu.Lock()
	x.byContainer, x.byIP = all, byIP
	x.mu.Unlock()
}

// shared returns the networks of the container at src, and whether src is
// one.
func (x *dnsIndex) shared(src netip.Addr) (map[string]bool, bool) {
	id, ok := x.byIP[src]
	if !ok {
		return nil, false
	}
	nets := map[string]bool{}
	for _, ep := range x.byContainer[id] {
		nets[ep.network] = true
	}
	return nets, true
}

func (x *dnsIndex) knows(src netip.Addr) bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	_, ok := x.byIP[src]
	return ok
}

// lookup returns the addresses of name on the networks the container at src
// is on.
func (x *dnsIndex) lookup(src netip.Addr, name string) []netip.Addr {
	name = strings.TrimSuffix(name, ".")
	x.mu.RLock()
	defer x.mu.RUnlock()
	nets, ok := x.shared(src)
	if !ok {
		return nil
	}
	var ips []netip.Addr
	for _, eps := range x.byContainer {
		for _, ep := range eps {
			if !nets[ep.network] {
				continue
			}
			for _, n := range ep.names {
				if strings.EqualFold(n, name) {
					ips = append(ips, ep.ip)
					break
				}
			}
		}
	}
	return ips
}

// reverse returns the first name of the container with address ip on a
// network the container at src is on.
func (x *dnsIndex) reverse(src, ip netip.Addr) string {
	x.mu.RLock()
	defer x.mu.RUnlock()
	nets, ok := x.shared(src)
	if !ok {
		return ""
	}
	for _, ep := range x.byContainer[x.byIP[ip]] {
		if ep.ip == ip && nets[ep.network] && len(ep.names) > 0 {
			return ep.names[0]
		}
	}
	return ""
}

// endpointsOf reads a container's endpoints: the networks it holds an
// address on. A starting container holds them before Docker calls it
// running, and its first lookups come then; a stopped one holds none.
func endpointsOf(c dockercontainer.InspectResponse) []endpoint {
	if c.NetworkSettings == nil {
		return nil
	}
	var eps []endpoint
	for name, n := range c.NetworkSettings.Networks {
		if n == nil {
			continue
		}
		ip, err := netip.ParseAddr(n.IPAddress)
		if err != nil {
			continue
		}
		network := n.NetworkID
		if network == "" {
			network = name
		}
		eps = append(eps, endpoint{network: network, ip: ip, names: n.DNSNames})
	}
	return eps
}

// liveContainers lists the containers that may hold addresses, a starting
// one ("created" until it runs) included.
func liveContainers() dockercontainer.ListOptions {
	return dockercontainer.ListOptions{Filters: filters.NewArgs(filters.Arg("status", "created"), filters.Arg("status", "running"),
		filters.Arg("status", "restarting"), filters.Arg("status", "paused"))}
}

// rebuild reads every live container. Docker's list carries no DNSNames, so
// each is inspected.
func (x *dnsIndex) rebuild(ctx context.Context, dc dnsDocker) error {
	list, err := dc.ContainerList(ctx, liveContainers())
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	all := map[string][]endpoint{}
	for _, s := range list {
		c, err := dc.ContainerInspect(ctx, s.ID)
		if err != nil {
			continue // gone since the list
		}
		if eps := endpointsOf(c); len(eps) > 0 {
			all[s.ID] = eps
		}
	}
	x.replace(all)
	return nil
}

// refresh reads the live containers the index lacks, for a question from
// an unknown source asked at asked: a container's first lookups often come
// before Docker's event that it started. Questions waiting on one read
// share it when it began after they were asked; reads begin dnsRefreshGap
// apart at most.
func (x *dnsIndex) refresh(ctx context.Context, dc dnsDocker, asked time.Time) {
	x.refreshMu.Lock()
	defer x.refreshMu.Unlock()
	if x.refreshedAt.After(asked) {
		return
	}
	if wait := dnsRefreshGap - time.Since(x.refreshedAt); wait > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
	x.refreshedAt = time.Now()
	ctx, cancel := context.WithTimeout(ctx, dnsDockerWait)
	defer cancel()
	list, err := dc.ContainerList(ctx, liveContainers())
	if err != nil {
		slog.Warn("dns relay: could not read the containers", "error", err)
		return
	}
	for _, s := range list {
		x.mu.RLock()
		_, known := x.byContainer[s.ID]
		x.mu.RUnlock()
		if !known {
			x.update(ctx, dc, s.ID)
		}
	}
}

// update reads the one container an event names.
func (x *dnsIndex) update(ctx context.Context, dc dnsDocker, id string) {
	c, err := dc.ContainerInspect(ctx, id)
	if err != nil {
		x.set(id, nil) // destroyed
		return
	}
	x.set(id, endpointsOf(c))
}

// watch keeps the index current until ctx ends: the container an event
// names after it starts, stops, goes, or joins or leaves a network, and a
// whole rebuild every dnsResync. The rebuild runs between events, so one
// that arrives while it reads is applied after it.
func (x *dnsIndex) watch(ctx context.Context, dc dnsDocker) {
	t := time.NewTicker(dnsResync)
	defer t.Stop()
	for ctx.Err() == nil {
		msgs, errs := dc.Events(ctx, dockerevents.ListOptions{Filters: filters.NewArgs(
			filters.Arg("type", string(dockerevents.ContainerEventType)),
			filters.Arg("type", string(dockerevents.NetworkEventType)),
			filters.Arg("event", "start"), filters.Arg("event", "die"), filters.Arg("event", "destroy"),
			filters.Arg("event", "connect"), filters.Arg("event", "disconnect"))})
		// Read everything after subscribing, so no change falls between.
		if err := x.rebuild(ctx, dc); err != nil {
			slog.Warn("dns relay: could not read the containers", "error", err)
		}
	read:
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-msgs:
				id := m.Actor.ID
				if m.Type == dockerevents.NetworkEventType {
					id = m.Actor.Attributes["container"]
				}
				if id != "" {
					x.update(ctx, dc, id)
				}
			case <-t.C:
				if err := x.rebuild(ctx, dc); err != nil {
					slog.Warn("dns relay: could not read the containers", "error", err)
				}
			case <-errs:
				break read
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second): // Docker restarting
		}
	}
}

// dnsRelay answers questions from the index, and forwards the rest.
type dnsRelay struct {
	index *dnsIndex
	dc    dnsDocker

	mu       sync.Mutex
	inFlight map[netip.Addr]int // questions being answered, per source
}

// take reserves one of src's dnsMaxPerSource shares; done returns it.
func (r *dnsRelay) take(src netip.Addr) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight[src] >= dnsMaxPerSource {
		return false
	}
	if r.inFlight == nil {
		r.inFlight = map[netip.Addr]int{}
	}
	r.inFlight[src]++
	return true
}

func (r *dnsRelay) done(src netip.Addr) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight[src]--; r.inFlight[src] <= 0 {
		delete(r.inFlight, src)
	}
}

// answer returns the response to msg from src, or nil to drop it.
func (r *dnsRelay) answer(ctx context.Context, src netip.Addr, msg []byte, tcp bool) []byte {
	asked := time.Now()
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || h.Response {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	name := q.Name.String()
	if strings.EqualFold(name, agentproto.DNSRelayCheckName) {
		var ips []netip.Addr
		if q.Type == dnsmessage.TypeA {
			ips = []netip.Addr{netip.MustParseAddr(agentproto.DNSRelayAddr)}
		}
		return dnsReply(h, q, dnsmessage.RCodeSuccess, ips, "")
	}
	if !r.index.knows(src) {
		r.index.refresh(ctx, r.dc, asked)
		if !r.index.knows(src) {
			return dnsReply(h, q, dnsmessage.RCodeRefused, nil, "")
		}
	}
	switch q.Type {
	case dnsmessage.TypeA, dnsmessage.TypeAAAA:
		if ips := r.index.lookup(src, name); len(ips) > 0 {
			if q.Type == dnsmessage.TypeAAAA {
				ips = nil // containers have IPv4 addresses only
			}
			return dnsReply(h, q, dnsmessage.RCodeSuccess, ips, "")
		}
	case dnsmessage.TypePTR:
		if ip, ok := ptrAddr(name); ok {
			if n := r.index.reverse(src, ip); n != "" {
				return dnsReply(h, q, dnsmessage.RCodeSuccess, nil, n)
			}
		}
	}
	out, err := forward(ctx, msg, tcp, upstreams())
	if err != nil {
		slog.Debug("dns relay: no resolver answered", "name", name, "error", err)
		return dnsReply(h, q, dnsmessage.RCodeServerFailure, nil, "")
	}
	return out
}

// dnsReply builds a response to q: A records for ips, or a PTR to ptr.
func dnsReply(req dnsmessage.Header, q dnsmessage.Question, rcode dnsmessage.RCode, ips []netip.Addr, ptr string) []byte {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: req.ID, Response: true, RecursionDesired: req.RecursionDesired, RecursionAvailable: true, RCode: rcode})
	b.EnableCompression()
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	rh := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: dnsTTL}
	for _, ip := range ips {
		_ = b.AResource(rh, dnsmessage.AResource{A: ip.As4()})
	}
	if ptr != "" {
		if n, err := dnsmessage.NewName(strings.TrimSuffix(ptr, ".") + "."); err == nil {
			_ = b.PTRResource(rh, dnsmessage.PTRResource{PTR: n})
		}
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}

// ptrAddr reads 4.3.2.1.in-addr.arpa. as 1.2.3.4.
func ptrAddr(name string) (netip.Addr, bool) {
	rest, ok := strings.CutSuffix(strings.ToLower(name), ".in-addr.arpa.")
	if !ok {
		return netip.Addr{}, false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 4 {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(parts[3] + "." + parts[2] + "." + parts[1] + "." + parts[0])
	return ip, err == nil && ip.Is4()
}

// upstreams are the host's resolvers, read each time as Docker's DNS does:
// /etc/resolv.conf's, or systemd-resolved's own when the file names only
// its stub.
func upstreams() []string {
	ns := nameservers(hostResolvConf)
	if len(ns) == 1 && ns[0] == "127.0.0.53" {
		if real := nameservers(resolvedResolvConf); len(real) > 0 {
			ns = real
		}
	}
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, net.JoinHostPort(n, "53"))
	}
	return out
}

func nameservers(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ns []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			if _, err := netip.ParseAddr(f[1]); err == nil {
				ns = append(ns, f[1])
			}
		}
	}
	return ns
}

// forward asks each resolver in turn: over UDP, or TCP when the client used
// TCP or the UDP answer came back truncated. The answer keeps the client's
// ID, which the query already carries.
func forward(ctx context.Context, msg []byte, tcp bool, servers []string) ([]byte, error) {
	if len(servers) == 0 {
		return nil, errors.New("the host has no resolvers")
	}
	var last error
	for _, s := range servers {
		out, err := exchange(ctx, s, msg, tcp)
		if err == nil && !tcp && truncated(out) {
			out, err = exchange(ctx, s, msg, true)
		}
		if err == nil {
			return out, nil
		}
		last = err
	}
	return nil, last
}

func truncated(msg []byte) bool {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	return err == nil && h.Truncated
}

func exchange(ctx context.Context, server string, msg []byte, tcp bool) ([]byte, error) {
	network := "udp"
	if tcp {
		network = "tcp"
	}
	ctx, cancel := context.WithTimeout(ctx, dnsUpstreamWait)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, network, server)
	if err != nil {
		return nil, fmt.Errorf("dial %s %s: %w", network, server, err)
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if tcp {
		if err := writeTCPMsg(c, msg); err != nil {
			return nil, fmt.Errorf("ask %s: %w", server, err)
		}
		out, err := readTCPMsg(c)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", server, err)
		}
		return out, nil
	}
	if _, err := c.Write(msg); err != nil {
		return nil, fmt.Errorf("ask %s: %w", server, err)
	}
	buf := make([]byte, 65535)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", server, err)
		}
		if n >= 2 && n <= len(buf) && buf[0] == msg[0] && buf[1] == msg[1] {
			return buf[:n], nil
		}
		// not our answer: keep waiting until the deadline
	}
}

func writeTCPMsg(w io.Writer, msg []byte) error {
	out := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(out, uint16(len(msg)))
	copy(out[2:], msg)
	_, err := w.Write(out)
	return err
}

func readTCPMsg(r io.Reader) ([]byte, error) {
	var n [2]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	msg := make([]byte, binary.BigEndian.Uint16(n[:]))
	_, err := io.ReadFull(r, msg)
	return msg, err
}

// serveUDP answers until ctx ends, which closes pc.
func (r *dnsRelay) serveUDP(ctx context.Context, pc net.PacketConn) {
	sem := make(chan struct{}, dnsMaxInFlight)
	for {
		buf := make([]byte, 4096) // a question; EDNS clients offer at most this
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		src, ok := addrOf(from)
		if !ok {
			continue
		}
		if !r.take(src) {
			continue // too many from it: the client asks again
		}
		select {
		case sem <- struct{}{}:
		default:
			r.done(src)
			continue // too many at once: the client asks again
		}
		go func() {
			defer func() { <-sem; r.done(src) }()
			if out := r.answer(ctx, src, buf[:n], false); out != nil {
				_, _ = pc.WriteTo(out, from)
			}
		}()
	}
}

// serveTCP answers until ctx ends, which closes l.
func (r *dnsRelay) serveTCP(ctx context.Context, l net.Listener) {
	sem := make(chan struct{}, dnsMaxInFlight)
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		src, ok := addrOf(c.RemoteAddr())
		if ok && !r.take(src) {
			ok = false
		} else if ok {
			select {
			case sem <- struct{}{}:
			default:
				r.done(src)
				ok = false
			}
		}
		if !ok {
			c.Close()
			continue
		}
		go func() {
			defer func() { <-sem; r.done(src) }()
			defer c.Close()
			for {
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				msg, err := readTCPMsg(c)
				if err != nil {
					return
				}
				out := r.answer(ctx, src, msg, true)
				if out == nil || writeTCPMsg(c, out) != nil {
					return
				}
			}
		}()
	}
}

func addrOf(a net.Addr) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(a.String())
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// startDNSRelay sets the relay up and serves it until ctx ends: the dummy
// interface with its address, the resolv.conf strict containers mount, and
// the two listeners. An error means the agent doesn't announce CapDNS.
func startDNSRelay(ctx context.Context, dc dnsDocker) error {
	if runtime.GOOS != "linux" {
		return errors.New("the DNS relay needs Linux")
	}
	if err := relayInterface(); err != nil {
		return err
	}
	if err := writeResolvConf(); err != nil {
		return err
	}
	addr := net.JoinHostPort(agentproto.DNSRelayAddr, "53")
	pc, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return fmt.Errorf("listen udp %s: %w", addr, err)
	}
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		pc.Close()
		return fmt.Errorf("listen tcp %s: %w", addr, err)
	}
	r := &dnsRelay{index: newDNSIndex(), dc: dc}
	go func() {
		<-ctx.Done()
		pc.Close()
		l.Close()
	}()
	go r.index.watch(ctx, dc)
	go r.serveUDP(ctx, pc)
	go r.serveTCP(ctx, l)
	return nil
}

// relayInterface makes the dummy interface and gives it the relay's
// address; each step is safe to repeat.
func relayInterface() error {
	run := func(args ...string) error {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
		}
		return nil
	}
	if exec.Command("ip", "link", "show", agentproto.DNSRelayInterface).Run() != nil {
		if err := run("link", "add", agentproto.DNSRelayInterface, "type", "dummy"); err != nil {
			return err
		}
	}
	if err := run("addr", "replace", agentproto.DNSRelayAddr+"/32", "dev", agentproto.DNSRelayInterface); err != nil {
		return err
	}
	return run("link", "set", agentproto.DNSRelayInterface, "up")
}

// removeRelayInterface undoes relayInterface, on uninstall.
func removeRelayInterface() {
	_ = exec.Command("ip", "link", "del", agentproto.DNSRelayInterface).Run()
}

// writeResolvConf writes the file strict containers mount over their
// /etc/resolv.conf, temp-then-rename.
func writeResolvConf() error {
	path := agentproto.StrictResolvConf
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("nameserver "+agentproto.DNSRelayAddr+"\noptions ndots:0\n"), 0o644); err != nil { //nolint:gosec // read by containers
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}
