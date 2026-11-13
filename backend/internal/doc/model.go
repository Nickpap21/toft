// Package doc is a project's docs (D-81, D-86): pages in a tree, every save
// a version, connected to the project's services, issues and pull requests
// through blocks fenced in their markdown.
package doc

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nickpap21/toft/backend/internal/code"
)

const (
	maxTitle = 200
	maxBody  = 256 << 10
	maxPages = 1000
)

var (
	ErrNotFound    = errors.New("page not found")
	ErrBadTitle    = errors.New("a title has 1 to 200 characters, without control characters")
	ErrBadBody     = errors.New("a page holds 256 KiB of text at most")
	ErrBadParent   = errors.New("a page goes into another page of the same project, never into itself or one of its own pages")
	ErrBadVersion  = errors.New("a new title or text needs base_version, the version it was written from")
	ErrConflict    = errors.New("the page changed since that version: reload it")
	ErrTooMany     = errors.New("a project holds 1,000 pages at most")
	ErrHasPages    = errors.New("move or delete the pages in it first")
	ErrDeleted     = errors.New("the page is deleted: restore it first")
	ErrNoStep      = errors.New("the page has no such command")
	ErrBadCursor   = errors.New("bad cursor")
	ErrHostCommand = errors.New("commands on a server are for instance operators")
	// ErrAgentHostStep: agents run steps in a service's containers only (Q-12, D-102).
	ErrAgentHostStep = errors.New("agents run steps in a service's containers only; a step on a server's host is for an instance operator")
)

// Person is someone who wrote, verified or owns a page.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func person(id, name string) *Person {
	if id == "" {
		return nil
	}
	return &Person{ID: id, Name: name}
}

// TreePage is a page as the docs' tree shows it.
type TreePage struct {
	ID        string     `json:"id"`
	ParentID  *string    `json:"parent_id"`
	Title     string     `json:"title"`
	Position  int        `json:"position"`
	Stale     bool       `json:"stale"`
	Runbook   bool       `json:"runbook"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at"`
}

// Page is a page with what its blocks show.
type Page struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	ParentID     *string        `json:"parent_id"`
	Title        string         `json:"title"`
	Body         string         `json:"body"`
	HTML         string         `json:"html"`
	Version      int            `json:"version"`
	Position     int            `json:"position"`
	CreatedBy    *Person        `json:"created_by"`
	UpdatedBy    *Person        `json:"updated_by"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	VerifiedAt   *time.Time     `json:"verified_at"`
	VerifiedBy   *Person        `json:"verified_by"`
	DeletedAt    *time.Time     `json:"deleted_at"`
	Path         []code.DocRef  `json:"path"`
	Blocks       []Block        `json:"blocks"`
	Services     []ServiceState `json:"services"`
	Stale        bool           `json:"stale"`
	ReferencedBy []code.DocRef  `json:"referenced_by"`
}

// Block is one of a page's fenced blocks.
type Block struct {
	Kind      string `json:"kind"`
	ServiceID string `json:"service_id,omitempty"`
	Host      bool   `json:"host,omitempty"`
	Command   string `json:"command,omitempty"`
	Issue     *Issue `json:"issue,omitempty"`
	Pull      *Pull  `json:"pull,omitempty"`
	ref       string // toft-issue's KEY-12, toft-pull's repository!40
}

// Issue is an issue as a page shows it.
type Issue struct {
	Key       string  `json:"key"`
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	ProjectID string  `json:"project_id"`
	Assignee  *Person `json:"assignee"`
	Priority  string  `json:"priority,omitempty"`
}

// Pull is a pull request as a page shows it.
type Pull struct {
	Repository   string `json:"repository"`
	RepositoryID string `json:"repository_id"`
	Number       int    `json:"number"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Checks       string `json:"checks"`
}

// ServiceState is a service a page names, as it is now, and what changed
// since the page last saw it.
type ServiceState struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Environment *string    `json:"environment"`
	Status      string     `json:"status"`
	Server      *string    `json:"server"`
	Image       string     `json:"image"`
	Commit      *string    `json:"commit"`
	DeployedAt  *time.Time `json:"deployed_at"`
	SecretSet   *SecretSet `json:"secret_set"`
	Changes     []Change   `json:"changes"`
	Deployment  *Deploy    `json:"deployment"`
}

type SecretSet struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}

// Deploy is the deploy that went live since a page last saw its service.
type Deploy struct {
	ID         string    `json:"id"`
	Commit     *string   `json:"commit"`
	FinishedAt time.Time `json:"finished_at"`
}

// Change is one way a service changed since a page last saw it. A
// variable's or secret's from and to are when it was written: from is nil
// when it was added, to when it was removed.
type Change struct {
	Field string  `json:"field"`
	Name  string  `json:"name"`
	From  *string `json:"from"`
	To    *string `json:"to"`
}

// Version is one version of a page.
type Version struct {
	Version   int       `json:"version"`
	Title     string    `json:"title"`
	Author    *Person   `json:"author"`
	CreatedAt time.Time `json:"created_at"`
}

// VersionDetail is a version as it was.
type VersionDetail struct {
	Version
	Body string `json:"body"`
	HTML string `json:"html"`
}

// Work is what touches a service (its Connected work).
type Work struct {
	Pulls  []Pull     `json:"pulls"`
	Issues []Issue    `json:"issues"`
	Pages  []WorkPage `json:"pages"`
}

type WorkPage struct {
	ID         string     `json:"id"`
	Title      string     `json:"title"`
	Stale      bool       `json:"stale"`
	VerifiedAt *time.Time `json:"verified_at"`
}

// CreateInput writes a page.
type CreateInput struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	ParentID string `json:"parent_id"`
}

// UpdateInput saves or moves a page; only what is sent changes.
type UpdateInput struct {
	Title       *string `json:"title"`
	Body        *string `json:"body"`
	BaseVersion int     `json:"base_version"`
	ParentID    *string `json:"parent_id"`
	Position    *int    `json:"position"`
}

// Step is one of a page's commands.
type Step struct {
	ServiceID string
	Host      bool
	Command   string
}

func checkTitle(t string) (string, error) {
	t = strings.TrimSpace(t)
	if t == "" || utf8.RuneCountInString(t) > maxTitle || strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", ErrBadTitle
	}
	return t, nil
}

func checkBody(b string) error {
	if len(b) > maxBody || !utf8.ValidString(b) {
		return ErrBadBody
	}
	return nil
}

var (
	uuidRe    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	issueRef  = regexp.MustCompile(`^([A-Z][A-Z0-9]{1,9})-0*(\d{1,9})$`)
	pullRefRe = regexp.MustCompile(`^([A-Za-z0-9._-]{1,100})!(\d{1,9})$`)
)

// parseBlock reads a fence's info string: toft-service <id>, toft-run <id>
// [host] with its command, toft-issue KEY-12, toft-pull repository!40.
func parseBlock(info, body string) Block {
	f := strings.Fields(info)
	arg := ""
	if len(f) > 1 {
		arg = f[1]
	}
	switch f[0] {
	case "toft-service":
		if uuidRe.MatchString(arg) {
			return Block{Kind: "service", ServiceID: arg}
		}
	case "toft-run":
		if uuidRe.MatchString(arg) && strings.TrimSpace(body) != "" {
			return Block{Kind: "run", ServiceID: arg, Host: len(f) > 2 && f[2] == "host", Command: body}
		}
	case "toft-issue":
		if m := issueRef.FindStringSubmatch(strings.ToUpper(arg)); m != nil {
			return Block{Kind: "issue", ref: m[1] + "-" + m[2]}
		}
	case "toft-pull":
		if pullRefRe.MatchString(arg) {
			return Block{Kind: "pull", ref: arg}
		}
	}
	return Block{Kind: "unknown"}
}

// seen is what a page saw of a service (D-86). The page is out of date
// once the service's differs: an image service's image, how it builds and
// starts, its server, and its variables and secrets. Not a git-built
// service's commits, or every push would date every page about it.
type seen struct {
	Image      string             `json:"image,omitempty"`
	Build      string             `json:"build,omitempty"`
	Server     string             `json:"server"`
	ServerName string             `json:"server_name"`
	Vars       map[string]varSeen `json:"vars"`
}

type varSeen struct {
	At     time.Time `json:"at"`
	Secret bool      `json:"secret"`
}

// serviceNow is a service's state, as the repository reads it.
type serviceNow struct {
	PageID, ServiceID         string
	Seen                      []byte // what the page saw; {} when just linked
	SeenAt                    time.Time
	Name, Environment, Status string
	SourceType, DockerImage   string
	BuildPack, BaseDirectory  string
	Install, BuildCmd, Start  string
	Port                      int
	ComposeHash, ComposeFile  string
	ServerID, ServerName      string
	LiveImage, LiveCommit     string
	LiveAt                    *time.Time
	Vars                      map[string]varSeen
	SinceID, SinceCommit      string
	SinceAt                   *time.Time
}

func (s serviceNow) seen() seen {
	out := seen{Server: s.ServerID, ServerName: s.ServerName, Vars: s.Vars}
	if s.SourceType == "docker-image" {
		out.Image = s.DockerImage
	}
	h := sha256.Sum256([]byte(strings.Join([]string{s.SourceType, s.BuildPack, s.BaseDirectory, s.Install, s.BuildCmd, s.Start,
		strconv.Itoa(s.Port), s.ComposeHash, s.ComposeFile}, "\x00")))
	out.Build = hex.EncodeToString(h[:8])
	if out.Vars == nil {
		out.Vars = map[string]varSeen{}
	}
	return out
}

// changes is how now differs from was, in a stable order.
func changes(was, now seen) []Change {
	out := []Change{}
	if was.Image != now.Image {
		out = append(out, Change{Field: "image", From: opt(was.Image), To: opt(now.Image)})
	}
	if was.Build != now.Build {
		out = append(out, Change{Field: "build"})
	}
	if was.Server != now.Server {
		out = append(out, Change{Field: "server", From: opt(serverName(was)), To: opt(serverName(now))})
	}
	keys := make([]string, 0, len(was.Vars)+len(now.Vars))
	for k := range was.Vars {
		keys = append(keys, k)
	}
	for k := range now.Vars {
		if _, ok := was.Vars[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		a, hadA := was.Vars[k]
		b, hasB := now.Vars[k]
		if hadA && hasB && a.At.Equal(b.At) && a.Secret == b.Secret {
			continue
		}
		field := "variable"
		if hasB && b.Secret || !hasB && a.Secret {
			field = "secret"
		}
		c := Change{Field: field, Name: k}
		if hadA {
			c.From = opt(a.At.UTC().Format(time.RFC3339))
		}
		if hasB {
			c.To = opt(b.At.UTC().Format(time.RFC3339))
		}
		out = append(out, c)
	}
	return out
}

func serverName(s seen) string {
	if s.Server == "" {
		return "the panel's server"
	}
	return s.ServerName
}

func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
