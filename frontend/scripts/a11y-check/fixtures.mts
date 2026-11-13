// What the accessibility gate's fake API answers (./index.mjs). Typed against
// the OpenAPI contract, so npm run typecheck fails when a shape drifts from it.
// The rows are the ones a page needs to show its hard cases: Needs you items,
// a current environment, domains waiting for DNS, a diff with line numbers, a
// merge in the pipelines, timeline bars, a docs checklist, tables wide enough to
// scroll on a phone.
import type { components, paths } from "../../src/lib/api/schema";

type S = components["schemas"];
type Json200<P extends keyof paths> = paths[P] extends { get: { responses: { 200: { content: { "application/json": infer B } } } } } ? B : never;
type Fulfil = { status: number; contentType: string; body: string };
type Route = { path: string; reply: (url: URL, theme: string) => unknown; stream?: string };

const routes: Route[] = [];
/** One GET, typed by its path in the contract. */
function get<P extends keyof paths>(path: P, body: Json200<P> | ((url: URL, theme: string) => Json200<P>)) {
  routes.push({ path, reply: typeof body === "function" ? (body as Route["reply"]) : () => body });
}

/** A log stream, answered in one piece: fulfilling a request can't hold it open. */
function stream(path: keyof paths, body: string) {
  routes.push({ path, reply: () => undefined, stream: body });
}

const id = (n: number) => `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`;
const ago = (hours: number) => new Date(Date.now() - hours * 3600_000).toISOString();
const ahead = (hours: number) => new Date(Date.now() + hours * 3600_000).toISOString();
const day = (days: number) => new Date(Date.now() + days * 86400_000).toISOString().slice(0, 10);
const sha = (n: number) => `${n}f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e3b5d7f9a`.slice(0, 40);

export const PROJECT = id(1);
export const ENV = id(11);
const STAGING = id(12);
const PREVIEW_ENV = id(13);
export const SERVICE = id(21);
const DB_SERVICE = id(22);
const WORKER = id(23);
const SHARED_SERVICE = id(24);
export const SERVER = id(31);
const SERVER_2 = id(32);
const DEPLOY = id(41);
const DEPLOY_FAILED = id(42);
export const DEPLOYMENT = DEPLOY;
export const REPO = "storefront";
export const REPO_ID = id(51);
export const DOC = id(61);
const DOC_2 = id(62);
export const REQUEST = id(71);
const ME = id(81);
const SAM = id(82);
const ORG = id(91);
const OTHER_ORG = id(92);
export const ISSUE = 118;
export const PULL = 47;
export const SHA = sha(1);

/** The GETs a page's session needs; pass 2 leaves them answering. */
export const SESSION_PATHS = ["/api/v1/me", "/api/v1/org", "/api/v1/org/list"];

export const signedInUser = {
  id: ME,
  email: "alex@acme.test",
  name: "Alex Moreno",
  is_operator: true,
  orgId: ORG,
  orgName: "Acme",
  role: "owner",
  orgs: [{ org_id: ORG, name: "Acme", role: "owner" }],
  noWorkspace: false,
};

const alex: S["Person"] = { id: ME, name: "Alex Moreno" };
const sam: S["Person"] = { id: SAM, name: "Sam Okafor" };
const claude: S["Person"] = { id: SAM, name: "Sam Okafor", agent: "Claude" };
const typeOf = (name: string, icon: S["IssueTypeRef"]["icon"], color: S["IssueTypeRef"]["color"], n: number): S["IssueTypeRef"] => ({ id: id(100 + n), name, kind: "standard", icon, color });
const bug = typeOf("Bug", "bug", "red", 1);
const feature = typeOf("Feature", "feature", "blue", 2);
const task = typeOf("Task", "task", "gray", 3);
const epic: S["IssueTypeRef"] = { ...typeOf("Epic", "epic", "violet", 4), kind: "container" };

const project: S["Project"] = { id: PROJECT, org_id: ORG, user_id: ME, name: "Storefront", description: "The shop and its checkout", icon: "", issue_key: "STO", open_issues: 12, open_pulls: 3, doc_pages: 4, created_at: ago(2000), updated_at: ago(4) };
const project2: S["Project"] = { ...project, id: id(2), name: "Internal tools", description: "", issue_key: "INT", open_issues: 1, open_pulls: 0, doc_pages: 0 };

const environment: S["Environment"] = { id: ENV, project_id: PROJECT, name: "production", color: "amber", protected: true, created_at: ago(2000), updated_at: ago(24) };
const staging: S["Environment"] = { ...environment, id: STAGING, name: "staging", color: "blue", protected: false };
const preview: S["Environment"] = { ...environment, id: PREVIEW_ENV, name: "pr-47", color: "gray", protected: false, preview: { pull_number: PULL, repository_id: REPO_ID } };

const health: S["HealthAnswer"] = { status: 200, ms: 12, healthy: true, at: ago(0.05), since: ago(30) };
const service: S["Service"] = {
  id: SERVICE, scope: "project", project_id: PROJECT, environment_id: ENV, server_id: SERVER, name: "web", type: "web", source_type: "git",
  git_repo_url: "https://git.acme.test/acme/storefront.git", git_branch: "main", repository_id: REPO_ID, deploy_on_push: true, base_directory: "", build_pack: "dockerfile",
  port: 3000, replicas: 1, cpu_limit: "1", memory_limit: "512m", runtime_status: "running", active_deployment_id: DEPLOY, live_since: ago(30), compose_route_service: "",
  deploy_strategy: "rolling", effective_deploy_strategy: "rolling", pids_limit: 1024, deploy_timeout_minutes: 15, rollback_images_kept: 3, isolation: "standard", effective_isolation: "standard",
  isolation_advice: { level: "standard", reason: "app" }, kind: "app", has_registry_password: false, has_git_token: false, health_check_path: "/health", health_check_timeout: 5, health_check_retries: 3,
  required_env_vars: "DATABASE_URL", migrate_command: "", volumes: [], created_at: ago(2000), updated_at: ago(30), last_deploy_failed: false, last_health_check: health,
};
const dbService: S["Service"] = { ...service, id: DB_SERVICE, name: "postgres", type: "worker", source_type: "docker-image", docker_image: "postgres:16.4-bookworm@sha256:3c1f9a5b7e2d4c6a8f0e1b3d5c7a9e2f4b6d8a0c1e3f5a7b9d1c3e5f7a9b1d3c", git_repo_url: undefined, git_branch: undefined, repository_id: undefined, addon_slug: "postgres", kind: "database", network_alias: "postgres", port: 5432, connect_to: { host: "postgres", port: 5432 }, last_health_check: null, active_deployment_id: undefined };
const worker: S["Service"] = { ...service, id: WORKER, name: "orders-worker", type: "worker", runtime_status: "failed", last_deploy_failed: true, active_deployment_id: undefined, live_since: null };
const shared: S["Service"] = { ...service, id: SHARED_SERVICE, name: "grafana", scope: "shared", project_id: "", environment_id: undefined, source_type: "docker-image", docker_image: "grafana/grafana:11.2.0", addon_slug: "grafana", kind: "tool" };
const services = [service, dbService, worker];

const workspaceService = (s: S["Service"]): S["WorkspaceService"] => ({ id: s.id, name: s.name, scope: s.scope, project_id: s.project_id, environment_id: s.environment_id ?? "", server_id: s.server_id ?? "", port: s.port, runtime_status: s.runtime_status, source_type: s.source_type, addon_slug: s.addon_slug ?? "", docker_image: s.docker_image ?? "", git_repo_url: s.git_repo_url ?? "", git_branch: s.git_branch ?? "", environment_name: "production" });

const deployment: S["Deployment"] = { id: DEPLOY, service_id: SERVICE, status: "running", image_tag: "storefront-web:1f3a9c1", commit_sha: sha(1), commit_message: "Keep the cart when a session expires", triggered_by: "alex@acme.test", pull_request: { number: PULL, repository_id: REPO_ID, issues: [{ key: "STO-118", number: ISSUE, title: "Carts vanish when a session expires", status: "shipped" }] }, started_at: ago(30.1), finished_at: ago(30), created_at: ago(30.2) };
const failedDeployment: S["Deployment"] = { id: DEPLOY_FAILED, service_id: SERVICE, status: "failed", image_tag: "storefront-web:9e8d7c6", commit_sha: sha(2), commit_message: "Bump the checkout SDK", triggered_by: "sam@acme.test", started_at: ago(3.1), finished_at: ago(3), error_msg: "the health check on /health never answered: connection refused", created_at: ago(3.2) };
const lastDeployment: S["LastDeployment"] = { id: DEPLOY, status: "running", commit_sha: sha(1), image_tag: "storefront-web:1f3a9c1", created_at: ago(30.2), finished_at: ago(30) };

const server: S["ServerResponse"] = {
  id: SERVER, org_id: ORG, user_id: ME, name: "eu-central-1", ip_address: "203.0.113.10", user: "root", port: 22, status: "active", provisioned_at: ago(2000), total_cpu_cores: 8, total_memory_mb: 32768,
  host_key_type: "ssh-ed25519", host_key_fingerprint: "SHA256:9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4", host_key_pinned_at: ago(2000), created_at: ago(2000), updated_at: ago(2), connection: "ssh", agent: null, ssh_key_stored: true, app_networks: "10.20.0.0/16", address_pool: "10.20.0.0/16",
};
const agentServer: S["ServerResponse"] = {
  ...server, id: SERVER_2, name: "eu-west-2", ip_address: "203.0.113.11", connection: "agent", ssh_key_stored: false,
  agent: { status: "connected", since: ago(300), last_seen_at: ago(0.01), version: "0.9.4", arch: "arm64", key_fingerprint: "SHA256:AAAA0000bbbb1111cccc2222dddd3333eeee4444ffff", key_pinned_at: ago(300), from_ip: "203.0.113.11", rtt_ms: 18, token_expires_at: null, panel_key_fingerprint: "SHA256:1111aaaa2222bbbb3333cccc4444dddd5555eeee6666", desired: null, routes: 4, frozen: null, rejoin: false },
};

const checksPassed: S["CheckSummary"] = { state: "success", total: 3, passed: 3, failed: 0, pending: 0 };
const checksFailed: S["CheckSummary"] = { state: "failure", total: 3, passed: 2, failed: 1, pending: 0 };
const commit: S["GitCommit"] = { sha: sha(1), parents: [sha(2)], author_name: "Alex Moreno", author_email: "alex@acme.test", authored_at: ago(30), committed_at: ago(30), subject: "Keep the cart when a session expires", checks: checksPassed };
const commit2: S["GitCommit"] = { ...commit, sha: sha(2), parents: [sha(3)], author_name: "Sam Okafor", author_email: "sam@acme.test", authored_at: ago(70), committed_at: ago(70), subject: "Bump the checkout SDK", checks: checksFailed };
const checks: S["CommitCheck"][] = [
  { name: "ci/lint", state: "success", description: "No problems", details_url: "https://ci.acme.test/runs/812", deployment: null, reporter: alex, started_at: ago(31), completed_at: ago(30.9), created_at: ago(31), updated_at: ago(30.9) },
  { name: "ci/test", state: "failure", description: "2 tests failed in cart_test.go", details_url: "https://ci.acme.test/runs/813", deployment: null, reporter: alex, started_at: ago(31), completed_at: ago(30.8), created_at: ago(31), updated_at: ago(30.8) },
  { name: "deploy/web", state: "success", description: "Deployed to production", details_url: null, deployment: { id: DEPLOY, service_id: SERVICE }, reporter: alex, started_at: ago(30.2), completed_at: ago(30), created_at: ago(30.2), updated_at: ago(30) },
];
const commitChecks: S["CommitChecks"] = { sha: sha(1), summary: checksFailed, checks };

const mirrorNone = null as unknown as S["Mirror"];
const repository: S["Repository"] = { id: REPO_ID, name: REPO, full_name: "acme/storefront", ssh_url: "ssh://git@git.acme.test:2222/acme/storefront.git", https_url: "https://git.acme.test/acme/storefront.git", created_at: ago(2000), protect_default_branch: true, open_pulls: 3, pull_mirror: mirrorNone };
const deployTarget: S["DeployTarget"] = { service_id: SERVICE, service_name: "web", environment_name: "production", environment_color: "amber", server_name: "eu-central-1", branch: "main", base_directory: "", deploy_on_push: true, files_changed: 3 };
const repositoryDetail: S["RepositoryDetail"] = { ...repository, default_branch: "main", protected_branch: "main", empty: false, branch_count: 3, tag_count: 2, pulls: { open: 3, merged: 41, closed: 2 }, deploys: [deployTarget] };

const compare: S["GitCompare"] = {
  base: { name: "main", kind: "branch", sha: sha(2) }, head: { name: "fix/cart-expiry", kind: "branch", sha: sha(1) }, merge_base: sha(2), ahead: 2, behind: 0, mergeable: true, conflicts: [], fast_forward: true,
  commits: [commit], commits_truncated: false, stats: { files: 2, additions: 14, deletions: 3 },
  files: [{ path: "internal/cart/session.go", status: "modified", additions: 12, deletions: 3, binary: false }, { path: "internal/cart/session_test.go", status: "added", additions: 2, deletions: 0, binary: false }], files_truncated: false,
};
const pullBase: S["PullRequest"] = { number: PULL, title: "Keep the cart when a session expires", body: "Carts were dropped with the session.", html: "<p>Carts were dropped with the session.</p>", state: "open", author: alex, base_branch: "main", head_branch: "fix/cart-expiry", head_sha: sha(1), merged_by: alex, merged_at: null, closed_at: null, created_at: ago(40), updated_at: ago(3), comments: 2, ahead: 2, behind: 0, conflicts: false, checks: checksFailed };
const pullDetail: S["PullRequestDetail"] = { ...pullBase, head_exists: true, head_kept: false, compare, head_checks: commitChecks, issues: [{ key: "STO-118", number: ISSUE, title: "Carts vanish when a session expires", status: "in_review" }], docs: [{ id: DOC, title: "Checkout runbook", project_id: PROJECT }], deploys: [deployTarget] };
const patch = `@@ -118,7 +118,8 @@ func (s *Session) Expire(ctx context.Context) error {
 	if s.Cart == nil {
 		return nil
 	}
-	return s.store.Delete(ctx, s.ID)
+	// The cart outlives the session.
+	return s.store.Detach(ctx, s.ID)
 }

 func (s *Session) Renew(ctx context.Context) error {
`;
const fileDiff: S["GitFileDiff"] = { path: "internal/cart/session.go", status: "modified", additions: 2, deletions: 1, binary: false, patch, patch_truncated: false };
const tree: S["GitTree"] = {
  ref: { name: "main", kind: "branch", sha: sha(1) }, path: "", last_commit: commit, commit_count: 214, truncated: false,
  entries: [
    { name: "internal", path: "internal", kind: "dir", size: null, last_commit: commit },
    { name: "Dockerfile", path: "Dockerfile", kind: "file", size: 812, last_commit: commit2 },
    { name: "README.md", path: "README.md", kind: "file", size: 2048, last_commit: commit2 },
  ],
  readme: { path: "README.md", html: "<h1>Storefront</h1><p>The shop and its checkout.</p>" },
};
const blobText = Array.from({ length: 24 }, (_, i) => `// line ${i + 1} of the session store`).join("\n");
const blob: S["GitBlob"] = { ref: { name: "main", kind: "branch", sha: sha(1) }, path: "internal/cart/session.go", kind: "file", size: blobText.length, binary: false, truncated: false, text: blobText, last_commit: commit };

const pipeline: S["Pipeline"] = { id: 3, kind: "push", branch: "fix/cart-expiry", before: sha(2), after: sha(1), subject: "Keep the cart when a session expires", commits: 2, forced: false, actor: alex, pull_number: null, created_at: ago(30), dispatched_at: ago(30), error: null, checks: commitChecks };
const pipelines: S["Pipeline"][] = [pipeline, { ...pipeline, id: 2, kind: "merge", branch: "main", subject: "Bump the checkout SDK", pull_number: PULL, actor: sam, created_at: ago(70), checks: { ...commitChecks, summary: checksPassed } }, { ...pipeline, id: 1, kind: "mirror", branch: "main", subject: "", commits: 5, actor: sam, created_at: ago(120), checks: { ...commitChecks, summary: { state: "success", total: 0, passed: 0, failed: 0, pending: 0 }, checks: [] } }];

const issueBase: S["Issue"] = {
  cycle: { number: 14, name: "Cycle 14", starts_on: day(-6), ends_on: day(8), state: "current" }, start_on: day(-3), due_on: day(1), estimate: 3, type: bug, parent: null as unknown as S["IssueParentRef"], blocked_by: [], progress: { total: 0, shipped: 0, done: 0 },
  key: "STO-118", number: ISSUE, title: "Carts vanish when a session expires", status: "in_progress", priority: "high", assignee: alex, labels: ["checkout", "web"], created_at: ago(200), updated_at: ago(3),
  work: { kind: "pull_request", number: PULL, state: "open", repository_id: REPO_ID, sha: sha(1), checks: checksFailed, deploy: "building" },
};
const issues: S["Issue"][] = [
  issueBase,
  { ...issueBase, key: "STO-119", number: 119, title: "Webhook retries double-charge a refund", status: "todo", priority: "urgent", type: bug, assignee: sam, due_on: day(4), work: null as unknown as S["IssueWork"] },
  { ...issueBase, key: "STO-120", number: 120, title: "Saved addresses at checkout", status: "in_review", priority: "medium", type: feature, assignee: claude, labels: ["checkout"], estimate: 5, due_on: null, work: { kind: "branch", branch: "feat/addresses" } },
  { ...issueBase, key: "STO-121", number: 121, title: "Rewrite the payments epic", status: "backlog", priority: "none", type: epic, assignee: sam, labels: [], cycle: null as unknown as S["CycleRef"], start_on: null, due_on: null, estimate: null, progress: { total: 4, shipped: 1, done: 2 }, work: null as unknown as S["IssueWork"] },
  { ...issueBase, key: "STO-122", number: 122, title: "Faster product search", status: "shipped", priority: "low", type: task, assignee: alex, labels: ["web"], work: null as unknown as S["IssueWork"] },
];
const typeRows: S["IssueType"][] = [bug, feature, task, epic].map((t, position) => ({ ...t, position, archived: false }));
const related: S["IssueRelated"] = { key: "STO-119", number: 119, title: "Webhook retries double-charge a refund", status: "todo", type: bug, project_id: PROJECT, assignee: sam };
const issueDetail: S["IssueDetail"] = {
  ...issueBase, carried_from: 13, cycles: [13, 14], docs: [{ id: DOC, title: "Checkout runbook", project_id: PROJECT }], relations: [{ id: id(201), kind: "blocks", issue: related }], children: [related],
  description: "Carts are dropped when the session expires.", html: "<p>Carts are dropped when the session expires.</p><ul><li>Reproduce with a 1-minute session</li></ul>",
  created_by: sam, status_changed_at: ago(3), comments: 1,
  links: [{ kind: "pull_request", repository: REPO, repository_id: REPO_ID, number: PULL, title: "Keep the cart when a session expires", state: "open", head_branch: "fix/cart-expiry", base_branch: "main", checks: checksFailed, deploys: [{ service_id: SERVICE, service: "web", environment: "production", state: "building", sha: sha(1), deployment_id: DEPLOY }] }, { kind: "branch", repository: REPO, repository_id: REPO_ID, branch: "fix/cart-expiry", exists: true }],
};
const issueTimeline: S["IssueTimelineItem"][] = [
  { type: "event", id: id(211), actor: sam, created_at: ago(200), edited_at: null, kind: "opened" },
  { type: "event", id: id(212), actor: alex, created_at: ago(30), edited_at: null, kind: "status", from: "todo", to: "in_progress" },
  { type: "comment", id: id(213), actor: claude, created_at: ago(3), edited_at: null, body: "Traced it to the expiry job.", html: "<p>Traced it to the expiry job.</p>" },
];

const cycleCounts: S["CycleCounts"] = { scope: 18, started: 6, shipped: 7, cancelled: 1, carried: 2, points: 40, started_points: 12, shipped_points: 16 };
const cycle: S["Cycle"] = { number: 14, name: "Cycle 14", starts_on: day(-6), ends_on: day(8), state: "current", days_left: 8, counts: cycleCounts };
const cycleOld: S["Cycle"] = { ...cycle, number: 13, name: "Cycle 13", starts_on: day(-20), ends_on: day(-7), state: "ended", days_left: 0 };
const cycleNext: S["Cycle"] = { ...cycle, number: 15, name: "Cycle 15", starts_on: day(8), ends_on: day(22), state: "upcoming", days_left: 22, counts: { ...cycleCounts, scope: 2, started: 0, shipped: 0, cancelled: 0, carried: 0, points: 8, started_points: 0, shipped_points: 0 } };
const cycleDetail: S["CycleDetail"] = {
  ...cycle, added: 2, carried_in: 3, previous: 13, next: 15,
  days: Array.from({ length: 7 }, (_, i) => ({ day: day(i - 6), scope: 16 + (i > 3 ? 2 : 0), started: 2 + i, shipped: i, scope_points: 36, started_points: 4 + i, shipped_points: i * 2 })),
  projects: [{ id: PROJECT, name: "Storefront", total: 15, shipped: 6 }, { id: id(2), name: "Internal tools", total: 3, shipped: 1 }],
  assignees: [{ id: ME, name: "Alex Moreno", total: 10, shipped: 4 }, { id: SAM, name: "Sam Okafor", total: 8, shipped: 3 }],
};
const cycleIssue = (i: S["Issue"], carried: number | null): S["CycleIssue"] => ({ key: i.key, number: i.number, title: i.title, status: i.status, priority: i.priority, project_id: PROJECT, project: "Storefront", type: i.type, assignee: i.assignee, labels: i.labels, estimate: i.estimate, due_on: i.due_on, carried_from: carried, carried_to: null });

const timelineEntry = (i: S["Issue"], start: number, due: number): S["IssueTimelineEntry"] => ({ key: i.key, number: i.number, title: i.title, status: i.status, priority: i.priority, type: i.type, parent: undefined, start_on: day(start), due_on: day(due), started_at: ago(-start * 24), shipped_at: i.status === "shipped" ? ago(2) : null, assignee: i.assignee, labels: i.labels, estimate: i.estimate, progress: i.progress });

const docTree: S["DocTreePage"][] = [
  { id: DOC, parent_id: null, title: "Checkout runbook", position: 0, stale: false, runbook: true, updated_at: ago(24) },
  { id: DOC_2, parent_id: DOC, title: "Rolling back a release", position: 0, stale: true, runbook: false, updated_at: ago(900) },
];
const docHTML = `<h2 id="before-you-start">Before you start</h2><p>Open <a href="https://status.acme.test">the status page</a> and check the queue.</p><ul><li><input checked="" disabled="" type="checkbox"> Tell support the window</li><li><input disabled="" type="checkbox"> Drain the checkout workers</li></ul><h2 id="steps">Steps</h2><pre><code class="language-toft-service">web</code></pre><pre><code class="language-sh">make release
</code></pre>`;
const docPage: S["DocPage"] = {
  id: DOC, project_id: PROJECT, parent_id: null, title: "Checkout runbook", body: "## Before you start", html: docHTML, version: 7, position: 0, created_by: alex, updated_by: sam, created_at: ago(900), updated_at: ago(24), verified_at: ago(48), verified_by: alex, deleted_at: null,
  path: [{ id: DOC, title: "Checkout runbook", project_id: PROJECT }],
  blocks: [{ kind: "service", service_id: SERVICE }],
  services: [{ id: SERVICE, name: "web", environment: "production", status: "running", server: "eu-central-1", image: "storefront-web:1f3a9c1", commit: sha(1), deployed_at: ago(30), secret_set: null, changes: [], deployment: { id: DEPLOY, commit: sha(1), finished_at: ago(30) } }],
  stale: false, referenced_by: [{ id: DOC_2, title: "Rolling back a release", project_id: PROJECT }],
};

const needsYou: S["NeedsYouItem"][] = [
  { key: "deploy_failed:1", snoozes: false, severity: "problem", kind: "deploy_failed", category: "deploys", title: "orders-worker failed to deploy", detail: "production · eu-central-1", error: "the health check on /health never answered", at: ago(3), project_id: PROJECT, project: "Storefront", service_id: WORKER, deployment_id: DEPLOY_FAILED },
  { key: "agent_approval:1", snoozes: false, severity: "request", kind: "agent_approval", category: "deploys", title: "Claude wants to deploy web", detail: "production", at: ago(0.5), project_id: PROJECT, project: "Storefront", service_id: SERVICE, approval_id: REQUEST, expires_at: ahead(0.5) },
  { key: "assigned:1", snoozes: true, severity: "waiting", kind: "assigned", category: "issues", title: "Carts vanish when a session expires", detail: "Assigned to you", at: ago(20), project_id: PROJECT, project: "Storefront", issue: { key: "STO-118", number: ISSUE, title: "Carts vanish when a session expires", status: "in_progress", priority: "high" } },
  { key: "stale_page:1", snoozes: true, severity: "warning", kind: "stale_page", category: "docs", title: "Rolling back a release", detail: "web changed since this page was verified", at: ago(50), project_id: PROJECT, project: "Storefront", page_id: DOC_2 },
  { key: "no_backup:1", snoozes: true, severity: "warning", kind: "no_backup", category: "servers", title: "eu-west-2 has no backup", detail: "Its databases aren't backed up", at: ago(90), project_id: null, project: "", server_id: SERVER_2 },
];
const inboxUpdates: S["InboxUpdate"][] = [
  { id: id(301), kind: "issue_comment", category: "issues", actor: claude, project_id: PROJECT, project: "Storefront", issue: { key: "STO-118", number: ISSUE, title: "Carts vanish when a session expires", status: "in_progress" }, detail: { excerpt: "Traced it to the expiry job." }, created_at: ago(3), read: false, done: false },
  { id: id(302), kind: "pull_merged", category: "code", actor: sam, project_id: PROJECT, project: "Storefront", pull: { repository: REPO, number: 46, title: "Bump the checkout SDK", state: "merged" }, detail: {}, created_at: ago(26), read: true, done: false },
  { id: id(303), kind: "page_edited", category: "docs", actor: alex, project_id: PROJECT, project: "Storefront", page: { id: DOC, title: "Checkout runbook" }, detail: {}, created_at: ago(30), read: true, done: false },
];
const myIssues: S["MyIssue"][] = issues.map((i) => ({ key: i.key, number: i.number, title: i.title, status: i.status, priority: i.priority, project_id: PROJECT, project: "Storefront", type: i.type, updated_at: i.updated_at, due_on: i.due_on, cycle: 14 }));

const failedDeploy: S["FailedDeploy"] = { id: DEPLOY_FAILED, service_id: WORKER, service_name: "orders-worker", project_id: PROJECT, project_name: "Storefront", environment_name: "production", server_name: "eu-central-1", error_summary: "the health check on /health never answered", commit_sha: sha(2), failed_at: ago(3) };
const dashboard: S["DashboardData"] = {
  work: { pulls_open: 3, pulls_yours: 1, issues_in_progress: 6, issues_yours: 2, issues_shipped_week: 9, cycle_number: 14, issues_shipped_cycle: 7 },
  activity: [
    { kind: "pull_merged", at: ago(2), actor: "Sam Okafor", project_id: PROJECT, project: "Storefront", title: "Bump the checkout SDK", number: 46, repository: REPO },
    { kind: "issue_shipped", at: ago(5), actor: "Alex Moreno", project_id: PROJECT, project: "Storefront", title: "Faster product search", key: "STO-122", number: 122 },
    { kind: "page_edited", at: ago(24), actor: "Sam Okafor", project_id: PROJECT, project: "Storefront", title: "Checkout runbook", page_id: DOC },
  ],
  deploys_24h: { total: 14, succeeded: 12, failed: 2, median_seconds: 96 },
  failed_deploys: [failedDeploy],
  open_alerts: [{ id: id(311), kind: "disk_full", subject: SERVER, subject_name: "eu-central-1", detail: "Disk at 91%", project_id: "", project_name: "", environment_name: "", fired_at: ago(1) }],
  deploying: [{ id: id(43), status: "building", service_id: SERVICE, service_name: "web", source_type: "git", has_compose_repo: false, project_id: PROJECT, project_name: "Storefront", environment_name: "staging", server_name: "eu-west-2", commit_sha: sha(3), created_at: ago(0.05) }],
  project_deploys: [{ project_id: PROJECT, status: "running", service_name: "web", source_type: "git", git_branch: "main", docker_image: "", created_at: ago(30) }],
  recent_deployments: [{ id: DEPLOY, status: "running", service_id: SERVICE, service_name: "web", project_id: PROJECT, project_name: "Storefront", environment_name: "production", created_at: ago(30), finished_at: ago(30) }],
};

const auditPerson: S["AuditPerson"] = { id: ME, name: "Alex Moreno", email: "alex@acme.test" };
const orgAudit: S["OrgAuditEntry"][] = [
  { id: id(321), org_id: ORG, actor_id: ME, actor: "Alex Moreno", actor_email: "alex@acme.test", action: "service.deploy", resource: "service", created_at: ago(2), target: { kind: "service", id: SERVICE, name: "web", deleted: false, project_id: PROJECT, service_id: SERVICE, environment: "production" }, detail: {} },
  { id: id(322), org_id: ORG, actor_id: SAM, actor: "Sam Okafor", actor_email: "sam@acme.test", action: "env_var.update", resource: "env_var", created_at: ago(5), target: { kind: "service", id: SERVICE, name: "web", deleted: false, project_id: PROJECT, service_id: SERVICE, environment: "production" }, detail: { key: "STRIPE_MODE" }, agent_id: id(331), agent_name: "Claude" },
];
const instanceAudit: S["AuditEntry"][] = [{ id: id(341), action: "operator.grant", actor: auditPerson, target: { id: SAM, name: "Sam Okafor", email: "sam@acme.test" }, detail: {}, created_at: ago(40) }];

const domain = (n: number, host: string, routed: boolean, cert: boolean, service_id = SERVICE, service_name = "web"): S["WorkspaceDomain"] => ({ id: id(400 + n), hostname: host, created_at: ago(40), created_by: "alex@acme.test", routed_at: routed ? ago(30) : null, certificate_at: cert ? ago(29) : null, temporary: false, service_id, service_name, scope: "project", project_id: PROJECT, project_name: "Storefront", environment_name: "production", server_id: SERVER });
const workspaceDomains = [domain(1, "shop.acme.test", true, true), domain(2, "www.acme.test", false, false), domain(3, "checkout.acme.test", true, false), domain(4, "metrics.acme.test", false, false, SHARED_SERVICE, "grafana")];
const serviceDomains: S["Domain"][] = workspaceDomains.slice(0, 3).map((d) => ({ id: d.id, service_id: SERVICE, hostname: d.hostname, tls_enabled: true, status: d.routed_at ? "active" : "pending", created_at: d.created_at, updated_at: d.created_at, created_by: d.created_by, routed_at: d.routed_at, certificate_at: d.certificate_at, temporary: false }));
const domainCheck: S["Check"] = {
  checked_at: ago(0.02), server: { name: "eu-central-1", address: "203.0.113.10", wildcards_ok: true, owner: true }, routing_known: true, routed_removed: [],
  domains: [
    { id: id(401), hostname: "shop.acme.test", dns: "ok", addresses: ["203.0.113.10"], certificate: "valid", not_after: ahead(1400), issuer: "Let's Encrypt", routed: true },
    { id: id(402), hostname: "www.acme.test", dns: "none", addresses: [], certificate: "missing", routed: false },
    { id: id(403), hostname: "checkout.acme.test", dns: "wrong", addresses: ["198.51.100.4"], certificate: "pending", routed: true },
  ],
};

const panelContainer = (name: string, role: S["PanelContainer"]["role"], image: string): S["PanelContainer"] => ({ name, role, image, state: "running", health: "healthy", started_at: ago(240), restarts: 0, cpu_percent: 2.4, memory_bytes: 268435456, memory_limit_bytes: 1073741824 });
const panel: S["PanelStatus"] = { shell: true, uptime_seconds: 864000, version: "1.14.2", healthy: true, containers: [panelContainer("toft-api", "api", "ghcr.io/toft/backend:1.14.2"), panelContainer("toft-web", "web", "ghcr.io/toft/frontend:1.14.2"), panelContainer("toft-db", "database", "postgres:16.4-bookworm"), panelContainer("toft-proxy", "proxy", "traefik:v3.6"), panelContainer("toft-git", "git", "gitea/gitea:1.24")], certificate: { domain: "panel.acme.test", issuer: "Let's Encrypt", not_after: ahead(1400), ask: null } };
const upgrade: S["UpgradeStatus"] = {
  install: "installer", current: "1.14.2", latest: "1.15.0", available: true, checked_at: ago(2), check_error: "", notes_url: "https://github.com/toft/toft/releases/tag/v1.15.0", since: ago(240), dumps: 3, last_dump_at: ago(240),
  run: { to: "1.14.2", from: "1.14.1", started_at: ago(240), finished_at: ago(239.9), step: "health", outcome: "succeeded", error: "", output: Array.from({ length: 40 }, (_, i) => `step ${i + 1}: done`), notes: {} },
};
const usage: S["PanelUsage"] = { cpu_percent: 22, load_1: 0.8, load_5: 0.7, load_15: 0.6, memory_used_bytes: 12884901888, memory_cache_bytes: 4294967296, swap_total_bytes: 0, swap_used_bytes: 0, disk_used_bytes: 171798691840, disk_free_bytes: 257698037760 };
const panelServer: S["PanelServer"] = {
  cpus: 8, memory_total_bytes: 34359738368, disk_total_bytes: 429496729600, uptime_seconds: 8640000, os: "Debian GNU/Linux 12", kernel: "6.1.0-25-arm64", docker_version: "27.3.1", now: usage,
  samples: Array.from({ length: 24 }, (_, i) => ({ at: ago(24 - i), cpu_percent: 20 + (i % 5) * 3, cpu_peak: 40 + (i % 7) * 4, memory_used_bytes: 12884901888, disk_used_bytes: 171798691840 })),
  disk: { images: 42, images_unused: 7, images_bytes: 32212254720, volumes_bytes: 53687091200, build_cache_bytes: 8589934592, containers_bytes: 1073741824 }, database: { version: "16.4", size_bytes: 1073741824, connections: 12, max_connections: 100 }, system: { firewall: "active", security_updates: "enabled", filesystem: "ext4" },
};
const diskPolicyValues: S["DiskPolicyValues"] = { build_cache_limit_bytes: 21474836480, auto_clean: ["untagged"], alert_percent: 85, alert_resolve_percent: 80 };
const diskPolicy: S["DiskPolicy"] = { build_cache_limit_bytes: null, auto_clean: null, alert_percent: null, effective: diskPolicyValues, defaults: diskPolicyValues };
const diskReport: S["DiskReport"] = {
  read_at: ago(0.2), size_bytes: 429496729600, used_bytes: 171798691840, free_bytes: 257698037760,
  classes: [{ kind: "in_use", removable: false, automatic: false, count: 12, bytes: 16106127360, examples: ["storefront-web:1f3a9c1"] }, { kind: "untagged", removable: true, automatic: true, count: 8, bytes: 4294967296, examples: ["<none>"] }, { kind: "old_builds", removable: true, automatic: false, count: 5, bytes: 2147483648, examples: ["storefront-web:0a1b2c3"] }],
  build_cache: { bytes: 8589934592, reclaimable_bytes: 6442450944, over_limit_bytes: 0, limit_bytes: 21474836480 }, volumes: [{ name: "old_pgdata", bytes: 1073741824, created_at: ago(2000), kind: "deleted_service", service_name: "postgres-old" }], volumes_in_use: { count: 4, bytes: 53687091200 }, containers_bytes: 1073741824, other_bytes: 2147483648,
  rollback_kept: [{ service_id: SERVICE, service_name: "web", kept: 3, images: 3, bytes: 3221225472 }], other_workspaces: { services: 0, rollback_images: 0, rollback_bytes: 0 }, policy: diskPolicy, clean: null, last_clean: null,
};
const hostUpdates: S["HostUpdates"] = {
  checked_at: ago(1), read_at: ago(1), error: "", lists_updated_at: ago(5), security: 2, other: 4, held: 0,
  packages: [{ name: "openssl", current: "3.0.13-1~deb12u1", candidate: "3.0.14-1~deb12u2", security: true }, { name: "curl", current: "7.88.1-10+deb12u6", candidate: "7.88.1-10+deb12u7", security: false }], removals: [], truncated: false,
  reboot: { required: true, since: ago(30), packages: ["linux-image-arm64"] },
  docker: { version: "27.3.1", package: "docker-ce", candidate: "27.4.0", advisories: [{ id: "CVE-2026-0001", severity: "high", summary: "A container can read a neighbouring volume", fixed_in: "27.4.0", url: "https://github.com/moby/moby/security/advisories/1" }] },
  unattended: { state: "active", last_run_at: ago(9), last_success_at: ago(9), last_result: "ok", last_error: "" },
};
const capabilities: S["ServerCapabilities"] = {
  kernel: "6.1.0-25-arm64", arch: "arm64", cgroup_v2: true, virtualization: "kvm", kvm: true, docker_version: "27.3.1", apparmor: true, seccomp: true, preview_firewall: true, dns_relay: true, address_pools: [{ base: "10.20.0.0/16", size: 24 }],
  runtimes: [{ name: "runc", ok: true, version: "1.1.14" }, { name: "runsc", ok: false, error: "not installed" }], error: "", probed_at: ago(2),
  levels: [{ id: "standard", state: "available", reason: "" }, { id: "sandboxed", state: "not_installed", reason: "gVisor isn't installed" }, { id: "sandboxed_strict", state: "unavailable", reason: "Needs a kernel gVisor supports" }, { id: "microvm", state: "unavailable", reason: "" }, { id: "dedicated", state: "unavailable", reason: "" }],
};
const capacity: S["CapacitySummary"] = { server_id: SERVER, effective_cpu_cores: 7, effective_memory_mb: 30720, allocated_cpu_cores: 3.5, allocated_memory_mb: 14336, live_cpu_percent: 22, live_memory_mb: 12288, capacity_discovered: true, share: false, disk_used_mb: 163840, disk_total_mb: 409600, docker_version: "27.3.1", uptime_seconds: 8640000, traefik_status: "running", traefik_image: "traefik:v3.6", workspaces: [{ org_id: ORG, org_name: "Acme", projects: ["Storefront", "Internal tools"], services: 4, cpu_cores: 3.5, memory_mb: 14336 }] };
const systemStats: S["SystemStats"] = { cpu_percent: 22, mem_used_mb: 12288, mem_total_mb: 32768, disk_used_mb: 163840, disk_total_mb: 409600, load_1: "0.80", load_5: "0.70", load_15: "0.60", uptime: "100 days", docker_version: "27.3.1", uptime_seconds: 8640000, traefik_status: "running", traefik_image: "traefik:v3.6" };
const serverServices: S["ServiceOnServer"][] = services.map((s) => ({ id: s.id, name: s.name, project_name: "Storefront", project_id: PROJECT, environment_name: "production", scope: "project", cpu_limit: "1", memory_limit: "512m", runtime_status: s.runtime_status, effective_isolation: "standard" }));

const backup: S["BackupDestinationDTO"] = { endpoint: "s3.eu-central-1.amazonaws.com", bucket: "acme-toft-backups", access_key: "AKIAIOSFODNN7EXAMPLE", schedule: "0 3 * * *", repository: "s3:s3.eu-central-1.amazonaws.com/acme-toft-backups", last_run_at: ago(9), last_ok: true, last_ok_at: ago(9), last_bytes: 4294967296, last_error: "", created_at: ago(900) };

const member = (n: number, name: string, email: string, role: S["Member"]["role"]): S["Member"] => ({ id: id(500 + n), org_id: ORG, user_id: n === 1 ? ME : SAM, user_email: email, user_name: name, role, created_at: ago(2000) });
const instanceUser = (n: number, name: string, email: string, over: Partial<S["User"]> = {}): S["User"] => ({ id: id(600 + n), email, name, is_active: true, is_operator: false, created_at: ago(2000), deactivated_at: null, deactivated_by: "", workspaces: ["Acme"], last_login_at: ago(5), reset_expires_at: null, ...over });

const cell = (action: S["AgentPolicyCell"]["action"], env_class: S["AgentPolicyCell"]["env_class"], decision: S["AgentPolicyCell"]["decision"]): S["AgentPolicyCell"] => ({ action, env_class, decision, default: decision });
const policyCells: S["AgentPolicyCell"][] = [cell("deploy", "preview", "allow"), cell("deploy", "other", "ask"), cell("deploy", "protected", "never"), cell("code", "any", "allow"), cell("issues", "any", "allow"), cell("variables", "other", "ask"), cell("variables", "protected", "never"), cell("secrets", "any", "never"), cell("runbooks", "other", "ask")];
const connection: S["AgentConnection"] = { id: id(701), name: "Claude Code", client_kind: "cimd", client: "claude.ai", user_id: ME, user_name: "Alex Moreno", user_email: "alex@acme.test", user_role: "owner", all_projects: true, projects: [], scopes: ["read", "deploy", "code", "issues"], created_at: ago(400), expires_at: ahead(1800), last_used_at: ago(0.5), last_tool: "deploy" };
const approval: S["AgentApproval"] = {
  id: REQUEST, connection_id: connection.id, agent: "Claude Code", user_id: ME, user_name: "Alex Moreno", user_role: "owner", action: "deploy", tool: "deploy", service_id: SERVICE, environment_id: ENV, project_id: PROJECT, target: "web", env_class: "protected",
  request: { method: "POST", path: `/api/v1/services/${SERVICE}/deployments`, body: { commit_sha: sha(1) } }, reason: "Ship the cart fix; see https://ci.acme.test/runs/813 for the green run and the logs of the previous attempt.", plan: "Deploy, watch /health for two minutes, roll back on failure.",
  plan_steps: [{ text: "Deploy web at 1f3a9c1", tool: "deploy", service_id: SERVICE, mark: "this_request" }, { text: "Watch the logs", tool: "get_deploy_logs", service_id: SERVICE, mark: "allowed_by_this_approval" }, { text: "Roll back if it fails", tool: "rollback", service_id: SERVICE, mark: "will_ask_again" }],
  rollback: null, state: "waiting", expires_at: ahead(0.5), decided_by: "", decided_at: null, note: "", executed_at: null, result: null, created_at: ago(0.2),
};
const waiting: S["AgentWaiting"] = { id: REQUEST, agent: "Claude Code", user_id: ME, user_name: "Alex Moreno", action: "deploy", tool: "deploy", service_id: SERVICE, target: "web", reason: "Ship the cart fix", expires_at: ahead(0.5), created_at: ago(0.2) };

const apiKey: S["APIKey"] = { id: id(801), user_id: ME, name: "ci-deploys", prefix: "paas_8fK2", project_id: PROJECT, project_name: "Storefront", org_name: "Acme", scopes: ["deploy", "checks"], last_used_at: ago(1), created_at: ago(900), expires_at: ahead(4000), service_ids: [SERVICE], services: [{ id: SERVICE, name: "web", environment_name: "production" }] };
const sshKey: S["SSHKey"] = { id: id(811), title: "alex@work-laptop", fingerprint: "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s", key_type: "ssh-ed25519", last_used_at: ago(4), created_at: ago(900) };
const recentAlert: S["RecentAlert"] = { id: id(821), kind: "deploy_failed", subject: WORKER, detail: "the health check on /health never answered", fired_at: ago(3), resolved_at: null, one_off: true, subject_name: "orders-worker", project_id: PROJECT, project_name: "Storefront", environment_name: "production", server_name: "eu-central-1" };
const volume: S["Volume"] = { id: id(831), server_id: SERVER, server_name: "eu-central-1", name: "images", size_limit_bytes: 10737418240, used_bytes: 6442450944, used_at: ago(1), created_at: ago(900), mounts: [{ service_id: SERVICE, service_name: "web", target: "/data/images", read_only: false }] };

/** A repository nothing was pushed to yet (its page shows the commands). */
export const emptyRepository: Json200<typeof R> = { data: { ...repositoryDetail, empty: true, branch_count: 0, tag_count: 0, pulls: { open: 0, merged: 0, closed: 0 }, deploys: [] } };

const empty = { data: [] };
const P = "/api/v1/projects/{projectID}" as const;
const R = `${P}/repositories/{repositoryID}` as const;
const V = "/api/v1/servers/{serverID}" as const;
const IP = "/api/v1/instance/panel" as const;
const Sv = "/api/v1/services/{serviceID}" as const;
const E = "/api/v1/environments/{environmentID}" as const;

get("/api/v1/me", (_url, theme) => ({ data: { id: ME, email: "alex@acme.test", name: "Alex Moreno", role: "admin", is_active: true, is_operator: true, last_login_at: ago(1), theme: theme as S["Profile"]["theme"], created_at: ago(2000), updated_at: ago(2) } }));
get("/api/v1/org", { data: { id: ORG, name: "Acme", created_at: ago(2000), updated_at: ago(40) } });
get("/api/v1/org/list", { data: [{ org_id: ORG, name: "Acme", role: "owner" }, { org_id: OTHER_ORG, name: "Acme Labs", role: "developer" }] });
get("/api/v1/org/settings", { data: { resource_bypass_min_role: "owner", default_isolation: "standard" } });
get("/api/v1/org/isolation", { data: { default_isolation: "standard", servers: [{ id: SERVER, name: "eu-central-1", sandboxed: "not_installed", reason: "gVisor isn't installed", sandboxed_strict: "unavailable", strict_reason: "", following: 3 }] } });
get("/api/v1/org/members", { data: [member(1, "Alex Moreno", "alex@acme.test", "owner"), member(2, "Sam Okafor", "sam@acme.test", "developer")] });
get("/api/v1/org/members/addable", empty);
get("/api/v1/org/invites", { data: [{ id: id(901), org_id: ORG, created_by: ME, token: "inv_3f9a1c7e", role: "developer", expires_at: ahead(48), used_at: null, created_at: ago(2) }] }); // gitleaks:allow
get("/api/v1/org/audit-log", { data: orgAudit });
get("/api/v1/org/audit-log/count", { data: { total: 2 } });

get("/api/v1/dashboard", { data: dashboard });
get("/api/v1/inbox/needs-you", { data: needsYou });
get("/api/v1/inbox/updates", { data: inboxUpdates });
get("/api/v1/inbox/counts", { data: { needs_you: 5, unread: 1, my_issues: 5 } });
get("/api/v1/workspace/my-issues", { data: myIssues });
get("/api/v1/workspace/services", { data: [...services, shared].map(workspaceService) });
get("/api/v1/workspace/environments", { data: [environment, staging, preview].map((e) => ({ id: e.id, project_id: PROJECT, name: e.name, color: e.color, protected: e.protected, preview: !!e.preview })) });
get("/api/v1/workspace/issue-settings", { data: { digits: 3, cycles: { enabled: true, weeks: 2, start_day: 1, time_zone: "Europe/Athens" }, estimates: "fibonacci" } });
get("/api/v1/workspace/issue-types", { data: typeRows });
get("/api/v1/workspace/cycles", { data: [cycleNext, cycle, cycleOld] });
get("/api/v1/workspace/cycles/{cycleNumber}", { data: cycleDetail });
get("/api/v1/workspace/cycles/{cycleNumber}/issues", { data: issues.map((i, n) => cycleIssue(i, n === 0 ? 13 : null)) });
get("/api/v1/workspace/search", { data: { docs: [], issues: [], pulls: [] } });
get("/api/v1/domains", { data: workspaceDomains });
get("/api/v1/projects", { data: [project, project2] });
get("/api/v1/shared-services", { data: [shared] });
get("/api/v1/shared-volumes", { data: [volume] });
get("/api/v1/servers", { data: [server, agentServer] });
get("/api/v1/git", { data: { enabled: true, ssh_host: "git.acme.test", ssh_port: 2222, host_key_fingerprint: "SHA256:9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4" } });
get("/api/v1/catalog", empty);
get("/api/v1/resources/default-limit", { data: { kind: "app", memory_mb: 512 } });
get("/api/v1/services/address", { data: { hostname: "storefront.203.0.113.10.sslip.io", temporary: true, reason: "" } });
get("/api/v1/alerts", { data: [recentAlert, { ...recentAlert, id: id(822), kind: "disk_full", subject: SERVER, subject_name: "eu-central-1", detail: "Disk at 91%", resolved_at: ago(1), one_off: false, project_id: undefined, project_name: "", environment_name: "" }] });
get("/api/v1/alerts/channel", { data: { webhook_url: "https://hooks.acme.test/toft-alerts", enabled: true, updated_at: ago(40), last_delivery_at: ago(3), last_delivery_status: 200, last_delivery_error: "" } });
get("/api/v1/observability", { data: { id: id(1001), org_id: ORG, grafana_url: "https://grafana.acme.test", loki_url: "http://loki:3100", grafana_service_id: SHARED_SERVICE, created_at: ago(400), updated_at: ago(40) } });
get("/api/v1/observability/projects", { data: [{ project_id: PROJECT, name: "Storefront", loki_url: "", grafana_url: "" }] });
get("/api/v1/observability/logs", { data: { service_id: SERVICE, containers: [{ name: "web", driver: "loki", loki_url: "http://loki:3100" }] } });
get("/api/v1/user/api-keys", { data: [apiKey, { ...apiKey, id: id(802), name: "local-dev", project_id: undefined, project_name: undefined, service_ids: [], services: [], expires_at: null, last_used_at: null }] });
get("/api/v1/user/ssh-keys", { data: [sshKey] });
get("/api/v1/deploy-policy/violations", { data: { mode: "enforce", generated_at: ago(1), services: [{ service_id: SERVICE, service_name: "web", org_id: ORG, org_name: "Acme", project_id: PROJECT, project_name: "Storefront", environment_name: "production", server_id: SERVER, server_name: "eu-central-1", source_type: "git", violations: [{ rule: "no_privileged", field: "privileged", message: "Runs privileged" }], allowed: [], allowed_rules: [], compose_checked_at_deploy: false, invalid_env_keys: [], exception_reason: "", exception_granted_by: "", exception_granted_at: null, allowed_values: null, last_deploy_check: null }], services_checked: 4, servers_checked: 2, rules: [{ rule: "no_privileged", grantable: true, description: "No privileged containers", why: "A privileged container owns the server.", fix: "Remove privileged: true." }] } });

get("/api/v1/agents/connections", { data: [connection] });
get("/api/v1/agents/policy", { data: { cells: policyCells, projects: [{ project_id: PROJECT, name: "Storefront", cells: policyCells }] } });
get("/api/v1/agents/pause", { data: { workspace: null, instance: null } });
get("/api/v1/agents/approvals", { data: [waiting] });
get("/api/v1/agents/approvals/{approvalID}", { data: approval });

get("/api/v1/instance/registration", { data: { mode: "invite", first_run: false, setup_token_required: false } });
get("/api/v1/instance/setup", { data: { in_progress: false, domain: "panel.acme.test", domain_set: true, awaiting_domain: false, role: "apps", role_set: true, kit_acknowledged_at: ago(2000), kit_out_of_date: false, finished_at: ago(2000), app: "https://panel.acme.test" } });
get("/api/v1/instance/recovery-kit", { data: { filename: "toft-recovery-kit-panel.acme.test.txt", text: "Toft recovery kit\n", domain: "panel.acme.test", version: "v0.34.0", encryption_key: "0".repeat(64), backup: { repository: "s3:https://s3.acme.test/acme-toft-backups", password: "fixture", access_key: "fixture" }, restore_command: "sudo sh install.sh --restore", fingerprint: "f".repeat(64) } });
get("/api/v1/instance/backup", { data: backup });
get("/api/v1/instance/upgrade", { data: upgrade });
get("/api/v1/instance/watchdog", { data: { url: "https://hc.acme.test/ping/toft", last_ping_at: ago(0.05), last_status: 200, last_error: "", held: false } });
get("/api/v1/instance/users", { data: { users: [instanceUser(1, "Alex Moreno", "alex@acme.test", { is_operator: true }), instanceUser(2, "Sam Okafor", "sam@acme.test", { reset_expires_at: ahead(20) }), instanceUser(3, "Dana Levi", "dana@acme.test", { is_active: false, deactivated_at: ago(90), deactivated_by: "Alex Moreno" })], deleted: 1 } });
get("/api/v1/instance/operators", { data: [{ user_id: ME, email: "alex@acme.test", name: "Alex Moreno", is_active: true, source: "first_run", granted_by: null, granted_at: ago(2000) }] });
get("/api/v1/instance/audit-log", { data: instanceAudit });
get(IP, { data: panel });
get(`${IP}/server`, { data: panelServer });
get(`${IP}/capabilities`, { data: capabilities });
get(`${IP}/updates`, { data: hostUpdates });
get(`${IP}/updates/run`, { data: null });
get(`${IP}/disk`, { data: diskReport });
get(`${IP}/disk/clean`, { data: null });
get(`${IP}/disk/policy`, { data: diskPolicy });
get(`${IP}/gvisor`, { data: null });
get("/api/v1/instance/panel-host", { data: { version: "1.14.2", reservation: { min_cpu: 2, min_memory_bytes: 2147483648, system_cpu: 1, system_memory_bytes: 2147483648, panel_cpu: 1, panel_memory_bytes: 2147483648, apps_cpu: 6, apps_memory_bytes: 28991029248, enforced: true }, control_plane_subnet: "172.30.0.0/24", app_address_pool: "10.20.0.0/16", app_address_pools: [{ base: "10.20.0.0/16", size: 24 }], os: "Debian GNU/Linux 12", role: "apps", address: "203.0.113.10", cpu_cores: 8, memory_bytes: 34359738368, disk_bytes: 429496729600, app_networks: "10.20.0.0/16" } });

get(P, { data: project });
get(`${P}/overview`, { data: { environments: [{ id: ENV, name: "production", color: "amber", protected: true, variables: 12, secrets: 4 }, { id: STAGING, name: "staging", color: "blue", protected: false, variables: 9, secrets: 3 }], services: services.map((s) => ({ service_id: s.id, domains: s.id === SERVICE ? ["shop.acme.test"] : [], last_deployment: lastDeployment })), recent_deployments: [{ id: DEPLOY, service_id: SERVICE, service_name: "web", environment_name: "production", status: "running", commit_sha: sha(1), commit_message: "Keep the cart when a session expires", triggered_by: "alex@acme.test", image_tag: "storefront-web:1f3a9c1", created_at: ago(30.2), started_at: ago(30.1), finished_at: ago(30) }, { id: DEPLOY_FAILED, service_id: WORKER, service_name: "orders-worker", environment_name: "production", status: "failed", commit_sha: sha(2), commit_message: "Bump the checkout SDK", triggered_by: "sam@acme.test", image_tag: "", created_at: ago(3.2), started_at: ago(3.1), finished_at: ago(3) }] } });
get(`${P}/environments`, { data: [environment, staging, preview] });
get(`${P}/services`, { data: services });
get(`${P}/shared-services`, { data: [shared] });
get(`${P}/observability`, { data: { id: id(1002), org_id: ORG, project_id: PROJECT, grafana_url: "https://grafana.acme.test", created_at: ago(400), updated_at: ago(40) } });
get(`${P}/webhook`, { data: { url: "https://panel.acme.test/api/v1/webhooks/github/3f9a1c7e", secret: "whsec_3f9a1c7e5b2d4f6a", last_push_at: ago(30), last_push_repo: "acme/storefront" } }); // gitleaks:allow
get(`${P}/data`, { data: [{ service_id: DB_SERVICE, service_name: "postgres", server_name: "eu-central-1", read: true, volumes: [{ name: "pgdata", bytes: 2147483648, from: "volume" }] }] });
get(`${P}/api-keys`, { data: [{ id: id(801), name: "ci-deploys", prefix: "paas_8fK2", scopes: ["deploy", "checks"], owner_id: ME, owner_name: "Alex Moreno", last_used_at: ago(1), created_at: ago(900), expires_at: null }] });
get(`${P}/previews`, { data: [] });
get(`${P}/previews/settings`, { data: { environment_id: STAGING, environment_name: "staging", domain: "preview.acme.test", max_per_project: 5 } });
get(`${P}/previews/domain-check`, { data: { domain: "*.preview.acme.test", record: "A", want: "203.0.113.10", got: ["203.0.113.10"], ok: true, checked_at: ago(1) } });
get(`${P}/cycles`, { data: { enabled: true, estimates: "fibonacci", cycles: [cycleNext, cycle, cycleOld] } });
get(`${P}/issues`, (url) => ({ data: issues.filter((i) => (url.searchParams.get("status") ?? i.status).split(",").includes(i.status)) }));
get(`${P}/issue-counts`, { data: { backlog: 1, todo: 1, in_progress: 1, in_review: 1, shipped: 1, cancelled: 0 } });
get(`${P}/issues/{issueNumber}`, { data: issueDetail, version: "7" });
get(`${P}/issues/{issueNumber}/timeline`, { data: issueTimeline });
get(`${P}/issue-labels`, { data: [{ name: "checkout", open: 3, total: 8, service: false }, { name: "web", open: 2, total: 5, service: true }] });
get(`${P}/issue-views`, { data: [{ id: id(1101), name: "Urgent checkout", filters: { priority: ["urgent"], label: ["checkout"] }, owner: alex, created_at: ago(40), updated_at: ago(4) }] });
get(`${P}/issue-settings`, { data: { failed_deploy_issues: true, key: "STO", aliases: [], digits: 3, workspace_digits: 3, inherit_digits: true, next_key: "STO-123", default_type_id: bug.id, hidden_type_ids: [] }, version: "3" });
get(`${P}/issue-timeline`, { data: { cycles: [cycleOld, cycle, cycleNext], items: [timelineEntry(issues[0], -3, 1), timelineEntry(issues[1], 0, 6), timelineEntry(issues[2], -8, 3), timelineEntry(issues[4], -14, -6)], blocks: [{ from: "STO-119", to: "STO-118" }], undated: 1 } });
get(`${P}/docs`, { data: docTree });
get(`${P}/docs/{pageID}`, { data: docPage });
get(`${P}/repositories`, { data: [repository] });
get(R, { data: repositoryDetail });
get(`${R}/tree`, { data: tree });
get(`${R}/blob`, { data: blob });
get(`${R}/branches`, { data: [{ name: "main", default: true, commit, ahead: null, behind: null, checks: checksPassed }, { name: "fix/cart-expiry", default: false, commit: commit2, ahead: 2, behind: 0, checks: checksFailed, issues: [{ key: "STO-118", number: ISSUE, title: "Carts vanish when a session expires", status: "in_progress" }] }] });
get(`${R}/tags`, { data: [{ name: "v1.4.2", sha: sha(1), annotated: true, message: "Release 1.4.2", created_at: ago(40) }] });
get(`${R}/commits`, { data: [commit, commit2] });
get(`${R}/commits/{sha}`, { data: { ...commit, stats: { files: 1, additions: 2, deletions: 1 }, files: [{ path: fileDiff.path, status: "modified", additions: 2, deletions: 1, binary: false }], files_truncated: false } });
get(`${R}/commits/{sha}/diff`, { data: [fileDiff] });
get(`${R}/commits/{sha}/checks`, { data: commitChecks });
get(`${R}/pipelines`, { data: pipelines });
get(`${R}/pulls`, { data: [pullBase, { ...pullBase, number: 45, title: "Show saved addresses", head_branch: "feat/addresses", author: claude, checks: checksPassed }] });
get(`${R}/pulls/{pullNumber}`, { data: pullDetail });
get(`${R}/pulls/{pullNumber}/diff`, { data: [fileDiff] });
get(`${R}/pulls/{pullNumber}/timeline`, { data: [{ type: "comment", id: id(1201), actor: sam, created_at: ago(20), edited_at: null, body: "Looks right.", html: "<p>Looks right.</p>" }, { type: "event", id: id(1202), actor: alex, created_at: ago(10), edited_at: null, kind: "pushed", data: { count: 1, commits: [{ sha: sha(1), subject: "Keep the cart when a session expires" }] } }] });
get(`${R}/mirrors`, empty);
get(`${R}/webhooks`, empty);

get(E, { data: environment });
get(`${E}/overview`, { data: { network: "toft-storefront-production", services: services.map((s) => ({ service_id: s.id, own_variables: 3, own_secrets: 1, overrides: ["LOG_LEVEL"], domains: s.id === SERVICE ? ["shop.acme.test"] : [], last_deployment: lastDeployment })) } });
get(`${E}/services`, { data: services });
get(`${E}/env-vars`, { data: [{ id: id(1301), environment_id: ENV, key: "LOG_LEVEL", value: "info", is_secret: false, masked: false, created_at: ago(90), updated_at: ago(40), updated_by: "alex@acme.test" }], version: "2" });
get(`${E}/secrets`, { data: [{ id: id(1311), environment_id: ENV, key: "STRIPE_SECRET", created_at: ago(90), updated_at: ago(40), updated_by: "alex@acme.test" }] });
get(`${E}/data`, { data: [{ service_id: DB_SERVICE, service_name: "postgres", server_name: "eu-central-1", read: true, volumes: [{ name: "pgdata", bytes: 2147483648, from: "volume" }] }] });

get(Sv, { data: service });
get(`${Sv}/deployments`, { data: [deployment, failedDeployment] });
get(`${Sv}/deployments/{deploymentID}`, { data: deployment });
get(`${Sv}/deployments/{deploymentID}/logs`, { data: [{ id: 1, deployment_id: DEPLOY, level: "info", message: "Building image storefront-web:1f3a9c1", created_at: ago(30.15) }, { id: 2, deployment_id: DEPLOY, level: "info", message: "Health check passed", created_at: ago(30) }] });
get(`${Sv}/domains`, { data: serviceDomains });
get(`${Sv}/domains/check`, { data: domainCheck });
get(`${Sv}/env-vars`, { data: [{ id: id(1302), service_id: SERVICE, key: "DATABASE_URL", value: "postgres://postgres:5432/shop", is_secret: false, masked: false, created_at: ago(90), updated_at: ago(40), updated_by: "alex@acme.test" }], version: "4" });
get(`${Sv}/secrets`, { data: [{ id: id(1312), service_id: SERVICE, key: "STRIPE_KEY", created_at: ago(90), updated_at: ago(40), updated_by: "alex@acme.test" }] });
get(`${Sv}/config-files`, empty);
get(`${Sv}/containers`, { data: ["web-1"] });
get(`${Sv}/shared-volumes`, { data: [{ shared_volume_id: volume.id, name: "images", target: "/data/images", read_only: false }], version: "2" });
get(`${Sv}/stats`, { data: { cpu_percent: 2.4, memory_mb: 180, memory_limit_mb: 512, started_at: ago(30), restart_count: 0 } });
get(`${Sv}/metrics`, { data: Array.from({ length: 12 }, (_, i) => ({ timestamp: ago(12 - i), cpu_percent: 2 + (i % 4), memory_mb: 170 + i * 2, throttled_pct: 0 })) });
get(`${Sv}/resources`, { data: { grace_until: null, rows: [{ compose_service: "", kind: "app", memory: { limit: "512m", source: "service", file_limit: "", peak_14d_mb: 240, days: 14, oom_kills_7d: 0, suggestion: null }, cpu: { limit: "1", p95_cores: 0.2, throttled_max_pct: 0, wait_avg_pct: 0.4, suggestion: null }, restarts_24h: 0, restarts_since: ago(240) }], latency_24h: { requests: 18420, p95_ms: 84, p95_bucket: { low_ms: 50, high_ms: 100 }, since: ago(24), unmeasured: null } }, version: "1" });
get(`${Sv}/deploy-policy`, { data: { service_id: SERVICE, mode: "enforce", violations: [], allowed: [], exception: null, compose_checked_at_deploy: false, invalid_env_keys: [], rules: [], can_manage_exceptions: true, control_plane_restricted: false, compose_services: [], last_deploy_check: null, values_digest: "d1" }, version: "1" });
get(`${Sv}/versions`, { data: { source_type: "git", is_outdated: false, current_branch: "main", latest_commit: { sha: sha(1), message: "Keep the cart when a session expires", author: "Alex Moreno", timestamp: ago(30) }, branches: [{ name: "main", latest_commit: { sha: sha(1), message: "Keep the cart when a session expires", author: "Alex Moreno", timestamp: ago(30) } }], git_tags: [{ name: "v1.4.2", sha: sha(1), date: ago(40) }] } });
get(`${Sv}/work`, { data: { pulls: [{ repository: REPO, repository_id: REPO_ID, number: PULL, title: "Keep the cart when a session expires", state: "open", checks: "failure" }], issues: [{ key: "STO-118", number: ISSUE, title: "Carts vanish when a session expires", status: "in_progress", priority: "high", project_id: PROJECT }], pages: [{ id: DOC, title: "Checkout runbook", stale: false, verified_at: ago(48) }] } });
get(`${Sv}/data`, { data: [] });
get(`${Sv}/volumes`, empty);
get(`${Sv}/admin-password`, { data: { pending: false } });

get(V, { data: server });
get(`${V}/backup`, { data: backup });
get(`${V}/dns-provider`, { data: null });
get(`${V}/capabilities`, { data: capabilities });
get(`${V}/updates`, { data: hostUpdates });
get(`${V}/updates/run`, { data: null });
get(`${V}/disk`, { data: diskReport });
get(`${V}/disk/clean`, { data: null });
get(`${V}/disk/policy`, { data: diskPolicy });
get(`${V}/gvisor`, { data: null });
get(`${V}/capacity-summary`, { data: capacity });
get(`${V}/provisioning`, { data: { started_at: ago(2000), started_by: "Alex Moreno", finished_at: ago(1999.9), ok: true, error: "", log: "Installing Docker\nDone", warnings: [] } });
get(`${V}/requirements`, { data: { met: true, os: "Debian 12", checks: [{ id: "docker", label: "Docker", found: "27.3.1", met: true }] } });
get(`${V}/resources`, { data: { services: [], frees_mb: 0, cpu_wait: null } });
get(`${V}/services`, { data: serverServices });
get(`${V}/shares`, { data: [{ org_id: OTHER_ORG, org_name: "Acme Labs", cpu_cores: 2, memory_mb: 4096, created_at: ago(400) }] });
get(`${V}/system-stats`, { data: systemStats });

get(`${V}/fit`, { data: { capacity_known: true, fits: true, cpu_ok: true, memory_ok: true, share: false, effective_cpu_cores: 7, effective_memory_mb: 30720, allocated_cpu_cores: 3.5, allocated_memory_mb: 14336 } });
get(`${R}/compare`, { data: compare });
get(`${R}/pulls/{pullNumber}/preview`, { data: { pull_number: PULL, repository_id: REPO_ID, repository: REPO, title: "Keep the cart when a session expires", branch: "fix/cart-expiry", environment: { id: PREVIEW_ENV, name: "pr-47" }, copies: "staging", status: "running", untrusted: false, head_sha: sha(1), approved_sha: sha(1), approved_by: "Alex Moreno", approved_at: ago(10), needs_approval: false, services: [{ id: SERVICE, name: "web", server: "eu-west-2", status: "running", sha: sha(1), deployment_id: DEPLOY, started_at: ago(10), finished_at: ago(9.9), addresses: ["pr-47.preview.acme.test"], isolation: "sandboxed" }], reached: ["postgres"], migration_files: [], migrations_unchecked: false, created_at: ago(10), updated_at: ago(9) } });
stream(`${Sv}/deployments/{deploymentID}/logs/stream`, `data: ${JSON.stringify({ id: 3, deployment_id: DEPLOY, level: "info", message: "Health check passed", created_at: ago(30) })}\n\nevent: done\ndata: ${JSON.stringify({ status: "running", runtime_status: "running" })}\n\n`);
// running: false ends the tab's follow; running would make it reconnect forever.
stream(`${Sv}/logs/live`, `event: container\ndata: ${JSON.stringify({ name: "web-1", running: false })}\n\n${["level=info msg=\"GET /health 200 2ms\"", "level=warn msg=\"slow query\" duration=840ms", "level=error msg=\"payment webhook retried\" attempt=3"].map((l) => `data: ${new Date(Date.now() - 60000).toISOString()} ${l}\n\n`).join("")}`);

const specific = (r: Route) => (r.path.match(/\{/g) ?? []).length;
const templates = routes.map((r) => ({ ...r, re: new RegExp(`^${r.path.replace(/\{[^}]+\}/g, "[^/]+")}$`) })).sort((a, b) => specific(a) - specific(b));

/** The contract's answer to a GET, or undefined when the check has no fixture. */
export function answer(url: URL, theme: string): Fulfil | undefined {
  const route = templates.find((t) => t.re.test(url.pathname));
  if (!route) return undefined;
  if (route.stream !== undefined) return { status: 200, contentType: "text/event-stream", body: route.stream };
  return { status: 200, contentType: "application/json", body: JSON.stringify(route.reply(url, theme)) };
}
