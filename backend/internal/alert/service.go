package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/pkg/broker"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var (
	// ErrNoChannel is a workspace without a webhook.
	ErrNoChannel = errors.New("no alert webhook is set up for this workspace")
	// ErrInvalidURL is a webhook URL that is not an absolute https:// URL.
	ErrInvalidURL = errors.New("the webhook URL must be an https:// URL")
	// ErrPrivateAddress is a webhook on a loopback, private, link-local or
	// other non-public address, which would let the panel reach its own
	// network.
	// ErrPrivateAddress is netcheck's, so what the dialer refuses at connect
	// time (ValidateWebhookURL only sees literal addresses) is the same
	// error, and the sender stops retrying it.
	ErrPrivateAddress = netcheck.ErrPrivateAddress
)

// Channel is a workspace's webhook. SigningSecret is encrypted.
type Channel struct {
	OrgID         string
	WebhookURL    string
	SigningSecret string
	Enabled       bool
	UpdatedAt     time.Time
	Delivery      Delivery
}

// Delivery is the last message posted to a channel and what the receiver
// answered: its HTTP status (nil when it gave none) and the error.
type Delivery struct {
	At     *time.Time `json:"last_delivery_at"`
	Status *int       `json:"last_delivery_status"`
	Error  string     `json:"last_delivery_error"`
}

// ChannelDTO is the API's view of a channel. SigningSecret is set only in
// the response that creates or rotates it.
type ChannelDTO struct {
	WebhookURL    string    `json:"webhook_url"`
	Enabled       bool      `json:"enabled"`
	UpdatedAt     time.Time `json:"updated_at"`
	SigningSecret string    `json:"signing_secret,omitempty"`
	Delivery
}

func channelDTO(ch *Channel) *ChannelDTO {
	return &ChannelDTO{WebhookURL: ch.WebhookURL, Enabled: ch.Enabled, UpdatedAt: ch.UpdatedAt, Delivery: ch.Delivery}
}

// RecentAlert is one row of Settings → Alerts: an alert open now or fired in
// the last 30 days, and what it is about.
type RecentAlert struct {
	ID         string     `json:"id"`
	Kind       Kind       `json:"kind"`
	Subject    string     `json:"subject"` // service, server or volume ID, or ControlPlane
	Detail     string     `json:"detail"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	// OneOff: a failed deploy or a memory kill, one message with nothing to
	// resolve (its row is resolved as it fires).
	OneOff          bool   `json:"one_off"`
	SubjectName     string `json:"subject_name"`
	ProjectID       string `json:"project_id,omitempty"`
	ProjectName     string `json:"project_name"`
	EnvironmentName string `json:"environment_name"`
	ServerName      string `json:"server_name"`
}

// oneOff kinds are a single message each.
func oneOff(k Kind) bool { return k == KindDeployFailed || k == KindOOMKilled || k == KindPanelUpgrade }

// ServiceContext names a service in a message and says whose it is.
type ServiceContext struct {
	Name, Project, Environment, Server, OrgID string
}

// Event is one message: an alert firing or resolving, or a failed deploy.
type Event struct {
	Kind     Kind
	Subject  string // service ID, server ID or ControlPlane
	Resolved bool
	Detail   string
	At       time.Time
}

// Repository is what the service stores and looks up.
type Repository interface {
	GetChannel(ctx context.Context, orgID string) (*Channel, error)
	CreateChannel(ctx context.Context, orgID, webhookURL, encryptedSecret string, enabled bool) (*Channel, error)
	UpdateChannel(ctx context.Context, orgID, webhookURL string, enabled bool) (*Channel, error)
	RotateSecret(ctx context.Context, orgID, encryptedSecret string) error
	DeleteChannel(ctx context.Context, orgID string) error
	ServiceContext(ctx context.Context, serviceID string) (*ServiceContext, error)
	// ServicesOrgIDs returns the workspaces the services belong to; one
	// deleted since is left out.
	ServicesOrgIDs(ctx context.Context, serviceIDs []string) ([]string, error)
	ServerContext(ctx context.Context, serverID string) (name, orgID string, err error)
	VolumeContext(ctx context.Context, volumeID string) (name, orgID string, err error)
	// MirrorContext names a repository's mirror ("the push mirror of
	// founders/shop to github.com/…") and its workspace (D-79).
	MirrorContext(ctx context.Context, mirrorID string) (name, orgID string, err error)
	OperatorOrgIDs(ctx context.Context) ([]string, error)
	// RecordDelivery stores what the receiver answered the last message.
	RecordDelivery(ctx context.Context, orgID string, d Delivery) error
	// RecordOneOff stores a failed deploy or a memory kill, so the list of
	// recent alerts has it.
	RecordOneOff(ctx context.Context, e Event) error
	ListRecent(ctx context.Context, orgID string) ([]RecentAlert, error)
}

const (
	queueSize     = 256
	maxDetailLen  = 500
	signatureHead = "X-Toft-Signature"
)

// Service manages workspaces' webhooks and delivers events to them. Notify
// never blocks: events go through a bounded queue drained by RunSender, so
// a slow receiver never holds up a deploy or the monitor.
type Service struct {
	repo      Repository
	keys      *crypto.Keys
	queue     chan Event
	client    *http.Client
	retryWait []time.Duration // before the 2nd and 3rd attempt
	log       *zap.Logger
	pub       Publisher
}

// Publisher tells a workspace's open tabs. Satisfied by *broker.Broker.
type Publisher interface {
	Publish(orgID string, e broker.Event)
}

// WithPublisher lets Announce tell workspaces' tabs.
func (s *Service) WithPublisher(p Publisher) *Service {
	s.pub = p
	return s
}

// Announce tells the tabs of each workspace the changed alerts (opened or
// resolved) concern, once each: the inbox's Needs you reads alerts, and a
// service's alert is its health dot. An alert whose workspace can't be
// found (its service was deleted) is skipped.
func (s *Service) Announce(ctx context.Context, changed []Event) {
	if s.pub == nil {
		return
	}
	services := map[string]bool{} // workspace → a service's alert among them
	// A service's alerts are looked up together: an outage can change one
	// for every service on a server in one pass.
	var serviceIDs []string
	for _, e := range changed {
		if slices.Contains(serviceKinds, e.Kind) {
			if !slices.Contains(serviceIDs, e.Subject) {
				serviceIDs = append(serviceIDs, e.Subject)
			}
			continue
		}
		_, orgIDs, err := s.whose(ctx, e)
		if err != nil {
			s.log.Debug("alert change not announced: cannot tell whose it is", zap.String("kind", string(e.Kind)), zap.String("subject", e.Subject), zap.Error(err))
			continue
		}
		for _, org := range orgIDs {
			if _, ok := services[org]; !ok {
				services[org] = false
			}
		}
	}
	if len(serviceIDs) > 0 {
		orgIDs, err := s.repo.ServicesOrgIDs(ctx, serviceIDs)
		if err != nil {
			s.log.Warn("service alert changes not announced: cannot tell whose they are", zap.Int("services", len(serviceIDs)), zap.Error(err))
		}
		for _, org := range orgIDs {
			services[org] = true
		}
	}
	for org, withServices := range services {
		kinds := []string{"alerts"}
		if withServices {
			kinds = append(kinds, "services")
		}
		s.pub.Publish(org, broker.Changed("", kinds...))
	}
}

func NewService(repo Repository, keys *crypto.Keys, log *zap.Logger) *Service {
	return &Service{
		repo:      repo,
		keys:      keys,
		queue:     make(chan Event, queueSize),
		client:    newWebhookClient(),
		retryWait: []time.Duration{2 * time.Second, 10 * time.Second},
		log:       log,
	}
}

// Notify queues an event for delivery. A full queue drops it with a warning.
func (s *Service) Notify(e Event) {
	select {
	case s.queue <- e:
	default:
		s.log.Warn("alert queue full; message dropped", zap.String("kind", string(e.Kind)), zap.String("subject", e.Subject))
	}
}

// DeployFailed queues a message for a failed deploy. Satisfies
// deployment.AlertNotifier.
func (s *Service) DeployFailed(serviceID, deploymentID, errMsg string) {
	s.Notify(Event{Kind: KindDeployFailed, Subject: serviceID, Detail: fmt.Sprintf("%s (deployment %s)", errMsg, deploymentID), At: time.Now()})
}

// OOMKilled queues a message for a container of a service killed for
// memory. Satisfies metrics.Alerter.
func (s *Service) OOMKilled(serviceID, detail string) {
	s.Notify(Event{Kind: KindOOMKilled, Subject: serviceID, Detail: detail, At: time.Now()})
}

// PanelUpgradeFailed queues a message for an upgrade of the panel that
// rolled back or stopped (D-68). Satisfies instance.UpgradeAlerter.
func (s *Service) PanelUpgradeFailed(detail string) {
	s.Notify(Event{Kind: KindPanelUpgrade, Subject: ControlPlane, Detail: detail, At: time.Now()})
}

// RunSender delivers queued events until ctx ends.
func (s *Service) RunSender(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.queue:
			s.deliver(ctx, e)
		}
	}
}

// deliver posts e to the webhook of every workspace it concerns, and
// records what each receiver answered. A one-off event is stored first, so
// Settings → Alerts lists it whether or not a webhook is set up.
func (s *Service) deliver(ctx context.Context, e Event) {
	if oneOff(e.Kind) {
		stored := e
		stored.Detail = truncate(e.Detail, maxDetailLen)
		if stored.At.IsZero() {
			stored.At = time.Now()
		}
		if err := s.repo.RecordOneOff(ctx, stored); err != nil {
			s.log.Warn("alert not stored", zap.String("kind", string(e.Kind)), zap.String("subject", e.Subject), zap.Error(err))
		}
	}
	body, orgIDs, err := s.describe(ctx, e)
	if err != nil {
		s.log.Warn("alert not delivered: cannot tell whose it is", zap.String("kind", string(e.Kind)), zap.String("subject", e.Subject), zap.Error(err))
		return
	}
	for _, orgID := range orgIDs {
		ch, err := s.repo.GetChannel(ctx, orgID)
		if errors.Is(err, ErrNoChannel) || (err == nil && !ch.Enabled) {
			continue
		}
		if err == nil {
			err = s.postAndRecord(ctx, orgID, ch, body)
		}
		if err != nil && ctx.Err() == nil {
			s.log.Warn("alert not delivered", zap.String("org_id", orgID), zap.String("kind", string(e.Kind)), zap.Error(err))
		}
	}
}

// postAndRecord posts body and stores the receiver's answer on the channel.
func (s *Service) postAndRecord(ctx context.Context, orgID string, ch *Channel, body []byte) error {
	status, err := s.post(ctx, ch, body)
	now := time.Now()
	d := Delivery{At: &now}
	if status != 0 {
		d.Status = &status
	}
	if err != nil {
		d.Error = truncate(err.Error(), 500)
	}
	if rerr := s.repo.RecordDelivery(context.WithoutCancel(ctx), orgID, d); rerr != nil {
		s.log.Warn("alert delivery not recorded", zap.String("org_id", orgID), zap.Error(rerr))
	}
	return err
}

// truncate cuts s to at most n bytes, never inside a UTF-8 sequence, and
// marks the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// titles head each message.
var titles = map[Kind]string{
	KindDeployFailed:      "Deploy failed",
	KindOOMKilled:         "Killed for memory",
	KindServiceDown:       "Service down",
	KindServiceRestarting: "Restarting in a loop",
	KindServiceUnhealthy:  "Health check failing",
	KindContainerExited:   "Container exited with an error",
	KindServerUnreachable: "Server unreachable",
	KindDiskFull:          "Disk almost full",
	KindVolumeFull:        "Shared volume filling up",
	KindBackupFailed:      "Backup failing",
	KindPanelContainer:    "Panel container not healthy",
	KindPanelCertificate:  "Panel certificate not renewing",
	KindPanelUpgrade:      "Panel upgrade failed",
	KindMirrorFailing:     "Mirror failing",
	KindAgentOffline:      "Server agent offline",
	KindAgentFrozen:       "Server kept its own state",
	KindSecurityUpdates:   "Security updates waiting",
	KindRebootRequired:    "Reboot required",
	KindDockerAdvisory:    "Docker affected by an advisory",
}

type payload struct {
	Text  string       `json:"text"` // Slack, Mattermost, Rocket.Chat, Google Chat
	Alert payloadAlert `json:"alert"`
}

type payloadAlert struct {
	Kind    Kind      `json:"kind"`
	Status  string    `json:"status"` // firing or resolved
	Subject string    `json:"subject"`
	Name    string    `json:"name"`
	Detail  string    `json:"detail"`
	At      time.Time `json:"at"`
}

// describe builds the message for e and finds the workspaces it goes to: a
// service's or a server's own, or for the control-plane server every
// workspace an instance operator owns.
func (s *Service) describe(ctx context.Context, e Event) ([]byte, []string, error) {
	name, orgIDs, err := s.whose(ctx, e)
	if err != nil {
		return nil, nil, err
	}

	status := "firing"
	if e.Resolved {
		status = "resolved"
	}
	label := status
	switch e.Kind { // one-off events: nothing resolves them
	case KindDeployFailed:
		label = "failed"
	case KindOOMKilled:
		label = "killed"
	case KindPanelUpgrade:
		label = "failed"
	}
	detail := truncate(e.Detail, maxDetailLen)
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	body, err := json.Marshal(payload{
		Text:  fmt.Sprintf("[%s] %s: %s — %s", label, titles[e.Kind], name, detail),
		Alert: payloadAlert{Kind: e.Kind, Status: status, Subject: e.Subject, Name: name, Detail: detail, At: at.UTC()},
	})
	return body, orgIDs, err
}

// whose names what e is about and finds the workspaces it concerns.
func (s *Service) whose(ctx context.Context, e Event) (name string, orgIDs []string, err error) {
	switch {
	case e.Subject == ControlPlane:
		ids, err := s.repo.OperatorOrgIDs(ctx)
		if err != nil {
			return "", nil, err
		}
		name, orgIDs = "the control-plane server", ids
		if e.Kind == KindPanelContainer || e.Kind == KindPanelCertificate || e.Kind == KindPanelUpgrade {
			name = "the panel"
		}
	case e.Kind == KindMirrorFailing:
		mirror, orgID, err := s.repo.MirrorContext(ctx, e.Subject)
		if err != nil {
			return "", nil, err
		}
		name, orgIDs = mirror, []string{orgID}
	case e.Kind == KindVolumeFull:
		vol, orgID, err := s.repo.VolumeContext(ctx, e.Subject)
		if err != nil {
			return "", nil, err
		}
		name, orgIDs = "shared volume "+vol, []string{orgID}
	case e.Kind == KindServerUnreachable || e.Kind == KindDiskFull || e.Kind == KindBackupFailed || e.Kind == KindAgentOffline || e.Kind == KindAgentFrozen,
		e.Kind == KindSecurityUpdates || e.Kind == KindRebootRequired || e.Kind == KindDockerAdvisory:
		srv, orgID, err := s.repo.ServerContext(ctx, e.Subject)
		if err != nil {
			return "", nil, err
		}
		name, orgIDs = "server "+srv, []string{orgID}
	default:
		sc, err := s.repo.ServiceContext(ctx, e.Subject)
		if err != nil {
			return "", nil, err
		}
		name, orgIDs = describeService(sc), []string{sc.OrgID}
	}
	return name, orgIDs, nil
}

// describeService names a service: "api (Initech · production, on web-1)".
func describeService(sc *ServiceContext) string {
	var where []string
	if sc.Project != "" {
		where = append(where, sc.Project)
	}
	if sc.Environment != "" {
		where = append(where, sc.Environment)
	}
	place := strings.Join(where, " · ")
	if sc.Server != "" {
		if place != "" {
			place += ", "
		}
		place += "on " + sc.Server
	}
	if place == "" {
		return sc.Name
	}
	return sc.Name + " (" + place + ")"
}

// permanentError is a receiver's 3xx or 4xx answer (except 429).
type permanentError struct{ status int }

func (e permanentError) Error() string { return fmt.Sprintf("the receiver answered %d", e.status) }

// post sends body to the channel's webhook, signed, with up to three
// attempts. It returns the last answer's HTTP status, 0 when there was none.
func (s *Service) post(ctx context.Context, ch *Channel, body []byte) (int, error) {
	secret, err := s.keys.Open(signingSecretAD(ch.OrgID), ch.SigningSecret)
	if err != nil {
		return 0, fmt.Errorf("decrypt signing secret: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	var (
		last   error
		status int
	)
	for attempt := 0; attempt <= len(s.retryWait); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return status, ctx.Err()
			case <-time.After(s.retryWait[attempt-1]):
			}
		}
		if status, last = s.postOnce(ctx, ch.WebhookURL, signature, body); last == nil {
			return status, nil
		}
		var perm permanentError
		if errors.As(last, &perm) || errors.Is(last, ErrPrivateAddress) {
			return status, last
		}
	}
	return status, last
}

// postOnce makes one attempt; the status is 0 when the receiver gave none.
func (s *Service) postOnce(ctx context.Context, webhookURL, signature string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Toft-Alerts/1")
	req.Header.Set(signatureHead, signature)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, nil
	case resp.StatusCode >= 300 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests:
		// A redirect (never followed) or a client error: retrying won't help.
		return resp.StatusCode, permanentError{resp.StatusCode}
	default:
		return resp.StatusCode, fmt.Errorf("the receiver answered %d", resp.StatusCode)
	}
}

// ─── Channel management ───────────────────────────────────────────────────────

// GetChannel returns the workspace's webhook (ErrNoChannel).
func (s *Service) GetChannel(ctx context.Context, orgID string) (*ChannelDTO, error) {
	ch, err := s.repo.GetChannel(ctx, orgID)
	if err != nil {
		return nil, err
	}
	return channelDTO(ch), nil
}

// ListRecent returns the workspace's alerts open now or fired in the last
// 30 days, newest first.
func (s *Service) ListRecent(ctx context.Context, orgID string) ([]RecentAlert, error) {
	return s.repo.ListRecent(ctx, orgID)
}

// PutChannel sets the workspace's webhook. Creating one generates its
// signing secret, returned this once (ErrInvalidURL, ErrPrivateAddress).
func (s *Service) PutChannel(ctx context.Context, orgID, webhookURL string, enabled bool) (*ChannelDTO, error) {
	if err := ValidateWebhookURL(webhookURL); err != nil {
		return nil, err
	}
	ch, err := s.repo.UpdateChannel(ctx, orgID, webhookURL, enabled)
	if err == nil {
		return channelDTO(ch), nil
	}
	if !errors.Is(err, ErrNoChannel) {
		return nil, err
	}
	secret, encrypted, err := s.newSecret(orgID)
	if err != nil {
		return nil, err
	}
	ch, err = s.repo.CreateChannel(ctx, orgID, webhookURL, encrypted, enabled)
	if err != nil {
		return nil, err
	}
	dto := channelDTO(ch)
	dto.SigningSecret = secret
	return dto, nil
}

// RotateSecret replaces the signing secret and returns the new one, once.
func (s *Service) RotateSecret(ctx context.Context, orgID string) (*ChannelDTO, error) {
	secret, encrypted, err := s.newSecret(orgID)
	if err != nil {
		return nil, err
	}
	if err := s.repo.RotateSecret(ctx, orgID, encrypted); err != nil {
		return nil, err
	}
	dto, err := s.GetChannel(ctx, orgID)
	if err != nil {
		return nil, err
	}
	dto.SigningSecret = secret
	return dto, nil
}

func (s *Service) DeleteChannel(ctx context.Context, orgID string) error {
	return s.repo.DeleteChannel(ctx, orgID)
}

// SendTest posts a test message to the workspace's webhook now, whether it
// is on or not, and returns why it failed.
func (s *Service) SendTest(ctx context.Context, orgID string) error {
	ch, err := s.repo.GetChannel(ctx, orgID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload{
		Text:  "[test] Alerts from Toft reach this channel.",
		Alert: payloadAlert{Kind: "test", Status: "test", Name: "test", Detail: "a test message", At: time.Now().UTC()},
	})
	if err != nil {
		return err
	}
	return s.postAndRecord(ctx, orgID, ch, body)
}

// signingSecretAD is where a workspace's alert signing secret is sealed for
// (D-126).
func signingSecretAD(orgID string) string {
	if id, err := uuid.Parse(orgID); err == nil {
		orgID = id.String()
	}
	return crypto.AD("alert_channels.signing_secret", orgID)
}

func (s *Service) newSecret(orgID string) (plain, encrypted string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate secret: %w", err)
	}
	plain = hex.EncodeToString(b)
	encrypted, err = s.keys.Seal(signingSecretAD(orgID), plain)
	if err != nil {
		return "", "", fmt.Errorf("encrypt secret: %w", err)
	}
	return plain, encrypted, nil
}

// ─── Destination checks ───────────────────────────────────────────────────────

// ValidateWebhookURL accepts an absolute https:// URL whose host, when it is
// an IP address, is public. Hostnames are checked when the panel connects.
func ValidateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || len(raw) > 2048 {
		return ErrInvalidURL
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !isPublic(ip) {
		return fmt.Errorf("the webhook must be on a public address: %w", ErrPrivateAddress)
	}
	return nil
}

// isPublic and newWebhookClient live in pkg/netcheck now, shared with the
// registry and SCM clients; the names stay for this package's tests.
var isPublic = netcheck.IsPublic

func newWebhookClient() *http.Client { return netcheck.NewPublicOnlyClient(10*time.Second, false) }
