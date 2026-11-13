package alert

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const testKey = "test-encryption-key-32-chars-long!"

// fakeRepo holds channels in memory; services, servers and operators are
// fixed.
type fakeRepo struct {
	mu         sync.Mutex
	channels   map[string]*Channel
	deliveries map[string]Delivery
	oneOffs    []Event
	// serviceLookups counts ServicesOrgIDs calls.
	serviceLookups int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{channels: map[string]*Channel{}, deliveries: map[string]Delivery{}}
}

func (f *fakeRepo) GetChannel(_ context.Context, orgID string) (*Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.channels[orgID]; ok {
		c := *ch
		return &c, nil
	}
	return nil, ErrNoChannel
}
func (f *fakeRepo) CreateChannel(_ context.Context, orgID, u, secret string, enabled bool) (*Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[orgID] = &Channel{OrgID: orgID, WebhookURL: u, SigningSecret: secret, Enabled: enabled}
	return f.channels[orgID], nil
}
func (f *fakeRepo) UpdateChannel(_ context.Context, orgID, u string, enabled bool) (*Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.channels[orgID]
	if !ok {
		return nil, ErrNoChannel
	}
	ch.WebhookURL, ch.Enabled = u, enabled
	return ch, nil
}
func (f *fakeRepo) RotateSecret(_ context.Context, orgID, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[orgID].SigningSecret = secret
	return nil
}
func (f *fakeRepo) DeleteChannel(_ context.Context, orgID string) error {
	delete(f.channels, orgID)
	return nil
}
func (f *fakeRepo) ServiceContext(_ context.Context, id string) (*ServiceContext, error) {
	if id != "svc-1" {
		return nil, errors.New("no such service")
	}
	return &ServiceContext{Name: "api", Project: "Initech", Environment: "production", Server: "web-1", OrgID: "org-1"}, nil
}
func (f *fakeRepo) ServicesOrgIDs(_ context.Context, ids []string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serviceLookups++
	if slices.Contains(ids, "svc-1") {
		return []string{"org-1"}, nil
	}
	return []string{}, nil
}
func (f *fakeRepo) ServerContext(context.Context, string) (string, string, error) {
	return "web-1", "org-1", nil
}
func (f *fakeRepo) MirrorContext(context.Context, string) (string, string, error) {
	return "the push mirror of acme/shop to https://github.com/acme/shop", "o1", nil
}
func (f *fakeRepo) VolumeContext(context.Context, string) (string, string, error) {
	return "northwind-images", "org-1", nil
}
func (f *fakeRepo) OperatorOrgIDs(context.Context) ([]string, error) {
	return []string{"org-1", "org-2"}, nil
}
func (f *fakeRepo) RecordDelivery(_ context.Context, orgID string, d Delivery) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries[orgID] = d
	return nil
}
func (f *fakeRepo) RecordOneOff(_ context.Context, e Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oneOffs = append(f.oneOffs, e)
	return nil
}
func (f *fakeRepo) ListRecent(context.Context, string) ([]RecentAlert, error) { return nil, nil }

// receiver records what reaches it and answers with codes, in order.
type receiver struct {
	mu     sync.Mutex
	bodies [][]byte
	sigs   []string
	codes  []int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bodies = append(r.bodies, b)
	r.sigs = append(r.sigs, req.Header.Get(signatureHead))
	code := http.StatusOK
	if len(r.codes) > 0 {
		code, r.codes = r.codes[0], r.codes[1:]
	}
	w.WriteHeader(code)
}

// newTestService returns a service whose client trusts the TLS test server
// (on 127.0.0.1, which the real client would refuse) and doesn't wait
// between attempts.
func newTestService(t *testing.T, repo Repository, recv *receiver) (*Service, string) {
	srv := httptest.NewTLSServer(recv)
	t.Cleanup(srv.Close)
	s := NewService(repo, crypto.NewKeys(testKey, ""), zap.NewNop())
	s.client = srv.Client()
	s.retryWait = []time.Duration{0, 0}
	return s, srv.URL
}

func TestDeliverySignsTheBodyAndNamesTheService(t *testing.T) {
	repo, recv := newFakeRepo(), &receiver{}
	s, url := newTestService(t, repo, recv)
	created, err := s.PutChannel(context.Background(), "org-1", "https://hooks.example.com/x", true)
	require.NoError(t, err)
	require.NotEmpty(t, created.SigningSecret, "creating a channel returns its secret once")
	repo.channels["org-1"].WebhookURL = url

	s.deliver(context.Background(), Event{Kind: KindServiceRestarting, Subject: "svc-1", Detail: "restarting in a loop: api-blue"})

	require.Len(t, recv.bodies, 1)
	mac := hmac.New(sha256.New, []byte(created.SigningSecret))
	mac.Write(recv.bodies[0])
	assert.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), recv.sigs[0])
	var p payload
	require.NoError(t, json.Unmarshal(recv.bodies[0], &p))
	assert.Equal(t, "[firing] Restarting in a loop: api (Initech · production, on web-1) — restarting in a loop: api-blue", p.Text)
	assert.Equal(t, "firing", p.Alert.Status)

	again, err := s.PutChannel(context.Background(), "org-1", "https://hooks.example.com/y", true)
	require.NoError(t, err)
	assert.Empty(t, again.SigningSecret, "updating doesn't return the secret")
}

func TestDeliveryRetriesServerErrorsButNotClientErrors(t *testing.T) {
	repo := newFakeRepo()
	enc, _ := crypto.NewKeys(testKey, "").Seal(signingSecretAD("org-1"), "secret")

	recv := &receiver{codes: []int{503, 502}}
	s, url := newTestService(t, repo, recv)
	repo.channels["org-1"] = &Channel{OrgID: "org-1", WebhookURL: url, SigningSecret: enc, Enabled: true}
	s.deliver(context.Background(), Event{Kind: KindDeployFailed, Subject: "svc-1", Detail: "boom"})
	assert.Len(t, recv.bodies, 3, "two 5xx answers, then delivered")

	recv2 := &receiver{codes: []int{404}}
	s2, url2 := newTestService(t, repo, recv2)
	repo.channels["org-1"].WebhookURL = url2
	assert.Error(t, s2.SendTest(context.Background(), "org-1"))
	assert.Len(t, recv2.bodies, 1, "a 404 isn't retried")
}

func TestAMemoryKillGoesToTheServicesWorkspace(t *testing.T) {
	repo := newFakeRepo()
	enc, _ := crypto.NewKeys(testKey, "").Seal(signingSecretAD("org-1"), "secret")
	recv := &receiver{}
	s, url := newTestService(t, repo, recv)
	repo.channels["org-1"] = &Channel{OrgID: "org-1", WebhookURL: url, SigningSecret: enc, Enabled: true}

	s.OOMKilled("svc-1", "stack-web-1 was killed for memory (compose service web)")
	s.deliver(context.Background(), <-s.queue)

	require.Len(t, recv.bodies, 1, "to org-1, the service's workspace")
	var p payload
	require.NoError(t, json.Unmarshal(recv.bodies[0], &p))
	assert.Equal(t, "[killed] Killed for memory: api (Initech · production, on web-1) — stack-web-1 was killed for memory (compose service web)", p.Text)
	assert.Equal(t, KindOOMKilled, p.Alert.Kind)
	assert.Equal(t, "api (Initech · production, on web-1)", p.Alert.Name)
	assert.Equal(t, "firing", p.Alert.Status)
	assert.NotContains(t, serviceKinds, KindOOMKilled, "a one-off, never opened or resolved")
}

func TestDisabledOrMissingChannelsGetNothingAndControlPlaneGoesToOperators(t *testing.T) {
	repo := newFakeRepo()
	enc, _ := crypto.NewKeys(testKey, "").Seal(signingSecretAD("org-1"), "secret")
	recv := &receiver{}
	s, url := newTestService(t, repo, recv)
	repo.channels["org-1"] = &Channel{OrgID: "org-1", WebhookURL: url, SigningSecret: enc, Enabled: false}

	s.deliver(context.Background(), Event{Kind: KindDiskFull, Subject: ControlPlane, Detail: "/ is 91% full"})
	assert.Empty(t, recv.bodies, "org-1 is off and org-2 has no channel")

	repo.channels["org-1"].Enabled = true
	s.deliver(context.Background(), Event{Kind: KindDiskFull, Subject: ControlPlane, Resolved: true, Detail: "/ is 70% full"})
	require.Len(t, recv.bodies, 1)
	var p payload
	require.NoError(t, json.Unmarshal(recv.bodies[0], &p))
	assert.Equal(t, "[resolved] Disk almost full: the control-plane server — / is 70% full", p.Text)
}

func TestTheRealClientRefusesPrivateAddresses(t *testing.T) {
	recv := &receiver{}
	srv := httptest.NewTLSServer(recv) // on 127.0.0.1
	defer srv.Close()
	repo := newFakeRepo()
	enc, _ := crypto.NewKeys(testKey, "").Seal(signingSecretAD("org-1"), "secret")
	repo.channels["org-1"] = &Channel{OrgID: "org-1", WebhookURL: srv.URL, SigningSecret: enc, Enabled: true}
	s := NewService(repo, crypto.NewKeys(testKey, ""), zap.NewNop()) // the real client

	err := s.SendTest(context.Background(), "org-1")
	assert.ErrorIs(t, err, ErrPrivateAddress)
	assert.Empty(t, recv.bodies, "nothing reached the loopback receiver")
}

func TestValidateWebhookURL(t *testing.T) {
	for u, want := range map[string]error{
		"https://hooks.slack.com/services/T/B/X": nil,
		"https://203.0.113.10/hook":              nil, // documentation range: public as far as netip knows
		"http://hooks.slack.com/x":               ErrInvalidURL,
		"hooks.slack.com/x":                      ErrInvalidURL,
		"https://":                               ErrInvalidURL,
		"https://127.0.0.1/x":                    ErrPrivateAddress,
		"https://10.0.0.5/x":                     ErrPrivateAddress,
		"https://169.254.169.254/latest":         ErrPrivateAddress,
		"https://[::1]/x":                        ErrPrivateAddress,
		"https://100.100.1.1/x":                  ErrPrivateAddress,
	} {
		assert.ErrorIs(t, ValidateWebhookURL(u), want, u)
		if want == nil {
			assert.NoError(t, ValidateWebhookURL(u), u)
		}
	}
}

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"1.1.1.1": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.20.0.5": false, "192.168.1.1": false,
		"169.254.169.254": false, "fe80::1": false, "fd00::1": false, "::ffff:10.0.0.1": false,
		"100.64.0.1": false, "0.0.0.0": false, "64:ff9b::a00:1": false,
	} {
		assert.Equal(t, want, isPublic(netip.MustParseAddr(addr)), addr)
	}
}

func TestDeliveryIsRecorded(t *testing.T) {
	repo, recv := newFakeRepo(), &receiver{codes: []int{http.StatusOK, http.StatusGone}}
	s, url := newTestService(t, repo, recv)
	_, err := s.PutChannel(context.Background(), "org-1", "https://hooks.example.com/x", true)
	require.NoError(t, err)
	repo.channels["org-1"].WebhookURL = url

	s.deliver(context.Background(), Event{Kind: KindServiceDown, Subject: "svc-1", Detail: "no container"})
	d := repo.deliveries["org-1"]
	require.NotNil(t, d.At)
	require.NotNil(t, d.Status)
	assert.Equal(t, http.StatusOK, *d.Status)
	assert.Empty(t, d.Error)

	// 410 is permanent: one attempt, recorded with the receiver's answer.
	require.Error(t, s.SendTest(context.Background(), "org-1"))
	d = repo.deliveries["org-1"]
	require.NotNil(t, d.Status)
	assert.Equal(t, http.StatusGone, *d.Status)
	assert.Equal(t, "the receiver answered 410", d.Error)
}

func TestOneOffEventsAreStoredWithoutAWebhook(t *testing.T) {
	repo := newFakeRepo()
	s, _ := newTestService(t, repo, &receiver{})
	s.deliver(context.Background(), Event{Kind: KindDeployFailed, Subject: "svc-1", Detail: strings.Repeat("é", 400)})
	s.deliver(context.Background(), Event{Kind: KindServiceDown, Subject: "svc-1"})
	require.Len(t, repo.oneOffs, 1, "only the failed deploy is a one-off")
	assert.Equal(t, KindDeployFailed, repo.oneOffs[0].Kind)
	assert.False(t, repo.oneOffs[0].At.IsZero())
	assert.LessOrEqual(t, len(repo.oneOffs[0].Detail), maxDetailLen)
	assert.True(t, utf8.ValidString(repo.oneOffs[0].Detail), "cut on a rune boundary")
}
