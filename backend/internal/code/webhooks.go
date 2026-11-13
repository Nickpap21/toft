package code

import (
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
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nickpap21/toft/backend/internal/gitstore"
	"github.com/Nickpap21/toft/backend/pkg/crypto"
	"github.com/Nickpap21/toft/backend/pkg/netcheck"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Outbound webhooks (D-79): a repository's events posted to URLs its owners
// set, signed as alerts are. A delivery is written with its git event
// (InsertEvent), so none is lost; one worker sends them, builds each
// payload at the first attempt and keeps it, so a retry or a redelivery
// sends the same bytes.

const (
	maxWebhooks    = 20
	hookWorkers    = 4 // deliveries at once, so one slow receiver doesn't hold up the rest
	hookTick       = 15 * time.Second
	hookAttempts   = 5
	keepDeliveries = 30 * 24 * time.Hour
	responseKeep   = 2 << 10
	maxPushCommits = 20
)

// hookRetries is the wait before the 2nd to the 5th attempt.
var hookRetries = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

var (
	ErrBadHookURL      = errors.New("the webhook must be an https:// URL with no user in it or fragment")
	ErrBadEvents       = errors.New("events is push, pull_request or both")
	ErrWebhookLimit    = errors.New("a repository has 20 webhooks at most")
	ErrWebhookPaused   = errors.New("the webhook is paused: turn it on first")
	ErrDeliveryPending = errors.New("the delivery is waiting to be sent already")
)

// Webhook is a repository's webhook as the API shows it.
type Webhook struct {
	ID              string           `json:"id"`
	URL             string           `json:"url"`
	Events          []string         `json:"events"`
	Active          bool             `json:"active"`
	CreatedAt       time.Time        `json:"created_at"`
	CreatedBy       *Person          `json:"created_by"`
	SecretRotatedAt time.Time        `json:"secret_rotated_at"`
	SecretRotatedBy *Person          `json:"secret_rotated_by"`
	LastDelivery    *WebhookDelivery `json:"last_delivery"`
}

// WebhookCreated is a new webhook with its secret, shown this once.
type WebhookCreated struct {
	Webhook
	Secret string `json:"secret"`
}

// WebhookInput is POST …/webhooks' body.
type WebhookInput struct {
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Active *bool    `json:"active"`
}

// WebhookUpdate is PATCH …/webhooks/{id}'s body: the fields to change.
type WebhookUpdate struct {
	URL    *string  `json:"url"`
	Events []string `json:"events"`
	Active *bool    `json:"active"`
}

// WebhookDelivery is one event for one webhook.
type WebhookDelivery struct {
	ID             string     `json:"id"`
	Event          string     `json:"event"`
	Summary        string     `json:"summary"`
	State          string     `json:"state"`
	Attempts       int        `json:"attempts"`
	ResponseStatus *int       `json:"response_status"`
	DurationMS     *int       `json:"duration_ms"`
	Error          *string    `json:"error"`
	CreatedAt      time.Time  `json:"created_at"`
	DeliveredAt    *time.Time `json:"delivered_at"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	Actor          *Person    `json:"actor"`
}

// WebhookDeliveryDetail is a delivery with what was sent and the start of
// the answer.
type WebhookDeliveryDetail struct {
	WebhookDelivery
	RequestHeaders map[string]string `json:"request_headers"`
	Payload        json.RawMessage   `json:"payload"`
	ResponseBody   *string           `json:"response_body"`
}

// StoredWebhook is a webhook row.
type StoredWebhook struct {
	Webhook
	SecretEnc string
}

// NewWebhook is a webhook to store.
type NewWebhook struct {
	ID, RepoID, URL, SecretEnc, CreatedBy string
	Events                                []string
	Active                                bool
}

// StoredDelivery is a delivery with its payload and answer.
type StoredDelivery struct {
	WebhookDelivery
	Payload, ResponseBody *string
}

// ClaimedDelivery is a delivery the worker is to send now.
type ClaimedDelivery struct {
	ID, WebhookID, RepoID, URL, SecretEnc, Event, ActorID string
	GitEventID                                            int64
	Payload                                               string
	Attempts                                              int
	CreatedAt                                             time.Time
}

// DeliveryResult is how an attempt ended.
type DeliveryResult struct {
	State          string // delivered, failed or pending (retried at NextAttemptAt)
	NextAttemptAt  time.Time
	ResponseStatus *int
	ResponseBody   *string
	Error          *string
	DurationMS     *int
}

// StoredEvent is a git event, for a payload.
type StoredEvent struct {
	ID                                       int64
	RepoID, Kind, Action, Branch, Before     string
	After, ActorID, ActorName, PullRequestID string
	Commits                                  int
	Forced                                   bool
	CreatedAt                                time.Time
}

// HookRepo is what a payload says about the repository.
type HookRepo struct {
	ID, Name, ProjectID, OrgSlug string
}

// WithHookClient sends webhooks with c instead of netcheck's public-only
// client (tests).
func (s *Service) WithHookClient(c *http.Client) *Service {
	s.hookClient = c
	return s
}

func (s *Service) nudgeHooks() {
	select {
	case s.hookWake <- struct{}{}:
	default:
	}
}

// checkHookURL is a webhook's URL, or why it can't be one.
func checkHookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.Fragment != "" || strings.ContainsAny(raw, "#@ \t\r\n") {
		return "", ErrBadHookURL
	}
	if netcheck.CheckHost(u.Hostname()) != nil {
		return "", ErrPrivateAddress
	}
	return raw, nil
}

// checkEvents is events sorted, each once, or ErrBadEvents.
func checkEvents(events []string) ([]string, error) {
	out := make([]string, 0, 2)
	for _, e := range events {
		if e != "push" && e != "pull_request" {
			return nil, ErrBadEvents
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, ErrBadEvents
	}
	slices.Sort(out)
	return out, nil
}

func newHookSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Webhooks lists the repository's webhooks, each with its last delivery.
func (s *Service) Webhooks(ctx context.Context, projectID, repoID string) ([]Webhook, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	stored, err := s.repo.ListWebhooks(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	last, err := s.repo.LastDeliveries(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Webhook, 0, len(stored))
	for _, w := range stored {
		if d, ok := last[w.ID]; ok {
			w.LastDelivery = &d
		}
		out = append(out, w.Webhook)
	}
	return out, nil
}

// Webhook is one of the repository's webhooks.
func (s *Service) Webhook(ctx context.Context, projectID, repoID, id string) (*Webhook, error) {
	hooks, err := s.Webhooks(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	for _, w := range hooks {
		if w.ID == id {
			return &w, nil
		}
	}
	return nil, ErrNotFound
}

// CreateWebhook adds a webhook and sends it a ping; its secret is in the
// answer, this once.
func (s *Service) CreateWebhook(ctx context.Context, projectID, repoID, actorID string, in WebhookInput) (*WebhookCreated, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	u, err := checkHookURL(in.URL)
	if err != nil {
		return nil, err
	}
	events, err := checkEvents(in.Events)
	if err != nil {
		return nil, err
	}
	if n, err := s.repo.CountWebhooks(ctx, r.ID); err != nil {
		return nil, err
	} else if n >= maxWebhooks {
		return nil, ErrWebhookLimit
	}
	secret, err := newHookSecret()
	if err != nil {
		return nil, err
	}
	// The ID is made here, so the secret is sealed for it (D-126).
	id := uuid.NewString()
	enc, err := s.keys.Seal(hookSecretAD(id), secret)
	if err != nil {
		return nil, fmt.Errorf("encrypt secret: %w", err)
	}
	active := in.Active == nil || *in.Active
	id, err = s.repo.CreateWebhook(ctx, NewWebhook{ID: id, RepoID: r.ID, URL: u, Events: events, Active: active, SecretEnc: enc, CreatedBy: actorID})
	if err != nil {
		return nil, err
	}
	if active {
		if _, err := s.repo.InsertPing(ctx, id, actorID); err != nil {
			s.log.Warn("webhooks: the first ping", zap.Error(err))
		}
		s.nudgeHooks()
	}
	w, err := s.Webhook(ctx, projectID, r.ID, id)
	if err != nil {
		return nil, err
	}
	return &WebhookCreated{Webhook: *w, Secret: secret}, nil
}

// UpdateWebhook changes a webhook's URL, events or whether it's on;
// pausing it fails what waits. It returns the webhook and what changed.
func (s *Service) UpdateWebhook(ctx context.Context, projectID, repoID, id string, in WebhookUpdate) (*Webhook, map[string]any, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, nil, err
	}
	changed := map[string]any{}
	if in.URL != nil {
		u, err := checkHookURL(*in.URL)
		if err != nil {
			return nil, nil, err
		}
		in.URL, changed["url"] = &u, u
	}
	if in.Events != nil {
		events, err := checkEvents(in.Events)
		if err != nil {
			return nil, nil, err
		}
		in.Events, changed["events"] = events, events
	}
	if in.Active != nil {
		changed["active"] = *in.Active
	}
	ok, err := s.repo.UpdateWebhook(ctx, r.ID, id, in)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, ErrNotFound
	}
	w, err := s.Webhook(ctx, projectID, r.ID, id)
	return w, changed, err
}

// hookSecretAD is where a webhook's signing secret is sealed for (D-126).
func hookSecretAD(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		id = u.String()
	}
	return crypto.AD("repository_webhooks.secret_encrypted", id)
}

// RotateWebhookSecret replaces the signing secret, with no grace period.
func (s *Service) RotateWebhookSecret(ctx context.Context, projectID, repoID, id, actorID string) (string, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return "", err
	}
	secret, err := newHookSecret()
	if err != nil {
		return "", err
	}
	enc, err := s.keys.Seal(hookSecretAD(id), secret)
	if err != nil {
		return "", fmt.Errorf("encrypt secret: %w", err)
	}
	ok, err := s.repo.RotateWebhookSecret(ctx, r.ID, id, enc, actorID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotFound
	}
	return secret, nil
}

// DeleteWebhook removes a webhook and its deliveries, returning it.
func (s *Service) DeleteWebhook(ctx context.Context, projectID, repoID, id string) (*Webhook, error) {
	w, err := s.Webhook(ctx, projectID, repoID, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.repo.DeleteWebhook(ctx, repoID, id); err != nil {
		return nil, err
	}
	return w, nil
}

// PingWebhook queues a ping.
func (s *Service) PingWebhook(ctx context.Context, projectID, repoID, id, actorID string) (*WebhookDelivery, error) {
	w, err := s.Webhook(ctx, projectID, repoID, id)
	if err != nil {
		return nil, err
	}
	if !w.Active {
		return nil, ErrWebhookPaused
	}
	did, err := s.repo.InsertPing(ctx, id, actorID)
	if err != nil {
		return nil, err
	}
	s.nudgeHooks()
	return s.delivery(ctx, repoID, id, did)
}

// Deliveries is a page of a webhook's deliveries, newest first.
func (s *Service) Deliveries(ctx context.Context, projectID, repoID, id string, limit int, cursor string) ([]WebhookDelivery, string, error) {
	if _, err := s.Webhook(ctx, projectID, repoID, id); err != nil {
		return nil, "", err
	}
	var at time.Time
	var after string
	if cursor != "" {
		ns, rest, ok := strings.Cut(cursor, "_")
		n, err := strconv.ParseInt(ns, 10, 64)
		if !ok || err != nil || len(rest) != 36 {
			return nil, "", ErrBadCursor
		}
		at, after = time.Unix(0, n), rest
	}
	page, err := s.repo.ListDeliveries(ctx, id, at, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(page) > limit {
		page = page[:limit]
		last := page[len(page)-1]
		next = strconv.FormatInt(last.CreatedAt.UnixNano(), 10) + "_" + last.ID
	}
	return page, next, nil
}

func (s *Service) delivery(ctx context.Context, repoID, webhookID, id string) (*WebhookDelivery, error) {
	d, err := s.repo.GetDelivery(ctx, repoID, webhookID, id)
	if err != nil {
		return nil, err
	}
	return &d.WebhookDelivery, nil
}

// Delivery is one delivery with what was sent and the start of the answer.
func (s *Service) Delivery(ctx context.Context, projectID, repoID, webhookID, id string) (*WebhookDeliveryDetail, error) {
	r, err := s.repoOf(ctx, projectID, repoID)
	if err != nil {
		return nil, err
	}
	d, err := s.repo.GetDelivery(ctx, r.ID, webhookID, id)
	if err != nil {
		return nil, err
	}
	out := &WebhookDeliveryDetail{WebhookDelivery: d.WebhookDelivery, RequestHeaders: map[string]string{}, ResponseBody: d.ResponseBody}
	if d.Payload != nil {
		out.Payload = json.RawMessage(*d.Payload)
		out.RequestHeaders = map[string]string{
			"Content-Type":     "application/json",
			"User-Agent":       hookAgent,
			"X-Toft-Event":     d.Event,
			"X-Toft-Delivery":  d.ID,
			"X-Toft-Signature": "sha256=…",
		}
	}
	return out, nil
}

// Redeliver sends a delivery again, the same body and ID, its retries
// started over.
func (s *Service) Redeliver(ctx context.Context, projectID, repoID, webhookID, id, actorID string) (*WebhookDelivery, error) {
	w, err := s.Webhook(ctx, projectID, repoID, webhookID)
	if err != nil {
		return nil, err
	}
	if !w.Active {
		return nil, ErrWebhookPaused
	}
	if _, err := s.repo.GetDelivery(ctx, repoID, webhookID, id); err != nil {
		return nil, err
	}
	ok, err := s.repo.Redeliver(ctx, webhookID, id, actorID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDeliveryPending
	}
	s.nudgeHooks()
	return s.delivery(ctx, repoID, webhookID, id)
}

// ─── delivery ────────────────────────────────────────────────────────────────

const hookAgent = "Toft-Webhooks/1"

// RunWebhooks sends due deliveries until ctx ends, hookWorkers at once: at
// once when one is written, and every 15 s for retries; it also deletes
// deliveries older than 30 days, hourly.
func (s *Service) RunWebhooks(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	slots := make(chan struct{}, hookWorkers)
	var wg sync.WaitGroup
	defer wg.Wait()
	tick := time.NewTicker(hookTick)
	defer tick.Stop()
	var gc time.Time
	for {
		if time.Since(gc) > time.Hour {
			gc = time.Now()
			if n, err := s.repo.DeleteOldDeliveries(ctx, gc.Add(-keepDeliveries)); err != nil && ctx.Err() == nil {
				s.log.Warn("webhooks: delete old deliveries", zap.Error(err))
			} else if n > 0 {
				s.log.Info("deleted old webhook deliveries", zap.Int64("count", n))
			}
		}
		if free := hookWorkers - len(slots); free > 0 {
			ds, err := s.repo.ClaimDeliveries(ctx, free)
			if err != nil && ctx.Err() == nil {
				s.log.Error("webhooks: claim", zap.Error(err))
			}
			for _, d := range ds {
				slots <- struct{}{}
				wg.Add(1)
				go func() {
					defer wg.Done()
					s.deliver(ctx, d)
					<-slots
					s.nudgeHooks()
				}()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.hookWake:
		case <-tick.C:
		}
	}
}

// deliver makes one attempt: builds the payload the first time, signs and
// posts it, and records the answer.
func (s *Service) deliver(ctx context.Context, d ClaimedDelivery) {
	res := DeliveryResult{State: "failed"}
	defer func() {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.repo.FinishDelivery(fctx, d.ID, res); err != nil {
			s.log.Error("webhooks: record a delivery", zap.Error(err), zap.String("delivery_id", d.ID))
		}
	}()
	retry := func(msg string) {
		res.Error = &msg
		if d.Attempts < hookAttempts {
			res.State, res.NextAttemptAt = "pending", time.Now().Add(hookRetries[d.Attempts-1])
		}
	}
	if d.Payload == "" {
		body, summary, err := s.buildPayload(ctx, d)
		if err != nil {
			retry("couldn't build the payload: " + err.Error())
			return
		}
		if err := s.repo.SetDeliveryPayload(ctx, d.ID, summary, string(body)); err != nil {
			retry("couldn't keep the payload: " + err.Error())
			return
		}
		d.Payload = string(body)
	}
	secret, err := s.keys.Open(hookSecretAD(d.WebhookID), d.SecretEnc)
	if err != nil {
		msg := "the signing secret can't be read: rotate it"
		res.Error = &msg
		return
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(d.Payload))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, strings.NewReader(d.Payload))
	if err != nil {
		msg := err.Error()
		res.Error = &msg
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", hookAgent)
	req.Header.Set("X-Toft-Event", d.Event)
	req.Header.Set("X-Toft-Delivery", d.ID)
	req.Header.Set("X-Toft-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	start := time.Now()
	resp, err := s.hookClient.Do(req)
	ms := int(time.Since(start).Milliseconds())
	res.DurationMS = &ms
	if err != nil {
		if errors.Is(err, netcheck.ErrPrivateAddress) {
			msg := ErrPrivateAddress.Error()
			res.Error = &msg
			return
		}
		retry("no answer: " + lastPart(err.Error()))
		return
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, responseKeep))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	body := strings.ToValidUTF8(string(b), "")
	status := resp.StatusCode
	res.ResponseStatus, res.ResponseBody = &status, &body
	switch {
	case status >= 200 && status < 300:
		res.State = "delivered"
	case status == http.StatusTooManyRequests || status >= 500:
		retry(fmt.Sprintf("the receiver answered %d", status))
	case status >= 300 && status < 400:
		msg := fmt.Sprintf("the receiver answered %d, a redirect, which Toft doesn't follow", status)
		res.Error = &msg
	default:
		msg := fmt.Sprintf("the receiver answered %d", status)
		res.Error = &msg
	}
}

// Payloads (WebhookPushPayload, WebhookPullRequestPayload and
// WebhookPingPayload in openapi.yaml).

type hookRepository struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	ProjectID     string `json:"project_id"`
	SSHURL        string `json:"ssh_url"`
	HTTPSURL      string `json:"https_url"`
	DefaultBranch string `json:"default_branch"`
}

type hookActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type hookCommit struct {
	SHA         string    `json:"sha"`
	Subject     string    `json:"subject"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	AuthoredAt  time.Time `json:"authored_at"`
}

type hookPull struct {
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	State    string     `json:"state"`
	Base     string     `json:"base"`
	Head     string     `json:"head"`
	HeadSHA  string     `json:"head_sha"`
	MergeSHA *string    `json:"merge_sha"`
	Author   *hookActor `json:"author"`
	URL      string     `json:"url"`
}

type hookPayload struct {
	Text       string         `json:"text"`
	Event      string         `json:"event"`
	CreatedAt  time.Time      `json:"created_at"`
	Repository hookRepository `json:"repository"`
	Sender     *hookActor     `json:"sender"`
	// push
	Ref          string        `json:"ref,omitempty"`
	Before       string        `json:"before,omitempty"`
	After        string        `json:"after,omitempty"`
	Created      *bool         `json:"created,omitempty"`
	Deleted      *bool         `json:"deleted,omitempty"`
	Forced       *bool         `json:"forced,omitempty"`
	Via          string        `json:"via,omitempty"`
	Commits      *[]hookCommit `json:"commits,omitempty"`
	CommitsTotal *int          `json:"commits_total,omitempty"`
	// pull_request (and a merge's push)
	Action      string    `json:"action,omitempty"`
	PullRequest *hookPull `json:"pull_request,omitempty"`
	// ping
	WebhookID string `json:"webhook_id,omitempty"`
}

// buildPayload is a delivery's body and its summary in words.
func (s *Service) buildPayload(ctx context.Context, d ClaimedDelivery) ([]byte, string, error) {
	r, err := s.repo.RepoForHook(ctx, d.RepoID)
	if err != nil {
		return nil, "", err
	}
	full := r.OrgSlug + "/" + r.Name
	repo := hookRepository{ID: r.ID, Name: r.Name, FullName: full, ProjectID: r.ProjectID}
	// Without a panel domain there is no address to give (the panel is
	// reached on whatever its clients use), so the URLs stay empty.
	if host := s.host(); host != "" {
		repo.SSHURL = "ssh://git@" + host + ":" + strconv.Itoa(s.sshPort) + "/" + full + ".git"
		repo.HTTPSURL = "https://" + host + "/" + full + ".git"
	}
	var sum gitstore.Summary
	if err := s.git.Browse(ctx, r.ID, "summary", nil, &sum); err != nil {
		return nil, "", err
	}
	repo.DefaultBranch = sum.DefaultBranch
	p := hookPayload{Event: d.Event, CreatedAt: d.CreatedAt.UTC(), Repository: repo}
	if d.Event == "ping" {
		p.WebhookID, p.Sender = d.WebhookID, s.hookSender(ctx, d.ActorID)
		p.Text = "Toft webhook for " + full + " works"
		summary := "sent from the panel"
		if p.Sender != nil {
			summary += " by " + p.Sender.Name
		}
		b, err := json.Marshal(p)
		return b, summary, err
	}
	ev, err := s.repo.GetEvent(ctx, d.GitEventID)
	if err != nil {
		return nil, "", err
	}
	if ev.ActorID != "" {
		p.Sender = &hookActor{ID: ev.ActorID, Name: ev.ActorName}
	} else if ev.Kind == "mirror" || ev.Kind == "tag" || ev.Kind == "push" {
		p.Sender = &hookActor{Name: "mirror"}
	}
	who := "the mirror"
	if p.Sender != nil && p.Sender.ID != "" {
		who = p.Sender.Name
	}
	var pull *hookPull
	if ev.PullRequestID != "" {
		if pr, err := s.repo.PullByID(ctx, ev.PullRequestID); err == nil {
			pull = s.toHookPull(pr, r)
		}
	}
	if d.Event == "pull_request" {
		p.Action, p.PullRequest = ev.Action, pull
		if ev.Kind == "merge" {
			p.Action = "merged"
		}
		if pull == nil {
			return nil, "", errors.New("the pull request is gone")
		}
		verb := map[string]string{"opened": "opened", "pushed": "pushed to", "closed": "closed", "reopened": "reopened", "merged": "merged"}[p.Action]
		p.Text = fmt.Sprintf("%s %s !%d in %s: %s", who, verb, pull.Number, full, pull.Title)
		summary := fmt.Sprintf("%s !%d by %s", verb, pull.Number, who)
		if p.Action == "merged" {
			summary = fmt.Sprintf("merged !%d into %s", pull.Number, pull.Base)
		}
		b, err := json.Marshal(p)
		return b, summary, err
	}
	// push: a branch or a tag created, moved or deleted.
	zero := func(sha string) bool { return strings.Trim(sha, "0") == "" }
	created, deleted, forced := zero(ev.Before), zero(ev.After), ev.Forced
	p.Ref = "refs/heads/" + ev.Branch
	if ev.Kind == "tag" {
		p.Ref = "refs/tags/" + ev.Branch
	}
	p.Before, p.After, p.Created, p.Deleted, p.Forced = ev.Before, ev.After, &created, &deleted, &forced
	p.Via = "push"
	switch {
	case ev.Kind == "merge":
		p.Via, p.PullRequest = "merge", pull
	case ev.ActorID == "":
		p.Via = "mirror"
	}
	commits, total := []hookCommit{}, 0
	if !deleted && ev.Kind != "tag" {
		commits, total = s.pushCommits(ctx, r.ID, ev, created)
	}
	p.Commits, p.CommitsTotal = &commits, &total
	name := ev.Branch
	var text, summary string
	switch {
	case deleted:
		text = fmt.Sprintf("%s deleted %s in %s", who, name, full)
		summary = name + " deleted by " + who
	case ev.Kind == "tag":
		text = fmt.Sprintf("%s tagged %s in %s", who, name, full)
		summary = "tag " + name + " by " + who
	case created:
		text = fmt.Sprintf("%s created %s in %s", who, name, full)
		summary = name + " created by " + who
	default:
		text = fmt.Sprintf("%s pushed %s to %s %s", who, plural(total, "commit"), full, name)
		summary = fmt.Sprintf("%s · %s by %s", name, plural(total, "commit"), who)
	}
	if forced && !created && !deleted {
		text += " (forced)"
	}
	p.Text = text
	b, err := json.Marshal(p)
	return b, summary, err
}

// pushCommits is the newest 20 commits a push brought, newest first, and
// how many it brought; a branch created has its head commit.
func (s *Service) pushCommits(ctx context.Context, repoID string, ev *StoredEvent, created bool) ([]hookCommit, int) {
	var list []gitstore.Commit
	total := 0
	if created {
		var page gitstore.Page[gitstore.Commit]
		if err := s.git.Browse(ctx, repoID, "commits", url.Values{"ref": {ev.After}, "limit": {"1"}}, &page); err == nil {
			list, total = page.Items, len(page.Items)
		}
	} else {
		var info struct {
			Count   int               `json:"count"`
			Commits []gitstore.Commit `json:"commits"`
		}
		if err := s.git.Browse(ctx, repoID, "range", url.Values{"from": {ev.Before}, "to": {ev.After}}, &info); err == nil {
			list, total = info.Commits, info.Count
			slices.Reverse(list)
		}
	}
	if len(list) > maxPushCommits {
		list = list[:maxPushCommits]
	}
	out := make([]hookCommit, 0, len(list))
	for _, c := range list {
		out = append(out, hookCommit{SHA: c.SHA, Subject: truncateRunes(c.Subject, 300), AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail, AuthoredAt: c.AuthoredAt.UTC()})
	}
	return out, total
}

func (s *Service) hookSender(ctx context.Context, actorID string) *hookActor {
	if actorID == "" {
		return nil
	}
	u, err := s.members.GetByID(ctx, actorID)
	if err != nil {
		return &hookActor{ID: actorID}
	}
	return &hookActor{ID: actorID, Name: u.Name}
}

func (s *Service) toHookPull(p *StoredPull, r *HookRepo) *hookPull {
	hp := &hookPull{Number: p.Number, Title: p.Title, State: p.State, Base: p.Base, Head: p.Head, HeadSHA: p.HeadSHA}
	if host := s.host(); host != "" {
		hp.URL = "https://" + host + "/projects/" + r.ProjectID + "/code/" + r.Name + "/pulls/" + strconv.Itoa(p.Number)
	}
	if p.MergeSHA != "" {
		m := p.MergeSHA
		hp.MergeSHA = &m
	}
	if p.AuthorID != "" {
		hp.Author = &hookActor{ID: p.AuthorID, Name: p.AuthorName}
	}
	return hp
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
