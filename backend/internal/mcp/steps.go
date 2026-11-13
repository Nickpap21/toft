package mcp

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// steps are what each tool does, by name. TestToolsMatchSpec checks every
// tool in the spec has steps and every step a tool.
var steps = map[string]func(c *call) (any, error){
	"list_projects":   listProjects,
	"get_service":     getService,
	"get_deploy_logs": getDeployLogs,
	"get_capacity":    getCapacity,
	"search_docs":     searchDocs,
	"get_issue":       getIssue,
	"get_diff":        getDiff,
	"deploy":          deploy,
	"rollback":        rollback,

	"create_branch":    createBranch,
	"open_pr":          openPR,
	"create_issue":     createIssue,
	"comment":          comment,
	"set_env_var":      setEnvVar,
	"set_secret":       setSecret,
	"run_runbook_step": runRunbookStep,
	"create_preview":   createPreview,
}

func list(v any) []any { l, _ := v.([]any); return l }

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }

func s(v any) string { str, _ := v.(string); return str }

func listProjects(c *call) (any, error) {
	st, body, err := c.op("getProjects", nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, body)
	}
	st, sbody, err := c.op("getWorkspaceServices", nil, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, sbody)
	}
	byProject := map[string][]map[string]any{}
	for _, sv := range list(data(sbody)) {
		m := obj(sv)
		byProject[s(m["project_id"])] = append(byProject[s(m["project_id"])], map[string]any{
			"id": m["id"], "name": m["name"], "environment": m["environment_name"], "scope": m["scope"],
			"status": m["runtime_status"], "source": m["source_type"], "image": m["docker_image"],
			"git_repo": m["git_repo_url"], "git_branch": m["git_branch"], "server_id": m["server_id"],
		})
	}
	out := make([]map[string]any, 0)
	for _, pv := range list(data(body)) {
		p := obj(pv)
		id := s(p["id"])
		if !c.reaches(id) {
			continue
		}
		svcs := byProject[id]
		if svcs == nil {
			svcs = []map[string]any{}
		}
		out = append(out, map[string]any{"id": id, "name": p["name"], "issue_key": p["issue_key"], "services": svcs})
	}
	if c.agent.AllProjects {
		if shared := byProject[""]; len(shared) > 0 {
			out = append(out, map[string]any{"id": nil, "name": "Workspace services", "services": shared})
		}
	}
	return map[string]any{"projects": out}, nil
}

func getService(c *call) (any, error) {
	id := c.str("service_id")
	c.resource = "service:" + id
	st, body, err := c.op("getServicesServiceid", map[string]string{"serviceID": id}, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, body)
	}
	st, deps, err := c.op("getServicesServiceidDeployments", map[string]string{"serviceID": id}, url.Values{"limit": {"10"}}, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, deps)
	}
	st, doms, err := c.op("getServicesServiceidDomains", map[string]string{"serviceID": id}, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, doms)
	}
	domains := make([]map[string]any, 0)
	for _, dv := range list(data(doms)) {
		d := obj(dv)
		domains = append(domains, map[string]any{"hostname": d["hostname"], "status": d["status"], "tls_enabled": d["tls_enabled"],
			"temporary": d["temporary"], "routed_at": d["routed_at"], "certificate_at": d["certificate_at"]})
	}
	return map[string]any{"service": data(body), "deployments": data(deps), "domains": domains}, nil
}

// logTail is the last n lines of a deployment's log, one string each.
func (c *call) logTail(serviceID, deploymentID, search string, n int) ([]string, error) {
	q := url.Values{}
	if search != "" {
		q.Set("search", search)
	}
	st, body, err := c.op("getServicesServiceidDeploymentsDeploymentidLogs", map[string]string{"serviceID": serviceID, "deploymentID": deploymentID}, q, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, body)
	}
	all := list(data(body))
	if len(all) > n {
		all = all[len(all)-n:]
	}
	out := make([]string, 0, len(all))
	for _, lv := range all {
		l := obj(lv)
		out = append(out, fmt.Sprintf("%s %s %s", s(l["created_at"]), s(l["level"]), s(l["message"])))
	}
	return out, nil
}

func getDeployLogs(c *call) (any, error) {
	sid, did := c.str("service_id"), c.str("deployment_id")
	c.resource = "service:" + sid
	if did == "" {
		st, body, err := c.op("getServicesServiceidDeployments", map[string]string{"serviceID": sid}, url.Values{"limit": {"1"}}, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		deps := list(data(body))
		if len(deps) == 0 {
			return nil, fail("the service has no deployments yet")
		}
		did = s(obj(deps[0])["id"])
	}
	st, dep, err := c.op("getServicesServiceidDeploymentsDeploymentid", map[string]string{"serviceID": sid, "deploymentID": did}, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, dep)
	}
	lines, err := c.logTail(sid, did, c.str("search"), c.num("lines", 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"deployment": data(dep), "lines": lines}, nil
}

func getCapacity(c *call) (any, error) {
	ids := []string{}
	if id := c.str("server_id"); id != "" {
		ids = append(ids, id)
	} else {
		st, body, err := c.op("getServers", nil, nil, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		for _, sv := range list(data(body)) {
			ids = append(ids, s(obj(sv)["id"]))
		}
	}
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		st, body, err := c.op("getServersServeridCapacitySummary", map[string]string{"serverID": id}, nil, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		out = append(out, data(body))
	}
	return map[string]any{"servers": out}, nil
}

func searchDocs(c *call) (any, error) {
	pid, page := c.str("project_id"), c.str("page_id")
	if page != "" {
		if pid == "" {
			return nil, fail("give project_id with page_id")
		}
		st, body, err := c.op("getDocPage", map[string]string{"projectID": pid, "pageID": page}, nil, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		return data(body), nil
	}
	q := c.str("query")
	if q == "" {
		return nil, fail("give a query, or project_id and page_id")
	}
	projects := []string{pid}
	if pid == "" && !c.agent.AllProjects {
		projects = c.agent.ProjectIDs
	}
	out := make([]any, 0)
	for _, p := range projects {
		v := url.Values{"q": {q}, "types": {"docs"}, "limit": {"20"}}
		if p != "" {
			v.Set("project_id", p)
		}
		st, body, err := c.op("searchWorkspace", nil, v, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		out = append(out, list(obj(data(body))["docs"])...)
	}
	return map[string]any{"pages": out, "read_one": "call search_docs with project_id and page_id for a page's markdown"}, nil
}

func getIssue(c *call) (any, error) {
	st, body, err := c.op("getIssue", map[string]string{"projectID": c.str("project_id"), "issueNumber": c.str("issue_number")}, nil, nil)
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, body)
	}
	return data(body), nil
}

func getDiff(c *call) (any, error) {
	pid, rid := c.str("project_id"), c.str("repository_id")
	if rid == "" {
		st, body, err := c.op("getProjectsProjectidRepositories", map[string]string{"projectID": pid}, nil, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		repos := list(data(body))
		if len(repos) == 0 {
			return nil, fail("the project has no repository")
		}
		rid = s(obj(repos[0])["id"])
	}
	path := map[string]string{"projectID": pid, "repositoryID": rid}
	var st int
	var body map[string]any
	var err error
	switch {
	case c.str("pull_number") != "":
		path["pullNumber"] = c.str("pull_number")
		st, body, err = c.op("getPullRequestDiff", path, nil, nil)
	case c.str("sha") != "":
		path["sha"] = c.str("sha")
		st, body, err = c.op("getRepositoryCommitDiff", path, nil, nil)
	case c.str("base") != "" && c.str("head") != "":
		st, body, err = c.op("compareBranchesDiff", path, url.Values{"base": {c.str("base")}, "head": {c.str("head")}}, nil)
	default:
		return nil, fail("give pull_number, sha, or base and head")
	}
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, c.apiError(st, body)
	}
	return data(body), nil
}

func deploy(c *call) (any, error) {
	sid := c.str("service_id")
	c.resource = "service:" + sid
	body := map[string]any{}
	for _, k := range []string{"git_ref", "git_ref_type", "commit_sha", "image_tag"} {
		if v := c.str(k); v != "" {
			body[k] = v
		}
	}
	st, resp, err := c.op("postServicesServiceidDeployments", map[string]string{"serviceID": sid}, nil, body)
	if err != nil {
		return nil, err
	}
	return c.started(sid, st, resp)
}

func rollback(c *call) (any, error) {
	sid, target := c.str("service_id"), c.str("deployment_id")
	c.resource = "service:" + sid
	if target == "" {
		st, body, err := c.op("getServicesServiceidDeployments", map[string]string{"serviceID": sid}, url.Values{"limit": {"20"}}, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		// The newest deployment that ran is the current one; the one that
		// ran before it is the target.
		seen := 0
		for _, dv := range list(data(body)) {
			d := obj(dv)
			if st := s(d["status"]); st == "running" || st == "stopped" {
				if seen++; seen == 2 {
					target = s(d["id"])
					break
				}
			}
		}
		if target == "" {
			return nil, fail("there's no earlier deployment that ran: give deployment_id")
		}
	}
	st, resp, err := c.op("postServicesServiceidDeploymentsDeploymentidRollback", map[string]string{"serviceID": sid, "deploymentID": target}, nil, nil)
	if err != nil {
		return nil, err
	}
	return c.started(sid, st, resp)
}

// started takes a deploy's or a rollback's answer: refused, waiting for a
// person (then waits for them), or started (then follows it, unless wait
// is false).
func (c *call) started(sid string, st int, resp map[string]any) (any, error) {
	if st == http.StatusAccepted && s(obj(data(resp))["code"]) == "approval_pending" {
		var err error
		st, resp, err = c.waitApproval(s(obj(data(resp))["approval_id"]))
		if err != nil {
			return nil, err
		}
	}
	if st != http.StatusAccepted {
		return nil, c.apiError(st, resp)
	}
	dep := obj(data(resp))
	if !c.flag("wait", true) {
		return map[string]any{"deployment": dep, "follow": "get_deploy_logs with this deployment's id"}, nil
	}
	return c.follow(sid, s(dep["id"]))
}

// waitApproval waits for a person, reporting progress, until they decide or
// the request expires. If the call drops, the request stands: Approve runs
// it, and the same call again attaches to it.
func (c *call) waitApproval(id string) (int, map[string]any, error) {
	c.outcome, c.approvalID = "asked", id
	tick := time.NewTicker(c.s.poll)
	defer tick.Stop()
	last := time.Time{}
	for {
		a, err := c.s.agents.AgentApproval(c.ctx, c.agent.ConnectionID, id)
		if err != nil {
			return 0, nil, err
		}
		switch a.State {
		case "approved":
			if a.Result != nil {
				c.outcome = "approved"
				c.detail = "approved by " + a.DecidedBy
				if a.Note != "" {
					c.note = fmt.Sprintf("\n\n%s approved this at %s%s.", a.DecidedBy, clock(a.DecidedAt), quoted(a.Note))
				}
				return a.Result.Status, a.Result.Body, nil
			}
		case "denied":
			c.outcome = "denied"
			return 0, nil, fail("%s denied this at %s%s. Nothing was done; you may ask again with another reason.", a.DecidedBy, clock(a.DecidedAt), quoted(a.Note))
		case "cancelled":
			c.outcome = "refused"
			return 0, nil, fail("%s", a.Note)
		case "expired":
			c.outcome = "expired"
			return 0, nil, fail("Nobody decided by %s, so nothing was done. Ask again when someone can approve.", a.ExpiresAt.Local().Format("15:04"))
		default:
			if time.Since(last) >= c.s.progressEvery {
				last = time.Now()
				c.progress(fmt.Sprintf("Waiting for a person to approve %s (%s): it is in Needs you until %s. Nothing has changed yet.",
					a.Target, a.Tool, a.ExpiresAt.Local().Format("15:04")))
			}
		}
		select {
		case <-c.ctx.Done():
			return 0, nil, c.ctx.Err()
		case <-tick.C:
		}
	}
}

func clock(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("15:04")
}

func quoted(note string) string {
	if note == "" {
		return ""
	}
	return ": “" + note + "”"
}

// follow waits for a deployment to end, up to maxWait, and returns it with
// its log's last lines.
func (c *call) follow(sid, did string) (any, error) {
	deadline := time.Now().Add(c.s.maxWait)
	tick := time.NewTicker(c.s.poll)
	defer tick.Stop()
	last := time.Time{}
	for {
		st, body, err := c.op("getServicesServiceidDeploymentsDeploymentid", map[string]string{"serviceID": sid, "deploymentID": did}, nil, nil)
		if err != nil {
			return nil, err
		}
		if st != http.StatusOK {
			return nil, c.apiError(st, body)
		}
		dep := obj(data(body))
		switch status := s(dep["status"]); status {
		case "running", "failed", "stopped":
			lines, err := c.logTail(sid, did, "", 50)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"deployment": dep, "last_lines": lines}
			if status != "running" {
				c.detail = "deploy " + status
				return nil, fail("The deploy %s: %s\n%s", status, s(dep["error_msg"]), joinLines(lines))
			}
			return out, nil
		default:
			if time.Now().After(deadline) {
				return map[string]any{"deployment": dep, "still_running": "after " + c.s.maxWait.String() + "; follow it with get_deploy_logs"}, nil
			}
			if time.Since(last) >= c.s.progressEvery {
				last = time.Now()
				c.progress("Deploying (" + status + ") since " + strconv.Itoa(int(time.Until(deadline).Minutes())) + " minutes before giving up")
			}
		}
		select {
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		case <-tick.C:
		}
	}
}

func joinLines(l []string) string {
	out := ""
	for _, x := range l {
		out += x + "\n"
	}
	return out
}
