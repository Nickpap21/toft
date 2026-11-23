# Toft security audit state

Maintained by the weekly security-audit routine. Lives only on the orphan
branch `security-audit-state`; never merged.

## Last audited commit

- master: `892d93a2c0df9e39a8d2a46ef912a7869c8f4e42` (2026-11-23 run)
- The diff `61599a4..892d93a` (63 commits) is fully reviewed.
- Coverage is still **incomplete** (see map). Next run: finish the
  partial/none components at their current SHA first, then audit
  `892d93a..master`.

## Run log

| Date | Scope | Result |
| --- | --- | --- |
| 2026-11-16 | First run: full-repo pass (7 parallel source reviewers), govulncheck, npm audit (prod), history secret scan | 1 high (PR #24), 1 medium + 2 design items needs-decision, 2 low recorded |
| 2026-11-23 | Coverage continuation (internal/server, netplan, hostproc, wsconn, toft-agent, agentproto, sshpool, agent, org, instance gates, issue, doc, inbox, cmd/server, service, preview, rollout, code, gitstore, docker, scm, registry) + diff 61599a4..892d93a (6 parallel reviewers), govulncheck, npm audit (prod), secret scan of the 63 new commits, re-check of open findings | 3 medium + 1 low with PRs (#49-#52); SEC-001 and SEC-003 fixed upstream; 4 needs-decision, 3 low open-no-pr |

Notes from 2026-11-23:
- govulncheck: vuln.go.dev is still blocked; ran against an offline DB built from the OSV Go export (4820 GO- entries, modified up to 2026-11-22). Only hits: GO-2026-4887, GO-2026-4883 (docker/docker v28.5.2, daemon-side, no fix), both listed in backend/.govulnignore. Not applicable.
- npm audit --omit=dev (frontend): 0 vulnerabilities.
- Secret scan of 61599a4..892d93a: no keys, tokens or private keys; only struct fields and test fixtures (`hunter2`, `t0ken`). Nothing to rotate.
- CI workflows in the diff: `permissions: {}` per job, `persist-credentials: false`, event SHAs passed via env. Nixpacks binary in backend/Dockerfile is pinned by sha256. No issue.
- Baseline `go test ./...` on master here: only `internal/docker` TestProbeScript* (3 tests) fail (environment).
- Branch policy: a plain `git push -u origin security/<id>` works; compound shell commands that include the push are refused by the session permission policy (that was the cause of last run's note). PR #49 has head `claude/optimistic-bell-jwde3l` for that reason; #50-#52 use `security/*`.

## Coverage map

Status: full = every file reviewed; partial = reviewed with listed gaps; none = not reviewed. SHA = master commit reviewed.

| Component | Status | SHA | Gaps / notes |
| --- | --- | --- | --- |
| pkg/middleware, pkg/reauth, pkg/crypto, pkg/config | full | 61599a4 | unchanged in 892d93a diff except as noted |
| internal/auth, internal/user | full | 61599a4 | |
| internal/apikey | full | 892d93a | logs scope + keyLists reviewed |
| internal/org | full | 892d93a | |
| internal/instance | partial | 892d93a | every handler's operator gate checked; panel/upgrade/watchdog/kit internals not |
| internal/agent | full | 892d93a | oauth.go role/scope checks only |
| cmd/server (router, CORS, panelroutes, hashkey, networks, previews, domainprobe) | full | 892d93a | main.go wiring skimmed |
| internal/project, environment, envvar, domain, sharedvolume, configfile, resources, dashboard, observability, alert, reveal, disk, catalog | full | 61599a4 | project/handler.go diff 892d93a reviewed; envvar key.go crypto internals skimmed |
| internal/service | full | 892d93a | |
| internal/deployment | partial | 892d93a | diff reviewed; long tail of service.go pipeline not line-by-line |
| internal/preview | partial | 892d93a | handler, dockerfile, names, address full; service.go/api.go skimmed; reconcile internals |
| internal/composespec | full | 892d93a | |
| internal/deploypolicy | full | 61599a4 | repository/handler skimmed |
| internal/docker | partial | 892d93a | buildkit, probe, orchestrator/remote_orchestrator (diff), compose, labels, release, login full; health.go, disk.go, agentrollout.go not; stats attribution skimmed |
| internal/rollout | partial | 892d93a | create, names, stackroutes full; networks/run/dockerhost/routefile skimmed; drain.go, slots.go not |
| internal/code | partial | 892d93a | githttp, events, handler_checks full; checks/service/agents skimmed; pulls_postgres, repository_postgres, mirrors*, webhooks not |
| internal/gitstore | full | 892d93a | |
| cmd/toft-git, pkg/gitcred, pkg/netcheck | full | 892d93a | |
| pkg/scm, pkg/registry | full | 892d93a | |
| internal/mcp, internal/webhook, internal/events, pkg/broker | full | 892d93a | webhook diff reviewed |
| internal/issue | partial | 892d93a | repository_postgres.go: signatures + SQL of called queries only |
| internal/doc | full | 892d93a | |
| internal/inbox | partial | 892d93a | rest of repository.go beyond NeedsYou/notifications skimmed |
| internal/server | partial | 892d93a | provisioner, updates, updaterun, agent_view, repository_postgres, capacity, firewall.nft, toft-firewall.service, toft-backup.sh full; capabilities.go, gvisor.go, requirements.go, backup.go, shares.go, agent.go skimmed |
| internal/netplan, pkg/hostproc, pkg/wsconn | full | 892d93a | netplan/preflight.go skimmed |
| internal/agenthub, pkg/wspty, pkg/sshclient/hostkey.go | full | 61599a4 | |
| cmd/toft-agent | partial | 892d93a | compose, config, rollouts, upgrade, hostupdate, desired, logs, metrics full; dns.go, routes.go, server.go, session.go skimmed; join.go, pty_*.go not |
| pkg/agentproto, pkg/sshpool, pkg/sshclient (client.go) | partial | 892d93a | agentproto, sshpool/options.go full; pool.go mostly; handle.go skimmed; client.go Dial/wrap/reap/Upload + cancel diff |
| cmd/toft, pkg/apiclient | partial | 892d93a | main.go diff reviewed; answerip.go shell-outs only; apiclient generated |
| frontend/src | full | 892d93a | sinks by grep and traced; diff (settings-tab, api-keys) reviewed |
| install.sh, docker-compose*.yml, traefik/, Dockerfiles, .env.example, .github/workflows | full | 892d93a | |
| Makefile, scripts/ | partial | 892d93a | grep only; dockerfile-frontend.sh read |
| internal/metrics, hoststats, hostupdate, advisory, reseal, version | partial | 892d93a | reseal operator-only checked; hoststats.Cmd fixed script; others not reviewed |
| db/migrations, db/queries | partial | 892d93a | queries on traced paths, 0084/0085/premigrate/contract reviewed |
| docs/, bench/ | none | | static site and benchmark data, low priority |

## Findings

| ID | Severity | Title | Location | Status | First seen | Last checked |
| --- | --- | --- | --- | --- | --- | --- |
| SEC-001 | high | Compose top-level volume with local driver and a real filesystem type mounts host disks/dirs | backend/internal/composespec/check.go | fixed (merged by hand in b3b381a, 1.1.1, GHSA-762m-rmcx-c8h5; PR #24 closed) | 2026-11-16 | 2026-11-23 |
| SEC-002 | low | Project-scoped API key (docs_write) resolves `toft-issue` blocks across every project in the workspace | backend/db/queries/docs.sql:167-174, backend/internal/doc/service.go:414 | open-no-pr | 2026-11-16 | 2026-11-23 |
| SEC-003 | medium | Local and SSH-remote compose/pull ran `docker login` in the host's shared Docker config | backend/internal/docker/orchestrator.go, remote_orchestrator.go | fixed (6f9c666 / d616f41: per-deploy DOCKER_CONFIG) | 2026-11-16 | 2026-11-23 |
| SEC-004 | low | MakeRoom runs before the capacity/target checks and, on the panel host, stops every workspace's previews even when the deploy is then refused | backend/internal/deployment/deploy.go:122, backend/internal/deployment/preview.go:165 | open-no-pr | 2026-11-16 | 2026-11-23 |
| SEC-005 | n/a (design) | Any owner of a server's workspace can rejoin its agent and pin a new key; guest workspaces then deploy to the owner's machine | backend/internal/server/handler_agent.go:126-157 | needs-decision | 2026-11-16 | 2026-11-23 |
| SEC-006 | n/a (design) | A deploy-scoped project key or agent can set `image_tag` to any image, which then receives the service's secrets | backend/internal/deployment/deploy.go | needs-decision | 2026-11-16 | 2026-11-23 |
| SEC-007 | medium | Untrusted preview of a Compose stack bypasses the untrusted firewall rules (stack bridges aren't toft-u*), and its Dockerfiles skip RunFlags | backend/internal/preview/service.go:612, backend/internal/server/firewall.nft:28 | pr-open #49 | 2026-11-23 | 2026-11-23 |
| SEC-008 | low | noLinksOut walks an unresolved symlinked build folder, so links out under it aren't checked (panel-host Nixpacks, non-root backend only) | backend/internal/docker/orchestrator.go:1259 | pr-open #50 | 2026-11-23 | 2026-11-23 |
| SEC-009 | medium | Loki URL naming a host that resolves to 127.0.0.1 passes the lexical isLoopback check; the agent pushes the workspace's logs into another workspace's loopback Loki (D-148) | backend/internal/observability/service.go:107, backend/cmd/toft-agent/logs.go:120,416 | pr-open #51 (agent path) | 2026-11-23 | 2026-11-23 |
| SEC-010 | medium | Issue clone copies other projects' sub-tasks for a project-scoped key/agent; project-limited agent can clone into any project | backend/internal/issue/deep.go:494-516, backend/internal/issue/handler.go:476 | pr-open #52 | 2026-11-23 | 2026-11-23 |
| SEC-011 | low | Audit log resolves names (service/project/env/doc page) of other workspaces' objects from IDs an agent's refused MCP call records | backend/db/queries/audit.sql (ListAuditEntries joins), backend/internal/mcp/server.go:262 | needs-decision | 2026-11-23 | 2026-11-23 |
| SEC-012 | n/a (needs validation) | Host firewall is `table ip` only: a compose network with `enable_ipv6: true` may reach host ports / private v6 past to_host and the untrusted rules | backend/internal/server/firewall.nft:17, backend/internal/composespec/networks.go:65 | needs-decision | 2026-11-23 | 2026-11-23 |
| SEC-013 | n/a (residual of SEC-009) | Same loopback-by-name Loki URL on SSH/panel-host servers goes to Docker's Loki log driver (Slot.LokiURL), which #51 can't guard | backend/internal/observability/model.go:68, Slot.LokiURL | needs-decision | 2026-11-23 | 2026-11-23 |
| SEC-014 | n/a (narrow precondition) | Provision adopts any container publishing :80 whose image name contains "traefik" as paas-traefik and connects it to every app network | backend/internal/server/provisioner.go:499-518 | needs-decision | 2026-11-23 | 2026-11-23 |
| SEC-015 | low | A `logs`-only project key reads `/workspace/services` in full, incl. `git_repo_url` (may embed credentials) | backend/internal/apikey/scope.go:161, backend/internal/service/handler.go:1363 | open-no-pr | 2026-11-23 | 2026-11-23 |
| SEC-016 | low | RunFlags (untrusted-preview Dockerfile gate) is bypassable: comment/blank line in a continuation, `# escape=`, fake heredoc in LABEL, symlinked Dockerfile, >1 MiB | backend/internal/preview/dockerfile.go:19-68 | open-no-pr | 2026-11-23 | 2026-11-23 |
| SEC-017 | low | from_container returns for port 53 before the untrusted jump: untrusted previews reach TCP/UDP 53 on any private address | backend/internal/server/firewall.nft:26 | needs-decision | 2026-11-23 | 2026-11-23 |
| SEC-018 | low | Predictable preview hostnames can be claimed first by another workspace; the preview's insert is silently dropped (ON CONFLICT DO NOTHING) | backend/db/queries/previews.sql:302, backend/internal/preview/names.go | open-no-pr | 2026-11-23 | 2026-11-23 |

### Recommended fixes for needs-decision / open items

- SEC-002: pass the key's project (middleware.APIKeyScopeFrom; agents with !AllProjects likewise) into doc Service.Get/links and drop resolved issues from other projects, or add an optional project_id filter to ResolveDocIssues. Same for DocsReferencing and issue detail's parent/children/relations (issue Get, service.go:513-546).
- SEC-004: in MakeRoom, stop nothing unless free + what all candidates would free >= need; call it after targetServer, AuthorizeTarget and the capacity check.
- SEC-005: decide whether guest workspaces trust every owner of the host workspace; if not, make agent rejoin on a shared server operator-only or require the sharer.
- SEC-006: decide whether deploy scope may override images; if not, require services-write (or an Ask policy for agents) when image_tag differs.
- SEC-007 (alternative to #49 if Compose previews from agents are wanted): give every stack network, default included, a toft-u bridge name via an override (com.docker.network.bridge.name) and run the stack's build Dockerfiles through RunFlags; needs a Docker daemon to verify.
- SEC-011: add org predicates to each resolving join in ListAuditEntries (services via COALESCE(sp.org_id, s.scoped_org_id), projects, environments, doc pages, repositories), keeping servers resolvable for shares; or set the MCP call's resource only after a 2xx. Needs Postgres-backed tests to verify (none in the audit container).
- SEC-012: switch to `table inet toft` with IPv6 equivalents (fib local, fc00::/7, fe80::/10, IPv6 metadata) and/or refuse `enable_ipv6` in CheckAppNetworks. First confirm on a Docker 27+ host that enable_ipv6 without ipam gets a ULA subnet and that a container reaches the host's [::] sshd.
- SEC-013: resolve named Loki hosts on save and at each deploy and refuse loopback/unspecified results for Slot.LokiURL; or refuse named hosts for Loki on SSH/panel servers. DNS rebinding remains for the log driver; decide whether that is acceptable.
- SEC-014: drop the adoption branch (fail with "stop it first"), or adopt only a container with no `paas.*`/`com.docker.compose.project` labels; consider refusing host `ports:` publishing in composespec (no rule restricts it today).
- SEC-015: for a key without deploy, return only id/name/project/environment from /workspace/services; strip userinfo from git_repo_url in every API response.
- SEC-016: parse with BuildKit's dockerfile parser and walk instruction flags; refuse a symlinked or truncated Dockerfile.
- SEC-017: limit the port-53 exception to local addresses and 169.254.53.53, or run the untrusted jump first; check that untrusted previews still resolve on hosts whose resolver is a private address (e.g. VPC .2).
- SEC-018: check domains.Taken for each preview hostname and fall back to a suffixed name; surface the conflict instead of DO NOTHING.

### Leads checked and not raised

2026-11-16:
- Agent dials panel with InsecureSkipVerify when no panel domain (accepted D-106 risk).
- Owner-only SSRF/port probe via add-server host and test-connection.
- Blind GET SSRF from deploy-from-git clone on the control plane.
- Login rate-limiter map eviction under a large IPv6 flood (hardening).
- netcheck.IsPublic doesn't block 2002::/16 or ::/96.
- Observability loki_url/grafana_url returned raw to viewers.
- Traefik access log records ?token= on WebSocket handshakes.
- compose extra_hosts host-gateway / external_links unchecked.

2026-11-23:
- Clone URL scheme restriction (#43): ext::/fd::/file:// and `-`-prefixed URLs refused; protocol.allow=never; no submodule recursion.
- git_ref (#45): validated as branch or tag; leading `-`, `..`, `@{`, control chars refused.
- Worker command health checks run inside the app's own container; probe path must start with `/`.
- BuildKit cache: BUILDKIT_CACHE_MOUNT_NS per service, tenant BUILDKIT_* args dropped, frontend pinned by digest; no --allow passed (entitlements not verified against a daemon).
- Remote/panel Nixpacks sandbox argv, chown, jail: no escape found.
- SSH cancel/reaper marks: random, exact match; TOFT_EXEC reserved in composespec.
- Deploy key sees app startup/migrate output in deploy logs while live logs need `logs` scope (documentation question, not a boundary).
- Developers may delete an idle, unshared server (pinned by routes_roles_test: intended).
- Viewers can trigger SSH connections (requirements/test-connection/stats) that pin the host key on first use.
- Metadata drop covers 169.254/16 only (Alibaba 100.100.100.200 reachable from trusted containers) — hardening.
- Backup dir/env perms not re-applied to an existing /var/lib/toft/backup — hardening.
- Agent approvals: no self-approval, claims atomic, policies org-scoped; org role changes/last-owner guarded.
- toft-agent upgrade: signed manifest, pinned key, no downgrade; host update IDs regex-checked.
