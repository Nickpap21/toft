// toft is the command-line client for a Toft instance, for CI deploys first
// (D-18, D-59): deploy, logs, rollback. Its types come from the OpenAPI
// document (pkg/apiclient, generated); the three calls are plain net/http.
//
//	TOFT_URL=https://api.example TOFT_API_KEY=paas_… TOFT_ORG=<workspace id> \
//	  toft deploy storefront/production/api --image 1.2.3 --wait
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Nickpap21/toft/backend/pkg/apiclient"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

const usage = `usage: toft [-url URL] [-key KEY] [-org ID] [-json] <command> [flags] <service>

commands:
  deploy   <service> [--image TAG | --ref REF] [--wait]   start a deploy
  logs     <service> [--follow] [--tail N]                the container's logs
  rollback <service> [--to DEPLOYMENT_ID] [--wait]        deploy an earlier version again
  upgrade  --to vX.Y.Z                                    on the panel host, as root: move to a release
  release-check                                           on the panel host, as root: look for a newer release
  rotate-encryption-key                                   on the panel host, as root: replace ENCRYPTION_KEY
  answer-on-ip                                            on the panel host, as root: a restored panel answers on its IP again

<service> is a service ID, or project/environment/name (project/name for a
project-level service). TOFT_URL, TOFT_API_KEY and TOFT_ORG set the defaults
of -url, -key and -org (the workspace, sent as X-Toft-Org).`

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("toft", flag.ContinueOnError)
	global.SetOutput(stderr)
	url := global.String("url", getenv("TOFT_URL"), "the instance's API, e.g. https://api.toft.example")
	key := global.String("key", getenv("TOFT_API_KEY"), "an API key")
	org := global.String("org", getenv("TOFT_ORG"), "the workspace ID (X-Toft-Org)")
	asJSON := global.Bool("json", false, "print the API's objects as JSON")
	if err := global.Parse(args); err != nil {
		return 2
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	// upgrade, release-check, rotate-encryption-key and answer-on-ip act on
	// the panel host, not through the API (D-63, D-68, D-126).
	switch rest[0] {
	case "upgrade":
		return exitCode(upgradeCmd(context.Background(), rest[1:], getenv, stdout, stderr), stderr)
	case "release-check":
		return exitCode(releaseCheckCmd(context.Background(), rest[1:], getenv, stdout, stderr), stderr)
	case "rotate-encryption-key":
		return exitCode(rotateCmd(context.Background(), rest[1:], stdout, stderr), stderr)
	case "answer-on-ip":
		return exitCode(answerOnIPCmd(context.Background(), rest[1:], stdout, stderr), stderr)
	}
	if *url == "" || *key == "" {
		fmt.Fprintln(stderr, "toft: -url and -key are required (TOFT_URL, TOFT_API_KEY)")
		return 2
	}
	c := &client{base: strings.TrimRight(*url, "/"), key: *key, org: *org, http: &http.Client{Timeout: 30 * time.Second}}
	ctx := context.Background()
	var err error
	switch rest[0] {
	case "deploy":
		err = deploy(ctx, c, rest[1:], *asJSON, stdout, stderr)
	case "logs":
		err = logs(ctx, c, rest[1:], stdout, stderr)
	case "rollback":
		err = rollback(ctx, c, rest[1:], *asJSON, stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return exitCode(err, stderr)
}

// exitCode prints err and maps it: 0 for none, 2 for a usage error, 1 else.
func exitCode(err error, stderr io.Writer) int {
	var ue usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ue):
		fmt.Fprintln(stderr, "toft:", err)
		return 2
	default:
		fmt.Fprintln(stderr, "toft:", err)
		return 1
	}
}

// splitService takes the <service> out of a command's arguments wherever
// it sits, so flags may come before or after it.
func splitService(args []string) (service string, flags []string) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") && (i == 0 || !takesValue(args[i-1])) {
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return "", args
}

func takesValue(flag string) bool {
	switch strings.TrimLeft(flag, "-") {
	case "image", "ref", "to", "tail":
		return !strings.Contains(flag, "=")
	}
	return false
}

type usageError string

func (e usageError) Error() string { return string(e) }

// ─── commands ────────────────────────────────────────────────────────────────

func deploy(ctx context.Context, c *client, args []string, asJSON bool, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	image := fs.String("image", "", "deploy this image tag")
	ref := fs.String("ref", "", "deploy this git branch or tag")
	wait := fs.Bool("wait", false, "follow the deploy and exit with its result")
	service, flags := splitService(args)
	if err := fs.Parse(flags); err != nil || service == "" || fs.NArg() != 0 {
		return usageError("deploy needs one <service>")
	}
	id, err := c.resolve(ctx, service)
	if err != nil {
		return err
	}
	in := apiclient.TriggerDeploymentInput{}
	if *image != "" {
		in.ImageTag = image
	}
	if *ref != "" {
		in.GitRef = ref
	}
	var d apiclient.Deployment
	if err := c.do(ctx, http.MethodPost, "/api/v1/services/"+id+"/deployments", in, &d); err != nil {
		return err
	}
	if asJSON {
		return printJSON(stdout, d)
	}
	fmt.Fprintf(stdout, "deployment %s started (%s)\n", d.Id, d.Status)
	if !*wait {
		return nil
	}
	return c.follow(ctx, id, d.Id, stdout)
}

func rollback(ctx context.Context, c *client, args []string, asJSON bool, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "the deployment to deploy again; the last successful one before the newest when empty")
	wait := fs.Bool("wait", false, "follow the deploy and exit with its result")
	service, flags := splitService(args)
	if err := fs.Parse(flags); err != nil || service == "" || fs.NArg() != 0 {
		return usageError("rollback needs one <service>")
	}
	id, err := c.resolve(ctx, service)
	if err != nil {
		return err
	}
	target := *to
	if target == "" {
		var list []apiclient.Deployment
		if err := c.do(ctx, http.MethodGet, "/api/v1/services/"+id+"/deployments?limit=50", nil, &list); err != nil {
			return err
		}
		for i, d := range list {
			if i > 0 && succeeded(string(d.Status)) {
				target = d.Id
				break
			}
		}
		if target == "" {
			return errors.New("no earlier successful deployment to roll back to; pass --to")
		}
	}
	var d apiclient.Deployment
	if err := c.do(ctx, http.MethodPost, "/api/v1/services/"+id+"/deployments/"+target+"/rollback", nil, &d); err != nil {
		return err
	}
	if asJSON {
		return printJSON(stdout, d)
	}
	fmt.Fprintf(stdout, "rollback to %s started as deployment %s\n", target, d.Id)
	if !*wait {
		return nil
	}
	return c.follow(ctx, id, d.Id, stdout)
}

func logs(ctx context.Context, c *client, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	follow := fs.Bool("follow", false, "keep printing new lines")
	tail := fs.Int("tail", 100, "lines of history")
	service, flags := splitService(args)
	if err := fs.Parse(flags); err != nil || service == "" || fs.NArg() != 0 {
		return usageError("logs needs one <service>")
	}
	id, err := c.resolve(ctx, service)
	if err != nil {
		return err
	}
	events, errs := c.stream(ctx, fmt.Sprintf("/api/v1/services/%s/logs/live?tail=%d", id, *tail))
	quiet := time.NewTimer(time.Hour)
	if !*follow {
		quiet.Reset(1500 * time.Millisecond)
	}
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return <-errs
			}
			switch ev.name {
			case "", "message":
				fmt.Fprintln(stdout, ev.data)
			case "error":
				return errors.New(ev.data)
			case "done":
				return nil
			}
			if !*follow {
				quiet.Reset(1500 * time.Millisecond)
			}
		case <-quiet.C:
			return nil
		}
	}
}

// ─── the client ──────────────────────────────────────────────────────────────

type client struct {
	base, key, org string
	http           *http.Client
}

// apiError is a Problem (D-59): detail and code, plus the deploy policy's
// violations when there are any.
type apiError struct {
	Status     int
	Code       string
	Detail     string
	Violations []apiclient.Violation
}

func (e *apiError) Error() string {
	var b strings.Builder
	b.WriteString(e.Detail)
	if e.Code != "" {
		fmt.Fprintf(&b, " (%s)", e.Code)
	}
	for _, v := range e.Violations {
		fmt.Fprintf(&b, "\n  %s: %s", v.Rule, v.Message)
	}
	return b.String()
}

func (c *client) request(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if c.org != "" {
		req.Header.Set("X-Toft-Org", c.org)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do sends a JSON request and decodes the envelope's data into out.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return problem(res.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("unexpected answer: %w", err)
	}
	return json.Unmarshal(env.Data, out)
}

func problem(status int, raw []byte) error {
	var p struct {
		Detail string `json:"detail"`
		Error  string `json:"error"`
		Code   string `json:"code"`
		Data   struct {
			Code       string                `json:"code"`
			Violations []apiclient.Violation `json:"violations"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &p)
	e := &apiError{Status: status, Code: p.Code, Detail: p.Detail, Violations: p.Data.Violations}
	if e.Code == "" {
		e.Code = p.Data.Code
	}
	if e.Detail == "" {
		e.Detail = p.Error
	}
	if e.Detail == "" {
		e.Detail = http.StatusText(status)
	}
	return e
}

// resolve turns a service ID or project/environment/name into an ID.
func (c *client) resolve(ctx context.Context, ref string) (string, error) {
	if !strings.Contains(ref, "/") {
		return ref, nil
	}
	parts := strings.Split(ref, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return "", usageError("a service is <id>, project/environment/name or project/name")
	}
	var projects []apiclient.Project
	if err := c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &projects); err != nil {
		return "", err
	}
	projectID := ""
	for _, p := range projects {
		if p.Name == parts[0] {
			projectID = p.Id
		}
	}
	if projectID == "" {
		return "", fmt.Errorf("no project named %q in this workspace", parts[0])
	}
	var services []apiclient.WorkspaceService
	if err := c.do(ctx, http.MethodGet, "/api/v1/workspace/services", nil, &services); err != nil {
		return "", err
	}
	env, name := "", parts[len(parts)-1]
	if len(parts) == 3 {
		env = parts[1]
	}
	for _, s := range services {
		if s.ProjectId == projectID && s.Name == name && (env == "" || s.EnvironmentName == env) {
			return s.Id, nil
		}
	}
	return "", fmt.Errorf("no service %q in %s", ref, parts[0])
}

// ─── streams ─────────────────────────────────────────────────────────────────

type event struct{ name, data string }

// stream follows a server-sent events endpoint until it ends.
func (c *client) stream(ctx context.Context, path string) (<-chan event, <-chan error) {
	events := make(chan event)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		req, err := c.request(ctx, http.MethodGet, path, nil)
		if err != nil {
			errs <- err
			return
		}
		res, err := (&http.Client{}).Do(req)
		if err != nil {
			errs <- err
			return
		}
		defer res.Body.Close()
		if res.StatusCode >= 400 {
			raw, _ := io.ReadAll(res.Body)
			errs <- problem(res.StatusCode, raw)
			return
		}
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		var cur event
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if cur.data != "" || cur.name != "" {
					events <- cur
				}
				cur = event{}
			}
		}
		errs <- sc.Err()
	}()
	return events, errs
}

// follow prints a deployment's log and returns nil when it succeeded, or
// an error naming its final status.
func (c *client) follow(ctx context.Context, serviceID, deploymentID string, stdout io.Writer) error {
	events, errs := c.stream(ctx, "/api/v1/services/"+serviceID+"/deployments/"+deploymentID+"/logs/stream")
	for ev := range events {
		switch ev.name {
		case "done":
			var done struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal([]byte(ev.data), &done)
			if succeeded(done.Status) {
				fmt.Fprintf(stdout, "deployment %s: %s\n", deploymentID, done.Status)
				return nil
			}
			return fmt.Errorf("deployment %s ended %s", deploymentID, done.Status)
		case "error":
			return errors.New(ev.data)
		default:
			var l apiclient.Log
			if json.Unmarshal([]byte(ev.data), &l) == nil && l.Message != "" {
				fmt.Fprintln(stdout, l.Message)
			} else if ev.data != "" {
				fmt.Fprintln(stdout, ev.data)
			}
		}
	}
	if err := <-errs; err != nil {
		return err
	}
	return errors.New("the log stream ended before the deployment did")
}

// succeeded: a deployment that ended with the service running.
func succeeded(status string) bool {
	return status == "running" || status == "success" || status == "succeeded"
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
