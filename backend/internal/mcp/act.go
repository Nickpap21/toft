package mcp

import (
	"net/http"
	"strconv"
	"strings"
)

// Session 2's acting tools (D-102). Each runs its operation once; the use
// case behind it runs the agent policy, so an answer is done, refused, or
// waiting for a person (acted waits then).

// acted takes an act's answer: refused, waiting for a person (then waits
// for them), or done, its data returned.
func (c *call) acted(st int, resp map[string]any) (any, error) {
	if st == http.StatusAccepted && s(obj(data(resp))["code"]) == "approval_pending" {
		var err error
		st, resp, err = c.waitApproval(s(obj(data(resp))["approval_id"]))
		if err != nil {
			return nil, err
		}
	}
	if st >= 300 {
		return nil, c.apiError(st, resp)
	}
	return data(resp), nil
}

// repoID is repository_id, or the project's first repository.
func (c *call) repoID(projectID string) (string, error) {
	if id := c.str("repository_id"); id != "" {
		return id, nil
	}
	st, body, err := c.op("getProjectsProjectidRepositories", map[string]string{"projectID": projectID}, nil, nil)
	if err != nil {
		return "", err
	}
	if st != http.StatusOK {
		return "", c.apiError(st, body)
	}
	repos := list(data(body))
	if len(repos) == 0 {
		return "", fail("the project has no repository")
	}
	return s(obj(repos[0])["id"]), nil
}

// scopeOf is the service or the environment a variable or secret belongs
// to: exactly one of service_id and environment_id.
func (c *call) scopeOf() (kind, id string, err error) {
	sid, eid := c.str("service_id"), c.str("environment_id")
	switch {
	case sid != "" && eid == "":
		c.resource = "service:" + sid
		return "service", sid, nil
	case eid != "" && sid == "":
		c.resource = "environment:" + eid
		return "environment", eid, nil
	}
	return "", "", fail("give service_id or environment_id, one of them")
}

func (c *call) body(keys ...string) map[string]any {
	b := map[string]any{}
	for _, k := range keys {
		if v, ok := c.args[k]; ok {
			if str, isStr := v.(string); isStr {
				v = strings.TrimSpace(str)
			}
			b[k] = v
		}
	}
	return b
}

func createBranch(c *call) (any, error) {
	pid := c.str("project_id")
	c.resource = "project:" + pid
	rid, err := c.repoID(pid)
	if err != nil {
		return nil, err
	}
	st, resp, err := c.op("createBranch", map[string]string{"projectID": pid, "repositoryID": rid}, nil, c.body("name", "from"))
	if err != nil {
		return nil, err
	}
	out, err := c.acted(st, resp)
	if err != nil {
		return nil, err
	}
	b := obj(out)
	if p := obj(b["push"]); p != nil && s(p["password"]) == "" {
		return map[string]any{"branch": b, "next": "a person approved this; call create_branch again on the same branch for its push password (that needs no approval)"}, nil
	}
	return map[string]any{"branch": b, "next": "push your commits with push.command (only this branch takes them), then call open_pr with head " + s(b["branch"])}, nil
}

func openPR(c *call) (any, error) {
	pid := c.str("project_id")
	c.resource = "project:" + pid
	rid, err := c.repoID(pid)
	if err != nil {
		return nil, err
	}
	path := map[string]string{"projectID": pid, "repositoryID": rid}
	body := c.body("title", "body", "head", "base")
	if body["base"] == nil || body["base"] == "" {
		st, repo, err := c.op("getRepository", path, nil, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, repo)
		}
		body["base"] = s(obj(data(repo))["default_branch"])
	}
	st, resp, err := c.op("openPullRequest", path, nil, body)
	if err != nil {
		return nil, err
	}
	return c.acted(st, resp)
}

func createIssue(c *call) (any, error) {
	pid := c.str("project_id")
	c.resource = "project:" + pid
	st, resp, err := c.op("createIssue", map[string]string{"projectID": pid}, nil, c.body("title", "description", "priority", "labels", "type_id"))
	if err != nil {
		return nil, err
	}
	return c.acted(st, resp)
}

func comment(c *call) (any, error) {
	pid := c.str("project_id")
	c.resource = "project:" + pid
	body := c.body("body")
	var st int
	var resp map[string]any
	var err error
	switch {
	case c.str("pull_number") != "":
		rid, rerr := c.repoID(pid)
		if rerr != nil {
			return nil, rerr
		}
		st, resp, err = c.op("addPullRequestComment", map[string]string{"projectID": pid, "repositoryID": rid, "pullNumber": c.str("pull_number")}, nil, body)
	case c.str("issue_number") != "":
		st, resp, err = c.op("addIssueComment", map[string]string{"projectID": pid, "issueNumber": c.str("issue_number")}, nil, body)
	default:
		return nil, fail("give issue_number or pull_number")
	}
	if err != nil {
		return nil, err
	}
	return c.acted(st, resp)
}

func setEnvVar(c *call) (any, error) {
	kind, id, err := c.scopeOf()
	if err != nil {
		return nil, err
	}
	op, param := "postServicesServiceidEnvVars", "serviceID"
	if kind == "environment" {
		op, param = "postEnvironmentsEnvironmentidEnvVars", "environmentID"
	}
	st, resp, err := c.op(op, map[string]string{param: id}, nil, c.body("key", "value", "masked"))
	if err != nil {
		return nil, err
	}
	out, err := c.acted(st, resp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"variable": out, "next": "it takes effect at the next deploy"}, nil
}

// setSecret sets a new secret, or a new value for one that exists; the
// value never comes back, and the audit entry never holds it.
func setSecret(c *call) (any, error) {
	kind, id, err := c.scopeOf()
	if err != nil {
		return nil, err
	}
	list_, create, replace, param := "getServicesServiceidSecrets", "postServicesServiceidSecrets", "putServicesServiceidSecretsKey", "serviceID"
	if kind == "environment" {
		list_, create, replace, param = "getEnvironmentsEnvironmentidSecrets", "postEnvironmentsEnvironmentidSecrets", "putEnvironmentsEnvironmentidSecretsKey", "environmentID"
	}
	st, have, err := c.op(list_, map[string]string{param: id}, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, have)
	}
	key, value := c.str("key"), s(c.args["value"])
	exists := false
	for _, sv := range list(data(have)) {
		if s(obj(sv)["key"]) == key {
			exists = true
		}
	}
	var resp map[string]any
	if exists {
		st, resp, err = c.op(replace, map[string]string{param: id, "key": key}, nil, map[string]any{"value": value})
	} else {
		st, resp, err = c.op(create, map[string]string{param: id}, nil, map[string]any{"key": key, "value": value})
	}
	if err != nil {
		return nil, err
	}
	if _, err := c.acted(st, resp); err != nil {
		return nil, err
	}
	what := "set"
	if exists {
		what = "replaced"
	}
	return map[string]any{"secret": key, "result": what, "next": "it takes effect at the next deploy; nobody can read it back"}, nil
}

func runRunbookStep(c *call) (any, error) {
	pid, page := c.str("project_id"), c.str("page_id")
	c.resource = "doc_page:" + page
	st, p, err := c.op("getDocPage", map[string]string{"projectID": pid, "pageID": page}, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, p)
	}
	version, _ := obj(data(p))["version"].(float64)
	// The agent counts steps from 1, as the page and the request do; the
	// API's path counts from 0.
	step := c.num("step", 0)
	if step < 1 {
		return nil, fail("step counts from 1: the first command on the page is 1")
	}
	st, resp, err := c.op("runDocStep", map[string]string{"projectID": pid, "pageID": page, "step": strconv.Itoa(step - 1)}, nil,
		map[string]any{"version": int(version)})
	if err != nil {
		return nil, err
	}
	out, err := c.acted(st, resp)
	if err != nil {
		return nil, err
	}
	r := obj(out)
	if s(r["outcome"]) != "ok" {
		c.detail = "step failed"
		lines := make([]string, 0)
		for _, l := range list(r["lines"]) {
			lines = append(lines, s(l))
		}
		return nil, fail("The step failed: %s\n%s", s(r["error"]), joinLines(lines))
	}
	return r, nil
}

func createPreview(c *call) (any, error) {
	pid := c.str("project_id")
	c.resource = "project:" + pid
	rid, err := c.repoID(pid)
	if err != nil {
		return nil, err
	}
	st, resp, err := c.op("startPullRequestPreview", map[string]string{"projectID": pid, "repositoryID": rid, "pullNumber": c.str("pull_number")}, nil, nil)
	if err != nil {
		return nil, err
	}
	out, err := c.acted(st, resp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"preview": out, "next": "follow it with get_service on its services, or the pull request's page"}, nil
}
