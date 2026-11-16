# Toft security audit state

Maintained by the weekly security-audit routine. Lives only on the orphan
branch `security-audit-state`; never merged.

## Last audited commit

- master: `61599a4a8b9300098c74bced1908356e4c72f434` (2026-11-16 run, first run)
- Coverage is **incomplete** at this SHA (see map). Next run: finish the
  partial/unreviewed components at this SHA first, then audit
  `61599a4..master`.

## Run log

| Date | Scope | Result |
| --- | --- | --- |
| 2026-11-16 | First run: full-repo pass (7 parallel source reviewers), govulncheck, npm audit (prod), history secret scan | 1 high (PR #24), 1 medium + 2 design items needs-decision, 2 low recorded |

Notes from 2026-11-16:
- govulncheck: vuln.go.dev is blocked by the session network policy; ran against an offline DB built from the OSV Go export (osv-vulnerabilities.storage.googleapis.com/Go/all.zip). Only hits: GO-2026-4887 and GO-2026-4883 in github.com/docker/docker v28.5.2 (daemon-side AuthZ-plugin / plugin-privilege bugs, no fixed version). Toft uses the module as a client only: not applicable.
- npm audit --omit=dev (frontend): 0 vulnerabilities.
- Secret scan over all 48 commits on master: only test fixtures, UI placeholders and AWS's documented example key (AKIAIOSFODNN7EXAMPLE). Nothing to rotate.
- Baseline `go test ./...` on master in the audit container: `internal/docker` TestProbeScriptTimesTheRequestInMilliseconds fails (environment); TestRouteAccess and TestLogHub_SlowViewerIsCutOff failed once and passed on re-run (flaky).
- Branch policy: pushes to `security/*` branches were refused by the session's permission policy (pushes to `claude/optimistic-bell-x49nj2` and `security-audit-state` were allowed). PR #24 therefore has head `claude/optimistic-bell-x49nj2`. The two low findings got no PR this run (`open-no-pr`); next run should open them (batched) if `security/*` pushes are allowed.

## Coverage map

Status: full = every file reviewed; partial = reviewed with listed gaps; none = not reviewed. SHA = master commit reviewed.

| Component | Status | SHA | Gaps / notes |
| --- | --- | --- | --- |
| pkg/middleware, pkg/reauth, pkg/crypto, pkg/config | full | 61599a4 | |
| internal/auth, internal/user | full | 61599a4 | |
| internal/apikey | full | 61599a4 | ListByProject SQL skimmed |
| internal/org | partial | 61599a4 | audit-log list SQL, settings SQL, isolation counting |
| internal/instance | partial | 61599a4 | panel/upgrade/watchdog/kit logic checked only for operator gate; panel container logs (CheckContainer) |
| internal/agent | partial | 61599a4 | allowance.go, policy/approval repo SQL, ClaimRun/ClaimRollback/ValidateAccess SQL |
| cmd/server (router, CORS, panelroutes, hashkey) | partial | 61599a4 | networks.go, previews.go, domainprobe.go (operator-only), rest of main.go wiring |
| internal/project, environment, envvar, domain, sharedvolume, configfile, resources, dashboard, observability, alert, reveal, disk, catalog | full | 61599a4 | envvar key.go crypto internals skimmed |
| internal/service | partial | 61599a4 | admin_password.go, network.go, data.go skimmed |
| internal/deployment | partial | 61599a4 | handler/deploy/move/rollback/logs reviewed; long tail of service.go pipeline not line-by-line |
| internal/preview | partial | 61599a4 | reconcile internals, dockerfile.go |
| internal/composespec | full | 61599a4 | |
| internal/deploypolicy | full | 61599a4 | repository/handler skimmed |
| internal/docker | partial | 61599a4 | compose.go, labels, release, login paths reviewed; stats/health/probe/disk/agentrollout not |
| internal/rollout | partial | 61599a4 | create.go full; networks/names/stackroutes skimmed; drain/migrate/run not |
| internal/code | partial | 61599a4 | events.go, checks.go skimmed; *_postgres via scoping SQL only |
| internal/gitstore | partial | 61599a4 | backup.go restore skimmed |
| cmd/toft-git, pkg/gitcred, pkg/netcheck | full | 61599a4 | |
| pkg/scm, pkg/registry | partial | 61599a4 | provider/tag parsing |
| internal/mcp, internal/webhook, internal/events, pkg/broker | full | 61599a4 | |
| internal/issue | partial | 61599a4 | cycles.go, planning.go, mover.go, most of repository_postgres.go |
| internal/doc | partial | 61599a4 | Purge, ServiceStates SQL |
| internal/inbox | partial | 61599a4 | repository.go / NeedsYou queries |
| internal/server | partial | 61599a4 | provisioner.go after line 250 (only command construction), updates/updaterun/gvisor/capacity/capabilities/requirements (command construction only), firewall.nft, toft-backup.sh, agent_view.go, repository_postgres.go |
| internal/agenthub, pkg/wspty, pkg/sshclient/hostkey.go | full | 61599a4 | |
| cmd/toft-agent | partial | 61599a4 | compose.go, logs.go, metrics.go, rollouts.go, upgrade.go, desired.go, hostupdate.go, config.go not reviewed |
| pkg/agentproto, pkg/sshclient (client.go), pkg/sshpool | partial | 61599a4 | |
| pkg/wsconn, internal/netplan, pkg/hostproc | none | | |
| cmd/toft, pkg/apiclient | partial | 61599a4 | answerip.go shell-outs only; apiclient generated, skimmed |
| frontend/src | full | 61599a4 | sinks found by grep and traced; not every component read |
| install.sh, docker-compose*.yml, traefik/, Dockerfiles, .env.example, .github/workflows | full | 61599a4 | |
| Makefile, scripts/ | partial | 61599a4 | grep only |
| internal/metrics, hoststats, hostupdate, advisory, reseal, version, events | partial | 61599a4 | no HTTP routes; internals not reviewed |
| db/migrations, db/queries | partial | 61599a4 | queries on traced paths only |
| docs/, bench/ | none | | static site and benchmark data, low priority |

## Findings

| ID | Severity | Title | Location | Status | First seen | Last checked |
| --- | --- | --- | --- | --- | --- | --- |
| SEC-001 | high | Compose top-level volume with local driver and a real filesystem type (ext4/xfs/overlay...) mounts host disks/dirs; policy only checked bind-style driver_opts | backend/internal/composespec/check.go:417-426 | pr-open #24 | 2026-11-16 | 2026-11-16 |
| SEC-002 | low | Project-scoped API key (docs_write) resolves `toft-issue` blocks across every project in the workspace (title/status/assignee leak) | backend/db/queries/docs.sql:167-174, backend/internal/doc/service.go:414 | open-no-pr | 2026-11-16 | 2026-11-16 |
| SEC-003 | medium | Local and SSH-remote compose/pull run `docker login` in the host's shared Docker config; concurrent deploys of other workspaces on the same host pull with those credentials (and a logout can break them) | backend/internal/docker/orchestrator.go:407-417, backend/internal/docker/remote_orchestrator.go:313-331,430-449,558 | needs-decision | 2026-11-16 | 2026-11-16 |
| SEC-004 | low | MakeRoom runs before the capacity/target checks and, on the panel host, stops every workspace's previews even when the deploy is then refused | backend/internal/deployment/deploy.go:112, backend/internal/deployment/preview.go:165-213 | open-no-pr | 2026-11-16 | 2026-11-16 |
| SEC-005 | n/a (design) | Any owner of a server's workspace can rejoin its agent (password reauth) and pin a new key; guest workspaces sharing that server then deploy (with secrets) to the owner's machine | backend/internal/server/handler_agent.go:126-157, backend/internal/server/agent.go:637 | needs-decision | 2026-11-16 | 2026-11-16 |
| SEC-006 | n/a (design) | A deploy-scoped project key or agent can set `image_tag` to any image, which then receives the service's decrypted secrets | backend/internal/deployment/deploy.go (UpdateDockerImage path) | needs-decision | 2026-11-16 | 2026-11-16 |

### Recommended fixes for needs-decision / open items

- SEC-002: pass the key's project (middleware.APIKeyScopeFrom; agents with !AllProjects likewise) into doc Service.Get/links and drop resolved issues from other projects, or add an optional project_id filter to ResolveDocIssues.
- SEC-003: give each deploy its own DOCKER_CONFIG holding only its registry auth, as toft-agent already does (cmd/toft-agent/compose.go:220 dockerConfig, including the cli-plugins symlink), for LocalOrchestrator.composeUp and RemoteOrchestrator pull/composeUp; drop the shared login/logout. Needs a Docker daemon to verify compose still finds its plugin.
- SEC-004: in MakeRoom, stop nothing unless free + what all candidates would free >= need; call it after targetServer, AuthorizeTarget and the capacity check.
- SEC-005: decide whether guest workspaces trust every owner of the host workspace; if not, make agent rejoin on a shared server operator-only (like the SSH host-key reset, ssh.go:162) or require the sharer.
- SEC-006: decide whether deploy scope should allow arbitrary image overrides; if not, require the variables/services-write scope (or an Ask policy for agents) when image_tag differs from the service's image.

### Leads checked and not raised (2026-11-16)

- Agent dials panel with InsecureSkipVerify when no panel domain (accepted D-106 risk); on-path attacker can make it exit via a forged agent_revoked 403.
- Owner-only SSRF/port probe via add-server host and test-connection (owner role, invite-only).
- Blind GET SSRF from deploy-from-git clone on the control plane (no netcheck on git clone); impact depends on whether tenant containers already reach the same networks.
- Login rate-limiter map eviction under a large IPv6 flood (hardening).
- netcheck.IsPublic doesn't block 2002::/16 or ::/96 (host-routing dependent).
- Observability loki_url/grafana_url returned raw to viewers (credentials only if owners embed them).
- Traefik access log records ?token= on WebSocket handshakes (readable by operators/root only).
- compose extra_hosts host-gateway / external_links unchecked (host firewall limits container→host to 80/443/53).
