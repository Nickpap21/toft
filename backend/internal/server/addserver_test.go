package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerNameRule(t *testing.T) {
	for name, ok := range map[string]bool{
		"hel1-prod-03": true, "a": true, "server-12": true, "9": true,
		"": false, "Hel1": false, "-web": false, "web-": false, "web_1": false, "web 1": false, "wéb": false,
		"a234567890123456789012345678901234567890123456789012345678901234": false, // 64
		"a23456789012345678901234567890123456789012345678901234567890123":  true,  // 63
	} {
		assert.Equal(t, ok, ValidServerName(name), name)
	}
}

func TestNameFromHostname(t *testing.T) {
	for host, want := range map[string]string{
		"hel1-prod-03\n":             "hel1-prod-03",
		"Hel1_Prod.03.example.com":   "hel1-prod",
		"ubuntu-s-1vcpu-1gb-fra1-01": "ubuntu-s-1vcpu-1gb-fra1-01",
		"--x--":                      "x",
		"":                           "",
		"日本":                         "",
		"a-very-long-hostname-that-goes-on-and-on-and-on-past-sixty-three-chars": "a-very-long-hostname-that-goes-on-and-on-and-on-past-sixty-thre",
	} {
		got := nameFromHostname(host)
		assert.Equal(t, want, got, host)
		if got != "" {
			assert.True(t, ValidServerName(got), got)
		}
	}
}

// The first join provisions a server Add a server made, as the person who
// made the command: once, and never over a run that already happened, a
// provisioned server, or one moved to the agent without a command.
func TestProvisionOnFirstJoin(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		used, provisioned, ran bool
		want                   string
	}{
		"joined from Add a server": {used: true, want: "u1"},
		"already provisioned":      {used: true, provisioned: true},
		"tried before":             {used: true, ran: true, want: "earlier"},
		"moved without a command":  {},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := &Server{ID: "s1", Name: "server-1", OrgID: "o1", Connection: ConnectionAgent, IPAddress: "192.0.2.1"}
			if c.provisioned {
				now := time.Now()
				srv.ProvisionedAt = &now
			}
			repo := newFakeRepo(srv)
			if c.ran {
				require.NoError(t, repo.StartProvisionRun(ctx, "s1", "earlier"))
			}
			svc := NewService(repo, "")
			store := newFakeAgents()
			svc.WithAgents(store, &fakeHub{started: time.Now()}, nil, t.TempDir())
			if c.used {
				store.tokens["h"] = &fakeToken{server: "s1", by: "u1", used: true}
			}

			svc.provisionOnFirstJoin(ctx, srv)
			svc.provisionOnFirstJoin(ctx, srv) // a second connect at once starts nothing more

			var run *ProvisionRun
			assert.Eventually(t, func() bool {
				_, busy := svc.firstJoins.Load("s1")
				run, _ = repo.GetProvisionRun(ctx, "s1")
				return !busy
			}, 5*time.Second, 10*time.Millisecond)
			if c.want == "" {
				assert.Nil(t, run)
				return
			}
			require.NotNil(t, run)
			assert.Equal(t, c.want, run.StartedBy)
		})
	}
}

// Provisioning's first step names a server still named server-<n> after
// its hostname: the next free -2..-5 when it's taken, and nothing when
// hostname fails or says nothing usable.
func TestNameFromHost(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		name, host string
		fails      bool
		want, line string
	}{
		"automatic":      {name: "server-3", host: "hel1-prod-03\n", want: "hel1-prod-03", line: "Named hel1-prod-03, from its hostname"},
		"taken":          {name: "server-3", host: "web-1.example.com\n", want: "web-1-3", line: "Named web-1-3, from its hostname"},
		"chosen name":    {name: "shop-db", host: "hel1-prod-03\n", want: "shop-db"},
		"hostname fails": {name: "server-3", fails: true, want: "server-3"},
		"nothing usable": {name: "server-3", host: "日本\n", want: "server-3"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := &Server{ID: "s3", Name: c.name, OrgID: "o1"}
			repo := newFakeRepo(srv, &Server{ID: "s1", Name: "web-1", OrgID: "o1"}, &Server{ID: "s2", Name: "web-1-2", OrgID: "o1"})
			svc := NewService(repo, "")
			sc := &fakeShell{
				answer: func(cmd string) string { return c.host },
				fails:  func(string) bool { return c.fails },
			}
			var lines []string
			svc.nameFromHost(ctx, srv, sc, func(l string) { lines = append(lines, l) })
			assert.Equal(t, []string{"hostname"}, sc.cmds)
			got, _ := repo.GetByID(ctx, "s3")
			assert.Equal(t, c.want, got.Name)
			assert.Equal(t, c.want, srv.Name)
			if c.line == "" {
				assert.Empty(t, lines)
			} else {
				assert.Equal(t, []string{c.line}, lines)
			}
		})
	}
}

// One provisioning run per server at a time: a second, from a page or a
// first join, is refused instead of fighting the first over apt's lock.
func TestProvisionOneAtATime(t *testing.T) {
	svc := NewService(newFakeRepo(&Server{ID: "s1", Name: "web-1", IPAddress: "192.0.2.1"}), "")
	svc.provisioning.Store("s1", true)
	err := svc.Provision(context.Background(), "s1", "u1", func(string) {})
	require.ErrorIs(t, err, ErrProvisionRunning)
	_, err = svc.repo.GetProvisionRun(context.Background(), "s1")
	assert.ErrorIs(t, err, ErrNoProvisionRun, "the running one's record is left alone")
}

// A server is waiting until an agent joined it or it was provisioned (the
// frontend's waitingForAgent is the same rule). Nothing is placed on a
// waiting one: no share, and no room to report.
func TestWaitingServerIsNotReady(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	waiting := &Server{ID: "w", OrgID: "org-a", Name: "server-1", Connection: ConnectionAgent}
	cases := map[string]struct {
		srv     *Server
		waiting bool
	}{
		"made by Add a server": {waiting, true},
		"an agent joined":      {&Server{ID: "j", OrgID: "org-a", Connection: ConnectionAgent, AgentKey: "ssh-ed25519 AAAA"}, false},
		"provisioned":          {&Server{ID: "p", OrgID: "org-a", Connection: ConnectionAgent, ProvisionedAt: &now}, false},
		"over SSH":             {&Server{ID: "h", OrgID: "org-a", Connection: ConnectionSSH}, false},
		"a row with no mode":   {&Server{ID: "o", OrgID: "org-a"}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, c.waiting, c.srv.WaitingForAgent())
			s := NewService(newFakeRepo(c.srv), "")
			s.WithAppNetworks(true, "10.203.0.0/16")
			s.WithOrgRoles(bothOwner)
			err := s.CheckReady(ctx, c.srv.ID)
			_, fitErr := s.Fit(ctx, c.srv.ID, "org-a", 1, 512)
			shareErr := s.ShareWith(ctx, "u-both", "org-a", c.srv.ID, "org-b", 1, 512)
			if c.waiting {
				assert.ErrorIs(t, err, ErrServerNotReady)
				assert.ErrorIs(t, fitErr, ErrServerNotReady, "no pretending it has room")
				assert.ErrorIs(t, shareErr, ErrServerNotReady)
			} else {
				assert.NoError(t, err)
				assert.NotErrorIs(t, fitErr, ErrServerNotReady)
				assert.NotErrorIs(t, shareErr, ErrServerNotReady)
			}
		})
	}
}

// GET /servers/:id/fit answers 409 server_not_ready for a waiting server.
func TestFitEndpointRefusesWaitingServer(t *testing.T) {
	repo := newFakeRepo(&Server{ID: testServerID, OrgID: "org-a", Name: "server-1", Connection: ConnectionAgent})
	r := roleRouter(NewService(repo, ""), &fakeAudit{}, "owner")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/servers/"+testServerID+"/fit", nil))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"server_not_ready"`)
}
