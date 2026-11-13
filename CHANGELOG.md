# Changelog

One entry per release, newest first. Toft follows [semantic versioning](https://semver.org/);
before 1.0, a minor version may change behaviour, and its entry says how.
Releases, with the install script and the signed agent binaries, are on
[GitHub](https://github.com/Nickpap21/toft/releases). A version marked "not
published" was tagged, but its release run failed before publishing anything.

## 1.0.0 (2026-11-13)

Toft is public. Deploys, git hosting with pull requests, issues, docs and
access for AI agents, on servers you own, under the AGPL-3.0. Beside the
release changes, it fixes what running 0.46.0 showed: count badges that
lagged behind, a failing count with nothing to click, and a worker
reported unhealthy by its image's check. One migration (0081, new
columns with defaults): a failed upgrade starts 0.46.0 again without
restoring the database. Upgrade from 0.46.0; a new install follows the
README.

From 1.0.0, as semantic versioning says, a release that breaks an
install, an upgrade or the API is a new major version; a minor version
adds, a patch fixes.

### Releases

- The images are public: `install.sh` and `toft upgrade` pull them with
  no registry token, by the digests the signed image manifest names.
- A release's notes on GitHub are its entry here.
- The release's signing jobs read the release key from a GitHub
  environment open only to release tags.

### Added

- **A container check per service.** The Health check card in a
  service's settings chooses what Docker checks inside its containers:
  the image's own `HEALTHCHECK` (as before), none, or a command (run by
  `sh`, at the image's interval, timeout and retries, or Docker's if it
  sets none). A worker built from a web image no longer shows Unhealthy
  for a port it never opens. `container_health` and
  `container_health_command` on a service in the API; the alert for an
  unhealthy service says which check it fails. It applies from the
  service's next deploy.
- **Live counts.** Issues, docs, pull requests, alerts and service
  states changed by anyone, by git or by the monitor send a `changed`
  event on `/api/v1/events`, and the badges and lists that read them
  refresh: Needs you, My issues, a project's Issues, Docs and Code
  counts, and the health dots. Any change you make, deploy events and a
  reconnect of the stream refresh them too.

### Fixed

- A project's health line names each failing service, with a link to
  it; it said "1 failing" with nothing to click.
- A repository shows the branch its push hook protects
  (`protected_branch` in the API), and its settings warn when that isn't
  the default branch, as after a rollback that changed the default
  branch.
- The disk report on a server shared with other workspaces names none of
  their images, also once their services are gone.
- Git over HTTPS and SSH: a client that hasn't finished negotiating
  within 2 minutes gives its slot back, and one account holds at most 4
  of the clients' 12 slots (it was 6), so two accounts can't take them
  all. A push's client may compress for 10 minutes again before it
  sends (it was 5).
- An agent whose `agent.env` couldn't be saved after it joined keeps
  working after the panel's certificate changes; it refused every
  connection until joined again.

### Upgrading

- A server's agent from before 1.0.0 deploys a service whose container
  check is off or a command only once it has upgraded (it does when it
  next connects); until then the deploy fails at its connect step and
  says so.
  The image's own check deploys as before.
- The settings a deploy records now include the container check, so no
  live deployment matches its service's settings until the service
  deploys again: until then, a certificate issued for a new domain
  waits for that deploy to switch the service to HTTPS, rather than
  redeploying it on its own.
- The Go client (`pkg/apiclient`) keeps its constant names.

## 0.46.0 (2026-11-12)

Fixes from a security audit of everything since 0.28.0, with the node
agent and git pushes in full. The panel's images are now pulled by the
digests a signed manifest names, and a server's join token goes only to
the panel's own certificate. One migration (0080, a new column): a
failed upgrade starts 0.45.4 again without restoring the database.
Upgrade from 0.45.4.

Minor-version changes in behaviour:

- `install.sh` and `toft upgrade` install only a release with a signed
  image manifest, so 0.46.0 and later. `install.sh` checks the signature
  with OpenSSL 3, so it refuses a host with OpenSSL 1.1 (Debian 11, for
  one); `toft upgrade` checks it itself. Going back to the release a panel ran before
  (`toft upgrade --to v0.45.4 --force`) still works, from the images
  still on the host.
- Changing a repository's default branch needs an owner's session and
  the password, as turning its protection off does: an API key gets
  `session_required`, a session without a recent password
  `reauth_required`.
- A git session that sends nothing for a minute (a push's pack for five)
  is ended; it was ten.

### Security

- **The panel's images are pulled by signed digest.** Each release signs
  a manifest of its backend and frontend image digests with the release
  key, published as `paas-backend:<version>-release` and attached to the
  GitHub release. `install.sh`, `toft upgrade` and its handover check the
  signature and the version, then pull by digest: a tag moved in the
  registry no longer changes what runs as root with the Docker socket.
- **A server's join token goes only to the panel's certificate.** On a
  panel without a domain, the join command, the agent's download and the
  agent's first connection pin the SHA-256 of the panel certificate's
  public key, where they accepted any certificate. If the certificate
  changed since (Traefik restarted), the join says to make a new join
  command. Panels with a domain use the verified certificate, as before.
- **A service's base directory can't lead a build out of its
  repository.** It is checked on save (relative, no `..`, at most 255
  characters) and at every build, and a build whose folder or Dockerfile
  is a link out of the repository is refused.
- **Branch protection follows the branch's name.** The protected branch
  is stored when protection is turned on and moves only with the default
  branch (which now needs the password, above); before, the hook
  protected whatever the default branch named at the push, so an owner's
  API key could move it, force-push the old one, and move it back.
- **Git's work is shared fairly.** One account holds at most 6 of the
  instance's 16 git slots, one workspace 8, and clients together 12, so
  the panel's own git work always has room; a refusal says which limit.
  An idle session gives its slot back (see above).
- A remote command's reply is capped at 4 MiB, over SSH and through the
  node agent; a server answering more no longer fills the panel's memory.
- `/mcp` is rate-limited per address before its token is checked.
- Markdown autolinks (`<scheme:…>`) keep only `http`, `https` and
  `mailto`, as other links do.

### Servers

- A server's disk report names only its own workspace's services. On a
  server shared with other workspaces, it says how many of theirs there
  are and how much their rollback images keep, without naming them.

### Upgrading

- Migration 0080 adds `repositories.protected_branch`. A protected
  repository fills it at its next push. 0.45.4 runs on the new schema.
## 0.45.4 (2026-11-12)

Five fixes from a drill of every push flow with a real `git push`: a
merge whose preview built the same code deploys, git says why Toft
refuses it, and an untrusted preview can be approved once its server has
gVisor. No migration: a failed upgrade starts 0.45.3 again without
restoring the database. Upgrade from 0.45.3.

### Deploys

- **A merge whose preview built the same code deploys.** Merging a pull
  request removes its preview, and the removal could take the image the
  merge's build was reusing from cache: the deploy failed in a fraction
  of a second with `unable to find image`, and production stayed on the
  previous deployment. A build on the panel host that loses an image
  this way now builds once more, and its log says so.

### Git

- **git shows why a push or a clone is refused.** git shows a server's
  reason only as plain text, so it printed only `HTTP 403` or
  `Authentication failed`. It now prints the reason as `remote: …`: an
  expired API key (and when it expired), an agent's push password that
  expired or was revoked, agents paused (and where to resume them), a
  repository not found, a push a key or a role may not make. An agent's
  push is refused before it sends anything.
- A pull request opened with an API key, or by an agent, marks its first
  push in the timeline as a key's, as it does every later push.

### Previews

- **An untrusted preview blocked because its server has no gVisor asks
  for approval once it has.** It said "blocked", with no Approve button,
  until the next push to the pull request.

### API

- A project-scoped API key refused by its scopes gets 403 with `code`
  `insufficient_scope`, as the contract says and as an agent does; the
  answer had no code.
- Git's smart HTTP routes answer errors in `text/plain` (the new
  `GitRefusal` response), not as a Problem; only their rate limit (429)
  is still a Problem.

## 0.45.3 (2026-11-11)

Three security fixes, and a panel whose domain is still waiting for its
certificate now gets it without a restart and says why it hasn't yet.
No migration: a failed upgrade starts 0.45.1 again without restoring the
database. Upgrade from 0.45.1.

### Security

- **A compose stack's routes are checked before it starts** (SEC-010,
  high). The check on a stack's `traefik.*` labels ran only after a
  successful `docker compose up`, so a routed service whose image brought
  routers of its own was live from the moment it started, and stayed live
  when up failed. On a server shared between workspaces, or the panel
  host, a developer could serve another workspace's domain, or the
  panel's. Every deploy path (panel host, SSH, agent) now creates the
  containers, checks them and only then starts what was checked; a create
  that fails leaves the running stack alone.
- **A rollback sends the saved registry login only to its own registry**
  (SEC-011, medium). A rollback, and the certificate redeploy that runs
  one, pulled the old deployment's image with the service's saved
  registry password wherever that image was. A developer who once
  deployed an image from a registry of theirs could roll back to it and
  receive the password. Image names are now read as Docker reads them
  (`docker.io` and `index.docker.io` are Docker Hub).
- **A preview's copy can't be moved to the workspace** (SEC-013, low).
  The move let a copy leave its preview environment, after which its next
  deploy put an untrusted pull request's commit on every network of the
  workspace. It is refused with 409 `preview_service`.

### The panel's certificate

- **Toft has Traefik ask Let's Encrypt again.** Traefik asks only when its
  configuration changes, so a domain that started to reach the panel after
  Traefik's last try (port 80 opened, DNS moved, a restore) kept the
  self-signed certificate until something restarted Traefik. Every
  minute, while the domain reaches the panel without a trusted
  certificate, Toft has Traefik ask again: 15 minutes after the last try,
  the wait doubling up to a day, never two tries at once (also across a
  backend restart), and never while Let's Encrypt has rate-limited the
  domain, since asking then extends the limit.
- **The Problem says why.** Instance and the alert read Let's Encrypt's
  answer from Traefik's log: no DNS record, the domain points elsewhere,
  port 80 doesn't reach the server (and where to look: its firewall, the
  provider's firewall such as Hetzner Cloud Firewall, a router in front),
  something else answered, DNS or CAA, or rate-limited until when.
- The API's `PanelCertificate` gains `ask`.

### Services

- The new-service form deploys the repository's default branch, not
  `main`, until another is typed.

## 0.45.2 (2026-11-11)

- Not published: its release run failed a test.

## 0.45.1 (2026-11-11)

A panel restored from a backup is safer to bring up: its first account
stays closed while the restore runs, its backups stay paused until you
resume them, and it answers on its server's IP address until its
domain arrives. Toft now knows when the recovery kit you stored no
longer restores this panel, and says so. A service's deploys run one
after the other, and creating a service through the API stores its
template's settings and starts the first deploy. **A recovery kit
stored before this release shows as out of date once** on a panel with
backups set: open Show kit, store it and choose Mark as stored. No
migration: a failed upgrade starts 0.44.0 again without restoring the
database. Upgrade from 0.44.0.

### Restoring a panel

- **Security: the first account stays closed during a restore.**
  `install.sh --restore` set no setup code, so from the moment the new
  panel started until the backup was loaded, anyone who could reach
  the server could register as its instance operator. A restore that
  finished replaced that database; one that failed stayed open for as
  long as the server was left up. A restore now sets the code as an
  install does and shows it to nobody.
- **A wrong key says so in about a minute.** The restore used to show
  nothing for 15 minutes. It now reads the backup first, with a minute
  to answer, and gives the reason when it can't. A restore that stops
  says to run it again with the same kit; a kit with another
  encryption key needs a fresh server.
- **Backups stay paused on the restored server.** The restore no
  longer carries the old server's backup settings over, so a restored
  copy, a drill's included, never backs up into your bucket by itself.
  On the server you keep, an instance operator chooses **Resume
  backups** under Settings → Backups, with no key to enter. Until that
  server's first backup, the card says the last one is from before the
  restore.
- **The restored panel answers on its server's IP address** until its
  domain reaches it with a trusted certificate, which it looks for
  every minute. Your browser warns about the certificate there. The
  installer's last lines give the address, and Instance → Overview
  says so while it lasts. A panel that was never restored doesn't do
  this.
- **Stop answering on the IP…** on Instance → Overview ends it sooner,
  behind your password. Opened on the IP address, it first checks that
  the domain reaches this panel; when it doesn't, it stops only once
  you confirm that you can reach the server over SSH.
  `sudo toft answer-on-ip` on the server brings the IP answer back.

### Recovery kit

- **Toft says when the stored kit is out of date.** Setting up the
  panel's backups, changing their repository, its password or its
  access key, or rotating the encryption key changes what a restore
  needs, and nothing said so until a restore turned the old kit away.
  Instance, Home and the Inbox now tell every instance operator until
  the new kit is marked as stored. Toft keeps a fingerprint of the
  kit, never the kit.
- **Keep the old kit** while backups made before the change still
  exist: restoring one of those may need it.
- **Show kit** opens Instance → Recovery kit, behind your password,
  where it opened the last step of setup. **Mark as stored** is in the
  audit log, as every view is.
- The backups card and its password dialog say the kit holds the
  repository password, where they said it couldn't be shown again.

### Deploys

- **One deploy of a service at a time.** Two deploys asked for
  together both ran, so two rollouts could switch the same containers
  and its migrations could run twice. A second deploy is now accepted
  and waits its turn: it shows as pending, and its log says what it
  waits for. The panel, the CLI, a push and a rollback wait the same
  way, and every push still gets a deploy of its own. A deploy still
  waiting when the panel restarts is not started: it is marked
  interrupted, as one the restart cut short is, so deploy it again.
- **While a service moves to another server, another deploy of it is
  refused**, as is one asked for just as the move ends that was still
  aimed at the server the service left. Deploy it again once the move
  has finished. A push in that time gets a failed deployment that says
  why.
- **Create and deploy is one step.** A service from the catalog is
  created with its settings and its first deploy together. It took
  three calls, and a failure between them left a service without its
  settings or not deployed.
- A catalog app's temporary login made by the new-service form is kept
  for whoever created it: the admin password card shows it, where it
  had nothing to show.
- A deploy whose dependencies couldn't be checked says so, where the
  answer carried the database's own error text.
- After the panel restarts, a deploy that never started no longer
  stands in for its service's newest one.

### Backups

- The backups table's **Edit** fills the panel host's form.
- Backup dates show their time zone, and a first backup says it starts
  at the set time, or within 15 minutes if that has passed today.
- The guide's restore commands name restic by digest, as the backups
  do.

### Instance

- **No watchdog** is in an instance operator's Needs you, and Mark
  done puts it off for 7 days. Only Instance → Overview said so.
- The panel's certificate alert: a bad read of the certificate is kept
  for a minute, where it was kept for ten, long enough for three
  checks in a row to judge it.
  A certificate that couldn't be read leaves the alert as it is, where
  it closed as renewed and then fired again.
- A save of the panel's address whose routes can't be written is put
  back, and the save fails. It used to stay saved for the next restart
  to apply.
- Where Toft installs the Loki log driver (`install.sh`, and setting up
  a server that sends its logs to Loki), it installs it by digest. A
  server that already has it keeps it.
- The Docker advisories Toft ships with include the one Docker
  published on 2026-11-10 (GHSA-6jhp-7446-hf7f, high, fixed in Docker
  29.9.3). A panel that reaches GitHub had it from its daily read.
- **Security: built with Go 1.26.11.** Go 1.26.10 and earlier can
  crash an HTTP/1 server that limits the size of request bodies, as
  the panel does, when a handler reads the body while it writes its
  answer's headers (GO-2026-6859, CVE-2026-97054).

### API

**Breaking for Go code that imports `pkg/apiclient`:**
`ProjectServiceInput` is now `ServiceCreateInput` (the old name is an
alias), so its two enum types and their five constants are gone. Use
`ServiceCreateInputSourceType` with `DockerCompose`, `DockerImage` and
`Git`, and `ServiceCreateInputType` with `ServiceCreateInputTypeWeb`
and `ServiceCreateInputTypeWorker`. Every other constant keeps its
0.44.0 name.

Changed:

- All four creates take `variables`, stored with the service or not at
  all: `POST /api/v1/environments/{environmentID}/services`,
  `POST /api/v1/projects/{projectID}/shared-services` and
  `POST /api/v1/shared-services`, as the `service` of
  `POST /api/v1/projects` already did. A catalog service gets each
  plain variable of its template left out of `variables` at its
  default; one sent empty stays empty. A key sent as both a variable
  and a secret is refused and nothing is created.
- **`deploy: true` on a create now starts the first deploy**, as its
  caller and behind every gate of
  `POST /api/v1/services/{serviceID}/deployments`. The answer carries
  `deployment`, or `deploy_error` and `deploy_error_code` when the
  deploy was refused: the service stays created. A client that deploys
  after such a create is answered with that same deployment while it
  still runs, if its body overrides nothing and nothing it ships
  changed.
- One exception, until 0.46.0: a catalog create from a browser session
  with no `variables` in its body is not deployed by the create. A
  panel tab left open through the upgrade creates that way, then saves
  the settings and deploys by itself. Reload the tab. API keys and
  agents are not affected.
- `POST /api/v1/services/{serviceID}/deployments` and a rollback
  answer `409 service_moving` while the service moves between servers.
  A second deploy of a service is `202` as before and stays `pending`
  until the first has ended.
- `PUT /api/v1/instance/setup/domain` with the domain the panel
  already has ends a restored panel's answer on its IP address
  (audited as `panel.ip_answer_stopped`); with nothing to end it is no
  longer recorded as a change of address. A save whose routes can't be
  written is put back and fails (`panel.domain_change_failed`).
- The same deploy route answers `500` when the service's dependencies
  could not be looked up. It answered `409` with the database's error
  text; a dependency that isn't running keeps its `409`.
- `PATCH /api/v1/instance/setup` with `kit_acknowledged` needs
  `fingerprint`, from `GET /api/v1/instance/recovery-kit`: `400`
  without it, `409 kit_changed` when the key or the backup
  repository, its password or its access key changed since that read.
  Sessions only, as before.

Additive: `Setup.awaiting_domain` and `Setup.kit_out_of_date`;
`RecoveryKit.fingerprint`; `BackupDestinationDTO.paused` and
`restored_at` for the panel host; the Needs you kinds `no_watchdog`
and `kit_out_of_date`; the instance audit actions `recovery_kit.stored`
and `panel.ip_answer_restored` (`toft answer-on-ip`).

## 0.45.0 (2026-11-11)

- Not published: it needed Go 1.26.11 for a security fix in `net/http`.

## 0.44.0 (2026-11-09)

A page whose data failed to load could look empty, and saving from it
could delete what was really there. Variables and the other lists that
are saved whole now refuse a save made from a stale or failed read, and
a page that couldn't read something says so, where it used to show nothing,
a zero or a skeleton that never ended. Approving an
agent's deploy runs the commit you were shown. Setup won't lock you out
over a domain that doesn't reach the panel. **Scripts that replace
those lists through the API need a change:** see API. No migration: a
failed upgrade starts 0.43.0 again without restoring the database.
Upgrade from 0.43.0, and reload any panel tab left open through it:
until you do, its saves of those lists and of a project's issue settings
are refused, and nothing is lost.

### Lists that can't lose what you didn't see

- **A save needs the list it was made from.** This covers a service's
  and an environment's variables, shared volumes, compose limits,
  deploy policy exceptions, issue labels and hidden issue types. A failed read of variables or shared
  volumes used to show an empty list, and saving from it deleted what
  was there.
- **If someone changed it first,** nothing is saved. Each list says so,
  keeps what you typed or chose and offers to read it again; an issue's
  labels say so and ask you to try again.
- **A list that couldn't be read** says so, and Save waits until it
  has loaded.

### A page says when a read failed

- **Failed is no longer drawn as empty.** "Nothing needs you", "No
  deployments yet" on a running service, "0 of 0 vCPU", "No domains"
  and "No services" now appear only when Toft got that answer.
- A count that couldn't be read is —, never 0. Home's health strip says
  Unknown, and a server's Needs attention names what it couldn't check.
- A failed background refresh keeps what's on screen.
- If a whole page is refused, it says who to ask; if it's gone, it says
  so. Neither offers a Retry that can't help.
- **New service** says so when it can't read its servers or
  environments, and won't create until it can, where it used to offer
  only the panel host.
- **Previews:** Save waits until Toft knows which previews it would
  remove, so the dialog that names them can't be skipped.

### First run

- **Finishing setup checks the panel's domain first.** Finishing used
  to go ahead on a domain that doesn't reach this panel, which locked
  out the only operator. Now you can change the address or choose It
  opens: finish anyway.
- **A panel with no domain** gives its apps a temporary sslip.io
  address, where it used to say the server had no public address.
  `toft upgrade` records the IP on existing installs.
- A taken name for the first project is an error on the Name field,
  with a link to that project. Add a server says that opening the page
  makes the earlier command stop working.
- `install.sh` names the ports to open in your provider's firewall.

### Agent approvals

- **Approve runs the commit shown under Deploys,** so a push during the
  wait no longer ships unseen. Your note now reaches the agent on an
  approval, as it already did on a denial.
- Once decided, the page says if the run didn't start or the deploy
  failed. Needs you says what each request asks for.

### Logs

- **One very long line no longer ends the follow.** A line over 1 MiB
  is cut with a note, and the lines after it arrive.
- **A follow that ends while the service is still up reconnects by
  itself** for about a minute, showing Reconnecting…. Pausing stops it.
- A service deploying or failed with no container says so, where it
  said Disconnected.
- A line from another day shows its date. A line's own level beats words in its message, and "no error yet" no
  longer counts as an error.

### Pull requests

- **A merge never deletes the default branch or a branch a service
  deploys from.** The Merge card doesn't offer it.
- The Preview chip and card report the preview's real status, and a
  preview waiting for approval, waiting for room or still building
  offers no Stop.
- A commit refused for its Dockerfile reads as refused, with the reason.
- Merge choices and a half-written comment survive switching tabs.

### Keyboard, screen readers, contrast, phones

- **Keyboard:** focus returns to what opened a dialog, and to Edit when
  a pull request's title or description form closes. Most blocks that
  scroll (logs, code, wide tables) can be reached and scrolled from the
  keyboard.
- **Screen readers:** controls are named from the words they show, and
  a failed read is announced once.
- **Links inside sentences are underlined,** in every theme. Selected
  rows, line numbers and a selected deployment's status meet contrast
  in Paper.
- **Phones:** the page no longer scrolls sideways and long names wrap.
  Most Settings tables stack below 640 px, and small targets are bigger.

### API

**Breaking.** The versioning policy now says v1 may change in a minor
release before 1.0. These writes need the `version` from the read they were made on, sent
in the JSON body: `400 version_required` without it, and `409
changed_since_read` with nothing written if the list has changed.
Scripts that make one of these calls need updating (the exception
grant needs a signed-in session, as it did before); the CLI and the MCP
tools don't make them.

- `PUT /api/v1/services/{serviceID}/env-vars` and
  `PUT /api/v1/environments/{environmentID}/env-vars`: from their GET,
  where `version` sits beside `data`. A change to a secret doesn't
  change it.
- `PUT /api/v1/services/{serviceID}/shared-volumes`: from its GET.
  `mounts` is now required (leaving it out used to clear the list);
  `[]` clears.
- `PUT /api/v1/services/{serviceID}/compose-limits`: from
  `GET /api/v1/services/{serviceID}/resources`.
- `POST /api/v1/services/{serviceID}/deploy-policy/exception`: from
  `GET /api/v1/services/{serviceID}/deploy-policy`.
- `PATCH /api/v1/projects/{projectID}/issues/{issueNumber}` when it
  sends `labels`, and `PATCH /api/v1/projects/{projectID}/issue-settings`
  when it sends `hidden_type_ids`: from the GET or the last PATCH.

The 200 of each carries the new `version`, so a second write needs no
re-read.

Also changed:

- Merging a pull request with `delete_branch` when the head is the
  default branch or a deployed one: `409 head_kept`, nothing merged.
  These merges used to succeed. `PullRequestDetail.head_kept` says so
  beforehand.
- `PATCH /api/v1/instance/setup` with `finished` and a stored domain,
  and `PUT /api/v1/instance/setup/domain` with a domain after setup: `409
  panel_domain_unreachable`, nothing changed. `domain_checked: true`
  skips the check (`SetupPatch`, `SetPanelDomainInput`).
- `GET /api/v1/services/{serviceID}/logs/live`: `error` events
  `no_container: ...` and `server_unreachable: ...`.
- `PreviewService.error` also holds a refusal on an active preview,
  for a copy whose deployed commit isn't the approved one.

Additive: `AgentDecisionInput.commit` on
`POST /api/v1/agents/approvals/{approvalID}/approve` (the commit the person saw; on a deploy that names no commit, `400
invalid_input` if it isn't a full SHA; ignored otherwise).

## 0.43.0 (2026-11-09)

Workspace services take in Logs and metrics, the page that was
Observability. The Audit log and Issues are rebuilt; an issue In
progress is now amber and In review blue. Free disk space is what the
host says is available, in GB everywhere. **Marking a condition done
now puts it off for 7 days:** one marked more than 7 days ago comes
back after this upgrade. No migration: a failed upgrade starts 0.42.0
again without restoring the database. Upgrade from 0.42.0.

### Workspace services

- **Two tabs, Services and Logs and metrics.** Logs and metrics is the
  Observability page, which leaves the sidebar; `/observability` opens
  the tab.
- **Services:** how long each one's live version has run, how it is
  reached and who reaches it. **Who reaches them** says, server by
  server, which projects reach the services by alias and which only at
  an address.
- **Logs and metrics** names the servers whose logs stay local, with
  Set an external Loki address…, and says where each server's logs go.

### Audit log

- People, agents and API keys in one list by day, with search and
  filters for who, action and period. A link that narrows it to one
  person or agent shows as a chip you can remove.
- A refusal by the deploy policy is no longer red: red is for failures.
- An approval's row says what else it allowed, such as rolling back
  once to a commit.

### Issues

- **Rebuilt:** the list by status, tabs with counts, one + Filter menu,
  and the selected issue's panel from 1200 px wide (below that, a row
  opens the issue's page). The panel's History says which deploys a
  merge started.
- Backlog shows three, then Show N more; the other statuses load 50 at
  a time.
- **In progress is amber and In review blue** wherever an issue's
  status shows. They were blue and indigo.
- A row shows the part of a branch that names the issue's key; tapping
  it gives the full name and Copy.
- On a phone, Search is an icon until it is used.

### Fixes

- **Free disk space** on Server and Maintenance and in the upgrade's
  preflight is what `df` calls Available, in GB, from one reading. It
  counted the blocks reserved for root, so it read high.
- **Mark done on a condition that still holds** (no backups and the
  like) puts it off for 7 days, where it used to hide it for good.
- "Operator · since" says when the person became an operator, not when
  the account was created.
- Instance's audit log words the automatic disk clean-ups as sentences
  (a test now fails on any audited action without words), and its
  filters ask the server, so they cover every entry and "No changes
  yet" can't sit above more to load.

### API

Additive:

- `Service.live_since`: when the live deployment finished, null without
  one.
- `?api_key=any|none` on `/org/audit-log` and its count.
- `?kind=panel|accounts` on `/instance/audit-log`.
- `NeedsYouItem.snoozes` and `snoozed_until`, and `?done=any` on
  `/inbox/needs-you`.
- `PanelUsage.disk_free_bytes` and `DiskReport.free_bytes`.

The upgrade's step facts say GB, as in "38 GB free". They said GiB.

## 0.42.0 (2026-11-08)

The Instance pages are rebuilt, in six tabs, with a running or failed
upgrade shown above every one of them. Accounts says when each account
last signed in. The dashboard only: no migration and no API change, so
a failed upgrade starts 0.41.0 again without restoring the database.
Upgrade from 0.41.0.

### Instance

- **Six tabs:** Overview, Server, Maintenance (new), Accounts, Deploy
  policy and Audit log. Old `/instance?tab=…` links open their tab;
  `/instance/server#updates` and `#disk` open Maintenance.
- **Overview:** Needs attention, worst first (a container down, the
  certificate, a failed backup, failing watchdog pings, then a release
  that is out with Upgrade…, then what waits on Maintenance), the
  panel's containers, Health and Setup.
- **Server:** CPU, memory and disk in one Usage card, what the panel
  keeps, Isolation and the Watchdog; Database, Host and Networks beside
  them.
- **Maintenance:** Toft's version, with what's out and the four steps
  an upgrade takes, then Updates and Disk (moved from Server). The tab
  counts what needs action; a release never counts.
- **Accounts** (was Accounts and operators): Operators and Accounts, with search. Each
  account's status says since when it is an operator or who
  deactivated it, and when it last signed in.
- **Deploy policy:** one scroll with an index: Mode with when it was
  last checked and Check again, Would be refused by workspace,
  Exceptions with who granted each and why, Notes, and the rules.
- **Audit log:** entries by day, each a sentence naming who did it and
  the account it was done to, with search.
- **The upgrade card above every tab** while an upgrade runs, failed or
  rolled back, keeping the log's tail after a failure. After a failed
  or rolled-back upgrade, its Try again… is the one way to retry.
- On a phone, Upgrade is full width and Host shell moves into ⋯.
- Needs you's Full log for a failed upgrade opens Maintenance with the
  log.

## 0.41.0 (2026-11-07)

The server pages and Workspace settings are rebuilt. Only the page
scrolls now, never the window around it; a link to a section lands on
it; issue types coloured red show their colour again. The dashboard
only: no migration and no API change, so a failed upgrade starts 0.40.0
again without restoring the database. Upgrade from 0.40.0.

### Servers

- **Servers rebuilt:** each server one block with its status, who it is
  shared with, its address, size and agent, the CPU and memory set
  aside per workspace and the disk used (amber from 85%), what runs
  there, and what is wrong in words with where to fix it (a failed
  backup, nothing backed up, a full disk and what would free it,
  provisioning gaps, CPU waiting, little room left). Capacity and Will
  it fit? beside the list.
- **A server's page in three tabs:** Overview (Needs attention, worst
  first, each row opening its card; Capacity; Running here; Connection,
  Traefik and Backups at a glance), Maintenance (Updates, Disk,
  Provisioning; the tab counts what needs action) and Settings
  (Connection, Move to the agent, Isolation, Traefik, DNS provider,
  Firewall and ports, Backups, Capacity overrides, Sharing, Remove).
  Maintenance and Settings are one scroll with an On this page index.
  Old links to a card (`#backup`, `#disk`, …) open its tab.
- Instance → Server's Updates and Disk cards take the same look.
- **Removing a server's DNS provider asks first**, and says that
  wildcard deploys are refused from then on.
- Disk space reads in GB everywhere.

### Workspace settings

- **Rebuilt as one scroll** with an On this page index: Members with
  the pending invites in the same card, General with a save bar only
  while something changed, Isolation, Issues as a table of types,
  Cycles, Estimates, and Leave or delete.
- **Choosing an isolation level that would refuse services warns
  first** (D-138): how many servers can't run it, which services would
  be refused, and Change anyway or Keep the current level.

### Fixes

- Past the end of a page the window scrolled too, sliding the whole
  panel up over an empty strip, and a trackpad swipe over the sidebar
  could bounce it. Only the page scrolls now.
- A link to a section of a page (`/settings#cycles`, a server's
  `#backup`) lands on it once the page has loaded, not hundreds of
  pixels away; below 1200 px a section lands below the Jump to bar, not
  behind it.
- Issue types coloured red, such as Bug, showed an empty tile since
  0.34.0.

## 0.40.0 (2026-11-07)

Only a signed-in person merges a pull request now: **an API key's merge
is refused**. Needs you is one list in severity order on Home, the Inbox
and the sidebar, **and no longer carries a release**; a failed panel
upgrade is in it for instance operators. The pull request page, Home,
the Inbox, a service's Deploys tab, a deployment's page, Projects and
Domains are rebuilt. No migration and no contract step: a failed upgrade
starts 0.39.0 again without restoring the database. Upgrade from 0.39.0.

### Breaking

- **An API key can no longer merge a pull request** (D-102). `POST
  …/pulls/{number}/merge` with any API key, including an owner's
  full-access key, now answers `403 session_required`; agents were
  already refused. Merging is a person's decision: an owner or developer
  merges from the pull request's page while signed in (Merge, on the
  Conversation tab). A script or CI job that merged with a key should
  leave the pull request open and let a person merge it; nothing else
  that key does changes. Pull requests a key opened are unaffected and
  can be merged from the page.
- **Needs you no longer lists a release, and its `release` kind and
  `version` field are gone** (D-140). `GET /api/v1/inbox/needs-you` never
  returns an item of `kind: release`, and `NeedsYouItem.version` is
  removed; a new release shows only as the sidebar's Instance chip, and
  `GET /api/v1/instance/upgrade` (`latest`, `available`) says it. A
  client that read the release from Needs you should read the upgrade
  status instead. The list is also ordered by the new `severity`
  (problem, waiting, warning, request), then newest, not by time alone.

### Code

- **The pull request page rebuilt** (D-97, D-102). A commit waiting for
  a person leads the conversation for whoever may approve it (who
  pushed, the commit, Review the diff, Approve); the header names an
  agent for its person; tabs Conversation, Commits, Checks and **Files**
  (the Changes tab's new name; `?tab=changes` still opens it). The Merge
  card lists what merging needs (who merges, the head's checks, the
  preview's waiting checks); beside it Preview, Checks on the head
  commit, About (an agent's connection and its branch password's expiry)
  and Check it out locally.

### Home and the Inbox

- **One Needs you** (D-140), in severity order (problems, an agent's
  request, warnings, requests), with the same items and number on Home,
  the Inbox and the sidebar. Home: Create and Deploy…, a strip of state
  only, Needs you capped at 4 (failed deploys, offline servers and failed
  backups always shown), Projects with each project's issues, pull
  requests and docs. One action per row: approve a preview or deny an
  agent's request from its page.
- **A release is no longer a Needs you item:** the Instance chip says it.
- **A failed panel upgrade is a problem in Needs you** for instance
  operators: "Upgrade to vX failed and rolled back · Full log", until a
  later upgrade succeeds or it's marked done; Instance's Version card
  says "Last upgrade failed".
- **A server that kept its own state after a panel restore, and a
  crash-looping service, are problems** in Needs you, no longer warnings.
- **Free space…** on a disk row opens its dialog in place. **New issue
  in <project>** in ⌘K.

### Deploys

- **A service's Deploys tab rebuilt:** filters All, Live, Failed and
  **Refused** (a deploy the policy refused before it started, no longer
  counted as failed); each row says who started it and how it went; Why,
  Roll back, and on phones a menu with Redeploy and Copy SHA; "How
  <service> deploys" beside the list.
- **A deployment's page rebuilt:** the pipeline with each step's time,
  what failed or why it was refused, a log that opens on its warnings
  and errors, Details with Copy link, and the deploys around it.
- A project's Recent deploys read a refused deploy as **Refused**, as the
  Deploys tab does, not Failed.

### Projects and Domains

- **Projects rebuilt:** sorted by what needs you first; each project's
  environments with its previews, where they run, production's services
  as a bar, its issues, pull requests and docs, and its last deploy; the
  workspace services at the foot. New project says it starts with a
  protected production.
- **Domains rebuilt:** Waiting for DNS with the record to create, Copy
  record and Check again; All domains by project with where each routes,
  its DNS and its certificate (until when, and from whom); Certificates
  by server and Elsewhere beside them. Add a domain and New project open
  as full-screen sheets on phones.

### Fixes

- Toft's own HTTPS redeploy reads "by Toft", not "by certificate
  issued"; an image deploy's chip says Pulling, not Building.
- Home showed nothing that needed you while setup wasn't finished; it
  counted a server still waiting for its agent; the No backups row's age
  restarted when a service was added.
- Add a domain opened from a link before the services loaded kept its
  buttons disabled and showed no DNS record.

### API

Breaking (above): the merge route refuses API keys (`403
session_required`); `NeedsYouItem.version` and `kind: release` are gone.

Additive:

- `NeedsYouItem.severity` (`problem`, `waiting`, `warning`, `request`),
  and `kind: upgrade_failed`.
- `PullRequestDetail.agent_branch` (`agent`, `person`,
  `password_expires_at`) when the head branch is an agent's.
- `RecentDeployment.started_at` in a project's overview, null for a
  deploy that never started.

Changed:

- Needs you is ordered by severity, then newest; `agent_frozen` and
  `service_restarting` alerts are `problem`.

## 0.39.0 (2026-11-06)

A catalog app's admin password can be shown again to whoever created it,
and reset; deploys into a protected environment show what goes live
first; the Variables tab and an agent's request page are rebuilt. Only
workspace services become the workspace's Loki, Grafana and Prometheus
now: **a project's one already registered stays, and Observability asks
what to do with it.** A new table (0078) and new columns (0079); no
contract step: a failed upgrade starts 0.38.0 again without restoring
the database. Upgrade from 0.38.0.

### Catalog

- **Your app's admin password, until you dismiss it** (D-160). Creating
  a catalog service (or resetting its password) keeps that one admin
  password, or temporary login, sealed for the person who did it, for 7
  days or until they dismiss it; then it is deleted. The service's
  Overview shows it to them while they are an owner or developer, after
  they confirm their own password. Other secrets stay write-once. Agents
  and API keys never see it. Reading and dismissing it are audited,
  never with the value.
- **Reset the admin password** of Grafana, Umami, pgAdmin and RabbitMQ
  from the service's Overview: Toft generates a new one, stores it and
  runs the app's own command in the running container (the password
  goes on stdin, never in a command line or a log). MinIO's applies at
  the deploy its dialog starts. If the app refuses, nothing changes, and
  you see its own message. pgAdmin's image is pinned.

### Deploys and services

- **A deploy into a protected environment shows what goes live**
  before you confirm it (D-143): what runs now, what it deploys (the
  commit, its message, branch and checks), the commits it takes live
  with their authors, a commit whose own deploy failed there with its
  error, the variables, secrets and domains changed since the live
  deploy, what users see while it rolls, and who else is deploying in
  that environment. The same applies to Deploy, roll back, Redeploy,
  ⌘K and the Domains page; on phones it fills the screen. For a service
  building from the project's repository, it deploys exactly the commit
  it showed.
- **Rolling back to the live deployment is a Redeploy**, and is
  recorded and audited as `deployment.redeploy`.
- **Add a domain** has Add only and Add and deploy, on both forms.
- **The Variables tab, rebuilt** (D-145): one list of everything the
  container gets, each row naming where it comes from (the
  environment's, its own, or overriding the environment's), with
  Override and Edit in place and one Save. Secrets sit in one table;
  replacing an environment's secret gives the service its own copy. A
  line names what was saved since the live deploy and isn't live yet.
  Import .env lists what it adds, changes and refuses before it merges.
- **A workspace service's network alias must be its own** across the
  workspace, since it joins every network there: creating one with an
  alias another service uses is refused (`409 alias_taken`).

### Agents

- **An agent's request page, rebuilt** (D-145): the state and when it
  expires or who decided, the agent's reason and plan with Toft's marks,
  and what Toft does if you approve: for a deploy or roll back, the same
  "what goes live" as the protected confirm, and the rollback the
  approval also allows. The note is one line, and on phones Deny and
  Approve stay pinned to the bottom. A decided request ends with what
  happened. The sidebar marks Inbox on it.
- **What an agent did names the agent** (D-100). A rollback or redeploy
  an agent ran reads "Rollback · Agent · Claude Code for Ada", not the
  person alone; a preview it started names it in the pull request's
  history; a variable or secret it set shows "Claude Code for Ada" where
  it says who set it. Changes made before this release keep the person's
  name.

### Observability

- **Only workspace services become the workspace's Loki, Grafana and
  Prometheus** (D-162). A catalog one deployed in a project no longer
  takes over the workspace's (for Loki, every project's logs) on each
  deploy. One registered before stays, and Observability shows it as
  Needs attention with **Move to workspace** (keeps its id, volumes and
  domains, and copies the shared variables and secrets it uses) or
  **Unregister** (it keeps running as the project's).
- Project Settings' Logs and metrics finds the workspace's Grafana,
  Loki and Prometheus wherever they run.

### Sign-in

- **Signing in counts as confirming your password for 5 minutes**, in
  that tab (D-161): the setup wizard and anything else that asks for
  the password in those minutes doesn't ask again.

### Fixes

- A one-shot container that exits (Umami's `admin-password`) ends its
  live logs as finished, not as a lost connection.
- An Umami deploy on an agent's server no longer logs six "variable is
  not set" warnings.
- The HTTPS Inbox row for a service behind a temporary login says its
  address already redirects to HTTPS.
- Project Settings: Previews' hint no longer reads "A eighth waits", the
  Previews switch has an accessible name, and Copy project ID is back in
  General.

### API

Additive:

- `GET`, `DELETE /services/{id}/admin-password` (whether you have a copy,
  dismiss it) and `GET …/admin-password/value` (a signed-in session and
  `X-Toft-Reauth`); `POST /services/{id}/admin-password` resets it
  (`502 reset_unconfirmed` when the app didn't answer: the new password is
  stored).
- `POST /services/{id}/move-to-workspace`; `DELETE
  /observability/{loki,grafana,prometheus}`.
- `POST /auth/login` and `/auth/register` answer with `reauth`.
- Variables and secrets: `updated_by` names the agent acting for its
  person.

Changed:

- `PUT /observability/{kind}` refuses a service that isn't a workspace
  service (`400 not_workspace_service`).
- Creating a workspace service with an alias the workspace uses: `409
  alias_taken`.
- A rollback to the live deployment is audited as `deployment.redeploy`;
  an agent's deployment reads `… · Agent · <agent> for <person>` in
  `triggered_by`.

## 0.38.0 (2026-11-05)

Catalog services no longer start with a login anyone knows, and the
project pages (Overview, Services, Environment, Settings) are rebuilt.
**Change admin/umami on any Umami created before this release.** New
columns and an index (0077); no contract step: a failed upgrade starts
0.37.0 again without restoring the database. Upgrade from 0.37.0.

### Catalog

- **No catalog entry starts with a login the internet knows.** A catalog
  service created through the API without its passwords gets them
  generated, as the New service screen already did, stored write-once with
  the service and returned once in the create's response as
  `generated_secrets`: Grafana no longer starts as admin/admin, MinIO as
  minioadmin, RabbitMQ as guest/guest, MongoDB without authentication. The
  three creates take `secrets` too, written in the same transaction as the
  service (a refused secret leaves no service); the screen sends them
  there instead of after the create. API callers that set a catalog
  secret after creating get a conflict now: send it in `secrets`, or
  replace the generated one.
- **Umami's admin/umami is replaced at its first start.** A short-lived
  `admin-password` service in Umami's stack sets the generated
  `UMAMI_ADMIN_PASSWORD` on its admin account, and only while it is still
  umami, so a password changed in Umami is kept. Umami services created
  before keep their compose file: **change admin/umami on any of them
  now**.
- **Uptime Kuma, Plausible, n8n and Ghost start behind a temporary
  login.** Whoever opens one first becomes its admin, and scanners reach a
  new address within minutes. The create puts basic auth in front of it
  (user `toft`, a generated password, shown once on the screen or as
  `temporary_login`), and Needs attention asks to turn it off in
  Settings → Access once the app's admin account is set up. The catalog's
  templates say so as `setup_login`.

### Projects

- **The project pages, rebuilt** to the new design (7g steps 17 to 20):
  - **Overview:** a health line; Needs attention (the Inbox's rows for
    this project); Recent deploys with a menu on each row; Connected work
    (open pull requests and urgent issues). Beside them: the services and
    project services, the environments with their previews, and the
    workspace services.
  - **Services:** one environment, or every one, a card each, with the
    service, its status, its last deploy and Deploy. Over 8 services, a
    filter box (name, image, source or domain) and All / Failing /
    Stopped, kept in the URL.
  - **Environment:** shared variables (read until Edit), shared secrets,
    what each service sets (shared, own, overrides), and Delete behind a
    typed name. Deploy buttons are on Services only.
  - **Settings:** one page with an index. Previews take a limit of
    their own (1 to 20, was 5 for every project). Deploy on push shows
    the last push and its repository. API keys lists the project's keys
    with what each can do and whose it is: an owner sees every member's.
    Delete opens a dialog that asks for the name.
- Deleting a service opens the project in that service's environment.

### API

Additive:

- `GET /projects/{id}/api-keys`: the keys scoped to a project, newest
  first, paged with `?limit` and `?cursor`. Owners get every member's,
  other roles their own. Never the key itself.
- `PUT /projects/{id}/previews/settings` takes `max_per_project` (1 to
  20; omitted keeps the project's). A higher limit starts the previews
  waiting for room; a lower one stops none.
- `GET /projects/{id}/webhook` (and `…/rotate`) gains `last_push_at` and
  `last_push_repo`: the last signed branch push GitHub delivered.
- `GET /environments/{id}/overview`: each service gains `own_variables`
  and `own_secrets`.

## 0.37.0 (2026-11-05)

p95 latency on the Service page, measured at each server's Traefik, and
Traefik's logs capped. **Every Traefik Toft started restarts once** after
the upgrade (a few seconds' pause on 80 and 443). New table
`service_latency` (0076); no contract step: a failed upgrade starts 0.36.0
again without restoring the database. Upgrade from 0.36.0.

### Catalog

- **Mailpit replaces MailHog.** MailHog's image runs on amd64 only and is
  no longer maintained; Mailpit (`axllent/mailpit` v1.33.0, pinned by
  digest) catches mail on the same ports, SMTP 1025 and its web UI on 8025,
  on amd64 and arm64, with a 64 MiB default. MailHog services already
  created keep running and are shown, sized and connected to as before.
- **The catalog image check requires amd64 and arm64.** An image without
  both fails `scripts/check-catalog-images.sh` (CI and the daily run); it
  was only printed.

### Fixes

- **A compose stack switches to HTTPS by itself again.** In 0.36.0, a
  compose stack (a catalog app such as Umami) given a temporary address
  stayed on plain HTTP after its certificate arrived, with "HTTPS waits for
  a deploy" in the Inbox: what its first deploy recorded about its route
  counted as a setting changed since. A stack already waiting keeps that
  Inbox row until it is deployed once. A stack redeployed on 0.36.0 whose
  certificate arrives after the upgrade also gets the Inbox row once (its
  recorded fingerprint included the field), and is switched to HTTPS on its
  next deploy. What counts as a setting is now an explicit list, and a new
  service field or column fails the tests until it is put in it or left
  out on purpose, so one Toft writes can't hold back the switch again.

### Service page

- **p95 latency is back on Overview, measured** (D-156). The Usage facts
  show a routed service's 95th percentile request time over the last 24
  hours, "≈72 ms" interpolated within the bucket it falls in (a click, a
  tap or Enter on it opens the bucket's bounds), with the request count;
  under 20 requests the
  count alone. A stack's is its routed compose service's. Each server's
  Traefik keeps the durations in 12 buckets, read with the minute sample
  (and kept by the agent while the panel is away); answers a middleware
  gave first (basic auth, an IP allowlist, a redirect) and SSE and
  WebSocket streams aren't counted. Where it can't be measured the tile
  says why: a Traefik Toft didn't start, or an agent not upgraded yet. A
  service without a domain shows none. A sampling gap over 24 hours starts
  the count again rather than counting the gap. New table `service_latency` (0076,
  48 hours); `GET /services/{id}/resources` gains `latency_24h`.

### Servers

- **Each Traefik Toft started restarts once.** Its metrics are on an
  entrypoint bound to its container's loopback (never published, never on
  80 or 443), so the health poll restarts every Traefik Toft started, once,
  within 5 minutes of the upgrade, as a provisioning run: its routes pause
  for a few seconds. This restart also applies the capped logs below. An
  adopted Traefik is left as it is; after a failed provisioning run the
  restart waits for Re-provision. The panel's Traefik gets the same
  buckets, applied when `toft upgrade` recreates it.
- **Traefik's logs are capped.** Its access log was a file on each server
  and the panel host that nothing rotated; it now goes to the container's
  log, which Docker's local driver caps at 20 MiB × 5, as every app's is.
  `docker logs paas-traefik` shows it. The old file is deleted when
  Traefik is next started by provisioning or `toft upgrade`.
- **Every panel container's log is capped** the same way (20 MiB × 5,
  Docker's local driver): the backend, the web app, Postgres and git kept
  everything they ever wrote. `toft upgrade` recreates them with it, so
  the panel's Postgres restarts once during the upgrade.

## 0.36.0 (2026-11-05)

The fixes of the 0.35.0 QA sweep's 14 majors. **No contract step:** a
failed upgrade starts 0.35.0 again on the new schema without restoring the
database. Upgrade from 0.35.0. Two response fields are deprecated (see API);
nothing needs action.

### Servers

- **Add a server makes one join command per visit.** Opening the page
  directly (typed, bookmarked or reloaded) made a new command on every
  render, hundreds a visit, each with its audit row. The upgrade deletes the
  extra rows such a visit left, keeping the first of each burst and every
  rejoin, and the join tokens no command can use any more.
- **The Firewall card shows the server's own address pool,** as Docker on
  the server reports it, or **not set** with where the fix is. It showed
  the pool Toft writes on fresh servers, whatever the server had. Instance →
  Server's Networks card reads the panel host's Docker the same way.
- **Under Only Toft, nothing is created on the panel's server.** Home's
  first app and New service no longer offer it; with no other server they
  say so and lead to Add a server. The API answers 409 `panel_host_no_apps`.

### Services and deploys

- **Deploy deploys what went live.** A version whose deploy failed no
  longer stays the service's saved image (or its own `IMAGE_TAG` on a
  compose stack): Deploy, and the header's Source, go back to the live
  version, or the latest that went live. A rollback becomes the saved
  version, and Deploy on a stopped service brings that one back. Deploy
  says what it deploys ("Deploy whoami:v1.10"). A saved image is never
  swapped for another registry's, and a refused Deploy a version puts back
  the registry password it cleared.
- **Create and deploy doesn't create a service that won't fit.** It checks
  the server's room first, compose stacks from the catalog included, and
  refuses with what's needed and what's left; a role that may deploy past
  the limit gets **Create and deploy anyway**. The Summary warns before the
  click. Every capacity refusal now carries a readable message.
- **One address for other services.** The hint on New service, the Network
  card, the services table and the project pages show the name other
  services actually reach: `whoami-qa:80`, or a compose stack's routed
  service (`umami:3000`), and nothing for a stack that isn't on the network.
- **A service's own secrets are on its Variables tab,** with Add a secret
  when it has none. They were reachable only when the environment shared
  secrets.

### Catalog

- **MinIO comes from PGSTY Silo** (`pgsty/silo`), a maintained fork with the
  same variables, ports and data layout: `minio/minio` is gone from Docker
  Hub. Services created from the old entry keep `minio/minio` and fail on a
  redeploy that pulls; set their image to the catalog's to fix them, the
  data volume works as it is.

### Audit log

- **Every Settings save is in the audit log,** naming the fields it changed
  and never a secret's value: the config cards, domains added and removed,
  config files (by path), shared volumes, moving to another server, and a
  volume's data destroyed.

### Web

- **When Toft can't be reached, pages say so.** One "Toft isn’t answering"
  state with Retry replaces the page while the backend is down (an upgrade,
  a restart), instead of "Project not found", "0 servers" or an empty page,
  and Code says when git can't be read instead of loading forever. A page
  whose own data loaded stays.
- **Sign out other browsers keeps this one signed in.**
- **Dialogs return focus to what opened them** (WCAG 2.4.3).

### API

- Services have `connect_to` (`host`, `port`), the address other services
  use; absent when nothing answers there.
- `ServerCapabilities.address_pools` and PanelHost `app_address_pools`: the
  Docker default address pools, `null` before they were read.
  **Deprecated:** ServerResponse `address_pool` and PanelHost
  `app_address_pool`, kept for one release and filled from the first pool.
- The service creates take `deploy` and `force`: with `deploy`, a service
  that won't fit is refused with 409 `capacity_exceeded`, and nothing is
  created. Capacity refusals carry RFC 9457 `title`/`detail`/`error`.
- 409 `panel_host_no_apps` on a create on the panel host under Only Toft.
- 503 `git_unavailable` when the git server can't be reached.
- New audit actions: `service.settings_changed`, `service.domain_added`,
  `service.domain_removed`, `service.config_file_saved`,
  `service.config_file_deleted`, `service.shared_volumes_changed`,
  `service.server_changed`, `service.volume_purged`.

### Upgrade

- `toft upgrade` counts a contract step only when `contract.sql` holds a
  statement; this release's holds none, so its log says "nothing dropped".
- CI checks that every catalog and template image can still be pulled,
  daily and when the catalog changes.

## 0.35.0 (2026-11-04)

**This release has a contract step** (below): it tightens two database
constraints, so a rollback that runs after the step restores the database
from the dump taken before the upgrade, and loses what was written during
the upgrade. Upgrade from 0.34.0. **One API change needs action:** a script
that deletes services must add `?confirm=<name>` (see API).

### Servers

- **Add a server is one page,** from the install command to **Use this
  server**. The server waits under Servers as one line until its agent
  joins, then installs by itself. It is named after its host, and its size
  is read before it shows as provisioned. **SSH instead** gives a key Toft
  made, for root only, then the host-key check. A waiting server can't be
  chosen anywhere: the API answers 409 `server_not_ready`.
- **Remove server asks first** once the server has joined, and names what
  stays on the machine.
- **Provisioning that leaves something out says so.** A run whose
  firewall, host firewall, nightly backups or automatic security updates
  couldn't be set up still provisions the server, but finishes **with
  warnings**. The warnings show on the server's card, under Needs attention
  and on the server page, with Re-provision.
- **A server nothing backs up needs attention:** one running a database,
  a compose stack or any volume, with no backup destination, shows under
  Needs attention on Home, in the Inbox and on its page, with **Set up
  backups**. Before, only a new workspace's checklist mentioned backups.
- **No app network address pool is a provisioning warning.** Toft gives
  app networks no subnet of their own, so without `default-address-pools`
  in `/etc/docker/daemon.json` they come from Docker's defaults: about 30
  networks, which can run out or clash with the server's LAN or VPN. The
  server page shows the exact fix under Needs attention; Toft never
  restarts Docker by itself.
- **The agent ships logs only from within Loki's 7-day window.** On its
  first start it sent every log Docker held, which Loki refused as too old.
- **A Docker install that didn't install Docker stops the run**, instead of
  failing later at "docker: command not found".
- **`install.sh` prints one line per check:** the server, the ports,
  Docker, Toft started, then where to finish. The rest goes to
  `/var/log/toft-install.log` (root only). A held port 80, 443 or 2222 names
  its process. A cut-off download of `install.sh` or the agent's installer
  runs nothing.
- `deploy.sh` is gone. Production installs and upgrades through
  `install.sh` and Instance → Upgrade. `install.sh --adopt` stays for a
  panel that `deploy.sh` set up.

### Services

- **New service** is one form: catalog tiles, then a sticky Summary with the
  address, the environment and **Create and deploy** or **Create only**. In
  a protected environment, Create and deploy asks first.
- **The service page has five tabs:** Overview, Deploys, Logs, Variables,
  Settings. Old `?tab=` links still land.
  - **Overview** shows the live deployment, usage over 1 h, 24 h and 7 d
    against the limit, the health check, **Restarts, 24h** and uptime.
  - **7 d draws what 1 h and 24 h do:** the peak of the summed containers,
    per day in UTC. Days summarised before this release show the largest
    single container's peak, marked as such, until they age out.
  - The p95 latency tile is gone until latency is measured.
- **Logs** shows coloured levels with counts and the last 500 lines, with
  Pause and Open in Grafana. ANSI colours show in log lines. On phones the
  list grows with the page and has **Jump to latest**.
- **Settings is one scroll of open cards:** General, Source (credentials
  included), Domains, Access, Resources, Deploy, Health check, Storage
  (volumes, shared mounts, config files), Hooks, Isolation, Deploy policy,
  Delete. From 1200 px an **On this page** index sits beside them, and
  **Jump to** below it. The Domains and Config files tabs fold into it, and
  old `#set-` links land on their new sections.
- **A temporary address switches to HTTPS once its certificate is valid.**
  The link and the header change at once, and the Domains table, the
  header and the links read the same record. Toft then redeploys the
  service on the image it runs, once, so HTTP redirects to HTTPS and
  `TOFT_URL` is `https://`. It ships only what the live deployment runs,
  with only the routing changed: never in a protected environment, never
  with settings saved since that deployment, and a compose stack only when
  every image in its file is pinned by digest, as the catalog templates
  are. Otherwise Needs attention shows **Redeploy <service> to switch it to
  HTTPS** until you deploy it; so does a service last deployed before
  0.35.0, which recorded none of this. Deploy history and the audit log show
  Toft's redeploy as by Toft. The panel's log says why a name has no
  certificate yet, once each time the reason changes.
- **Saving a Settings card changes only what you edited** in it. Before,
  a card's first save could also rewrite fields nobody touched (an `auto`
  build pack became `dockerfile`).
- **Turning basic auth off forgets its password.** Setting a user again
  needs a new one.
- **Deleting names the data it deletes:** each volume a service,
  environment or project delete removes, with its size.
- **Databases and admin tools say so:** a database or cache typed in by
  image shows as Database, an admin tool as Tool.
- **An admin tool anyone can open needs attention:** pgAdmin, phpMyAdmin,
  Adminer, MailHog or RedisInsight with an address and neither basic auth
  nor an IP allowlist. It shows in the Inbox and on its page, in amber.
- **The sidebar's Inbox badge counts what the Inbox shows.** It used to
  lag the page by up to two minutes.
- Granting or revoking a deploy policy exception asks for your password
  in words of its own, not the host shells'.
- A pinned image shows `image:tag`, with the digest on hover. Each
  environment's Overview lists its own recent deploys, and "N running"
  counts its own services.

### Agents

- `get_service` returns the service's domains. Agents and deploy-scoped
  keys may read `GET /services/{id}/domains`.

### API

- **Changed:** `DELETE /services/{serviceID}` needs `?confirm=<service
  name>` when the service or a preview copy holds data, or its server can't
  be read. Without it, the answer is 400 `confirm_name`. This applies to
  sessions and workspace API keys alike.
- **Changed:** `GET /servers` no longer takes `with_services`, which it
  never read. Its 200 schema is now `SharedServerResponse` (own and shared
  servers).
- **Changed:** `PUT /services/{serviceID}/config` with an empty
  `basic_auth_user` also clears the stored basic auth password.
- New: `GET /services|environments|projects/{id}/data` (`?sizes=false`
  for the names at once); `PATCH /servers/{serverID}` (rename);
  `POST /servers/{serverID}/ssh-key` and `PUT /servers/{serverID}/ssh`.
- 409 `server_not_ready` wherever a server still waiting for its agent is
  named (create, move, deploy, address, fit).
- `Service.kind` (database, cache, tool, app; display only).
  `ProvisionRun.warnings` and `ServerResponse.provision_warnings`. The
  inbox kind `exposed_tool`.
- The inbox kinds `no_backup` and `https_redeploy`.
  `ProvisionRun.warnings` can name the app network address pool, and
  `ProvisionRun.address_pool_fix` says how to set it.
- `GET /services/{id}/metrics?range=7d`: one point per UTC day, each with
  `largest_container`. `GET /services/{id}/resources` rows carry
  `restarts_24h` and `restarts_since`.

### Migrations and data steps

- Migrations 0067 to 0070 and 0072 are additive: `servers.name_automatic`,
  `server_provision_runs.warnings`, `service_metrics.restarts`,
  `service_usage_daily_sums` and `deployments.settings_fingerprint`.
- **The contract step, 0071:** environments refuse the colour red (gone
  since 0.34.0), and a preview's copy refuses a migrate or pre-deploy
  command (none has had one since 0.34.0). The data steps before it run
  again: red environments turn gray, and every preview copy loses those
  commands, including a copy whose preview is gone.
- Rolling back to 0.34.0 works on this schema. An upgrade that fails its
  health check rolls back by restoring the dump taken before it. That
  restore now runs in one transaction: before, it couldn't drop a table a
  newer one references, and stopped half-way with the panel down. If it
  fails, nothing changes, and the previous version starts on the new
  schema.

## 0.34.0 (2026-11-03)

**This release has data steps that can't be undone** (below). **The next
release has a contract step:** it tightens two database constraints. Upgrade
to 0.34.0 before going past it.

### The first run

- **Setup is four steps:** Your account (on an empty instance), Address,
  This server, Recovery kit. Backups, more servers and invites move to
  Home's **Finish setting up** checklist. `/setup/backups`, `/setup/servers`
  and `/setup/invite` are gone. `/register` is for invite sign-ups only, and
  a first run goes to `/setup`.
- **Home welcomes a new workspace** until Finish setting up is complete or
  an owner hides it. **Deploy your first app** takes your code, one of five
  catalog apps or an image. Once the app is live, the card becomes one line
  with its address. Workspaces that already run a service skip the welcome
  (see Data steps).
- **A new web app gets an address when it is created:** a temporary name
  under sslip.io (`<service>-<env>-<project>.<a-b-c-d>.sslip.io`), labelled
  **Temporary** everywhere it shows. Until its certificate arrives, it answers
  over HTTP and shows **Certificate pending**. sslip.io shares one Let's
  Encrypt limit with everyone, so add your own domain for anything with
  logins. Databases, caches, admin tools and workers get no address.
- **Every new password needs 12 characters:** registering, accepting an
  invite, changing it, or an operator reset. Shorter existing passwords still
  sign in.

### Apps and logs

- **Five apps in the catalog as Compose templates:** Uptime Kuma,
  Plausible, n8n, Ghost and Umami. Their images are pinned by digest. A
  Compose deploy gets `TOFT_URL`, the service's address (a temporary one is
  `http://`), unless it sets its own.
- **Loki from the catalog collects its server's logs** through a port on
  `127.0.0.1`, so a single-server install has central logs by default.
  Services on other servers still need an external Loki address for now.
- **pgAdmin listens on 8080**, because no-new-privileges moves it off 80.
  Each admin tool gets its own memory default: pgAdmin 512 MiB, RedisInsight
  256, phpMyAdmin 128, Adminer 64, MailHog 256.
- **New admin tools typed in by image** (pgAdmin, phpMyAdmin, MailHog,
  Adminer, RedisInsight) are treated as tools: no address, their memory
  default, and Sandboxed only where the server runs gVisor. Existing services
  keep their kinds.
- **Previews never run migrate or pre-deploy commands.** Before, only a copy
  of a protected environment skipped them. The pull request lists the
  migration files its diff touches, warns that post-deploy commands and
  image entrypoints still run, and says when a diff is too large to check
  (over 3,000 files).

### Agents

- **Approving an agent's deploy also lets it roll back** to what was live,
  once, within 30 minutes of the approved deploy going live, and only while
  that deploy is still live. A person's deploy in between, an automatic
  rollback, or the approver losing their role ends it. The request page and
  the audit log say so.
- **A plan given in steps shows Toft's marks:** this request, allowed by
  this approval, will ask again, or Toft would refuse.

### Look and wording

- **Inside a project, the sidebar is the project's,** with the project and
  environment switchers. The project bar is gone. The sidebar shows icons
  only from 768 to 1199 px and becomes a drawer below 768.
- **Red is no longer an environment colour,** and a protected environment
  draws neutral with its lock. A new `production` is gray and protected.
- **One selected tint** replaces left accent bars on selected rows.
- **The panel server's role** reads "Run apps here too" or "Only Toft"
  everywhere. Its network ranges and host ports are on Instance → Server.

### Servers

- **No Re-provision is needed** for temporary addresses. Within about five
  minutes of the upgrade, Toft restarts each server's Traefik once, recorded
  as a provisioning run. It pulls the new image before stopping the old one,
  keeps certificates, and doesn't retry after a failed run.
- The Loki log driver plugin is pinned to 3.7.9 (`-amd64` or `-arm64`).

### Security

- An agent's push credential with a non-ASCII branch name is refused instead
  of losing its push-only limit, and the git hooks refuse a ref line they
  can't read (SEC-001).
- Changing a service's repository URL, registry or image to another host
  clears its saved token or registry password, including a deploy's image
  override (SEC-002).
- Removing or demoting an owner expires the workspace's pending invites
  (SEC-003).
- Tokens are decoded strictly, so a signed-out session can't come back under
  another spelling (SEC-004).
- An agent connected to some projects reads only those over REST (SEC-005).
- `?next=/.//host` no longer leaves the panel after sign-in (SEC-006).

### API

- `POST /projects` takes an optional inline `service` with `variables` and
  `secrets`, all written in one transaction; a taken name is 409
  `project_name_taken`. `GET /services/address` and
  `GET /environments/{environmentID}/address` name an address before it
  exists. `POST /environments/{environmentID}/services` takes `"address":
  true` (off by default).
- `Domain.temporary` and `Domain.certificate_at`. A hostname another service
  has is 409 `hostname_taken` (was 500).
- `PATCH /org/settings` takes `"setup_hidden": true` (one-way, audited as
  `org.setup_hidden`).
- `Preview.migration_files` and `Preview.migrations_unchecked`, and the
  pull request event `preview_migrations_skipped`.
- Agent approvals gain `plan_steps` and `rollback`.
- **Changed:** `PATCH /instance/setup` no longer takes `skip`, and
  `GET /instance/setup` no longer returns `skipped`. A refused first-run
  registration is 403 `registration_closed`. An environment `color` of
  `red` is a 400.

### Migrations and data steps

- Migrations 0060 to 0066, all additive: `previews.migration_files` and
  `migrations_unchecked`, `agent_approvals.plan_steps`, `rollback_to`,
  `deployment_id` and `rollback_used_at`, `services.loopback_port`,
  `domains.certificate_at`, `organizations.setup_hidden_at`, and a widened
  pull request event kind.
- **Data steps, not undone by a rollback:**
  - Red environments turn gray and keep their protection. This runs on every
    upgrade.
  - Preview copies lose their migrate and pre-deploy commands. This also runs
    on every upgrade.
  - Finish setting up is hidden for every workspace that already has a live
    service. This runs once.
- Rolling back to 0.33.1 works on this schema.

## 0.33.1 (2026-10-31)

- A new project's `production` environment is created red and protected,
  as the docs say: deploying to it asks you to confirm, and an AI agent's
  deploy there waits for a person. Projects made before this release keep
  theirs as it is: tick **Protected** under the environment's
  **Settings** and **Save**.
- The README is rewritten: what Toft is for, the benchmark and the security
  decisions behind it, how it works, and a first deploy in five steps.

## 0.33.0 (2026-10-31)

- **What fills the disk** on each server, and on Instance → Server for the
  panel's own: every image in one class, kept (in use, kept for rollback,
  the panel's previous release) or safe to delete (untagged images, old
  builds, old panel releases, pulled images no service uses), with **Free
  space** to remove what you tick after your password. Images are never
  force-removed and nothing restarts. Volumes no service owns are listed and
  deleted one at a time. Audited as `server.disk_clean_started`,
  `server.disk_cleaned` and `server.volume_removed` (`panel.*` on the
  panel's own server).
- Each server has a **disk policy** with defaults: Toft trims the build cache
  to 10% of the disk after each build, cleans untagged images and old builds
  once a day and when the disk alert fires, and alerts at 85% used. Owners
  change each value; `server.disk_policy_changed` is audited. The disk alert
  says what the automatic clean freed.
- Each service keeps the images of its last 10 live deploys for rollback,
  changed under **Settings → Resources**. A deploy whose build image was
  removed can't be rolled back to (409 `image_removed`); an image service's
  rollback pulls its image again.
- Deploys to a server wait while its disk is cleaned (409
  `disk_clean_running`). A clean, a Docker update and a reboot wait for a
  service moving onto the server, as they do for a deploy there.
- `GET /api/v1/servers/{serverID}/disk`, `…/disk/clean` (`GET`, `POST`),
  `…/disk/volumes/remove`, `…/disk/policy` (`GET`, `PUT`), and the same under
  `/api/v1/instance/panel/disk`. `Deployment.image_removed`,
  `Service.rollback_images_kept`. `PanelStatus.disk` is deprecated.
- Migration 0059: the disk policy on `servers`,
  `services.rollback_images_kept`, `deployments.image_removed_at`, and
  `audit_log.actor_id` nullable for Toft's own acts. Additive.

## 0.32.0 (2026-10-31)

- The **Updates** card runs updates: **Install updates** (every pending
  update, with Docker's packages held back for that run only), **Update
  Docker** (its containers restart with it) and **Reboot**. Owners run them
  on a server, instance operators on the panel's own, with their password;
  each confirmation says what goes down. The run is a systemd unit on the
  host, so it carries on when the page closes, the panel restarts or the
  connection drops, and the card follows it. Audited as
  `server.updates_started`, then `server.updates_succeeded` or
  `server.updates_failed` (`panel.*` on the panel's own server).
- Deploys to a server wait while its Docker updates or it reboots (409
  `server_updating`), and a Docker update or reboot waits for a deploy in
  progress (409 `deploy_running`). A panel upgrade and a run of updates on
  the panel's own server wait for each other (409 `upgrade_running`,
  `update_running`).
- Agent servers need this release's agent to run updates; the confirmation
  says so for an older one.
- `GET` and `POST /api/v1/servers/{serverID}/updates/run` and
  `/api/v1/instance/panel/updates/run`. `HostUpdates` gains `held` (Docker's
  pending updates) and `removals`.
- Migration 0058 (`server_update_runs`), additive.
- An image deploy with a registry token and no username (a GHCR token, say)
  no longer fails at the pull with "error from registry: unsupported" on an
  agent server or the panel's own: the token goes as a login under `token`,
  as it already did over SSH and for compose. Agent servers need this
  release's agent.

## 0.31.0 (2026-10-30)

- Each server's page, and Instance → Server for the panel's own, has an
  **Updates** card: the updates apt would install, security updates apart;
  whether a reboot waits; the Docker engine, a newer one available and
  Docker's published security advisories that affect it; and whether
  unattended-upgrades runs and its last run. Read every 6 hours over SSH or
  the agent, and on **Check again**; nothing is installed. The panel fetches
  Docker's advisories from GitHub daily.
- Three alerts: `security_updates` (security updates that unattended-upgrades
  isn't installing), `reboot_required` and `docker_advisory`.
- `GET` and `POST /api/v1/servers/{serverID}/updates` and
  `/api/v1/instance/panel/updates`.
- Instance → Server's Host card no longer has a Security updates row: the
  Updates card replaces it.

## 0.30.1 (2026-10-30)

- A fresh install no longer fails at random with "database paas_db does
  not exist": it waits for the database server itself, not the one that
  sets it up.

## 0.30.0 (2026-10-30)

- Redeploying the same commit from a git URL builds from Docker's cache
  instead of building again: the build no longer has `.git` in its context.
  **A Dockerfile or Nixpacks build that runs `git` now fails.**

## 0.29.0 (2026-10-29)

- Adding a server is the owners': developers can no longer add one, and a
  first-join token is the owners' and instance operators'. An owner's API key
  still adds and joins servers from cloud-init; a rejoin is unchanged.
- A server sold as 4 GB can run the panel and apps: the floor is 3.5 GiB.
- A redeploy on the panel host no longer drops a request: the previous
  version leaves the proxy and finishes its requests before it stops. The
  first redeploy after upgrading retires a version started without this and
  can still drop a request or two.

## 0.28.0 (2026-10-28)

- Stored secrets are sealed to their place: a value copied to another row no
  longer decrypts. Every secret is re-sealed at start.
- New `toft rotate-encryption-key` replaces the instance's encryption key.
- The example `ENCRYPTION_KEY` is refused at start.
- **Upgrade to 0.27.0 first.** The upgrade refuses to start from an older release.
- The recovery kit holds the key: download it again after a rotation.
- Fresh installs work again. From 0.22.0 to 0.27.0, installing on a new
  server failed at the database step; upgrades were not affected.

## 0.27.0 (2026-10-27)

- Reads the sealed secret format that 0.28.0 writes, so 0.28.0 can roll back to it.
- A wrong `ENCRYPTION_KEY` stops the backend at start, instead of starting
  with every secret unreadable.

## 0.26.0 (2026-10-27)

- Every deploy has a time limit, 60 minutes by default, set per service from 5
  to 240 minutes in Settings → Resources. At the limit the deploy fails and the
  previous version keeps serving.
- Viewers of one container's live log share a single stream.

## 0.25.0 (2026-10-27)

- API keys expire: 30 days, 90 days (the default), 1 year, a date, or never.
  Keys made before the upgrade keep no expiry.
- A project key can be limited to some of its services.
- A deploy-policy exception approves specific values, not a whole rule.

## 0.24.0 (2026-10-27)

- The agent's join command keeps the join token off every command line.
- Agent upgrades are signed together with their version, and offered once per
  panel version.

## 0.23.1 (2026-10-27)

- Stricter response headers on the panel and every server's Traefik.
- Rate limits count an IPv6 client by its /64; invite links are rate limited.
- A server's address reported by its agent can't be forged.

## 0.23.0 (2026-10-26)

- A resource of another workspace answers 404, as an unknown one does.
- An API key can't create another API key; deleting an environment needs an owner.
- Session tokens are HS256 with an expiry; passwords are at most 72 bytes.

## 0.22.1 (2026-10-26)

- More tests around authorization, routes and the release path. No behaviour change.

## 0.22.0 (2026-10-26)

- Fixes from the pre-launch security audit: every High and Medium finding and
  most Low ones.
- API keys and agents can't use session-only actions such as changing a password.
- Deleting a project, or a server with its services, needs an owner and the password.
- The panel's routes outrank every app's on the panel's own server.

## 0.21.1 (2026-10-25)

- The first release from `Nickpap21/toft`, with the Toft logo. Images move to
  `ghcr.io/nickpap21`. Installs from before the move set `GHCR_USER=nickpap21`
  in `/etc/toft/secrets.env` to keep upgrading.

## 0.21.0 (2026-10-25)

- Not published: its release run failed on the image name.

## 0.20.1 (2026-10-24)

- The strict network's DNS relay answers its check for every query type.

## 0.20.0 (2026-10-24)

- **Sandboxed with a strict network:** gVisor's own network stack, with names
  answered by Toft's agent.

## 0.19.2 (2026-10-24)

- A server joined again as another server forgets the old one's state.

## 0.19.1 (2026-10-24)

- Routes, logs and metrics run through the node agent.
- Restoring the panel freezes servers until the restored state is reconciled.

## 0.19.0 (2026-10-24)

- Not published: its release run failed a test.

## 0.18.1 (2026-10-23)

- Durable rollouts: the agent keeps each service's desired state and finishes a
  rollout even when the panel restarts.

## 0.18.0 (2026-10-23)

- Not published: its release run failed a test.

## 0.17.3 (2026-10-23)

- An offline agent no longer also raises "server unreachable".

## 0.17.2 (2026-10-23)

- Fixes from the first production checks of the node agent.

## 0.17.1 (2026-10-23)

- **The node agent:** add a server with one command and an outbound
  connection, instead of SSH.

## 0.17.0 (2026-10-23)

- Not published: its release run failed a test.

## 0.16.3 (2026-10-22)

- Each agent policy cell is chosen from a menu.

## 0.16.2 (2026-10-22)

- The project's Agents card.

## 0.16.1 (2026-10-22)

- Fixes from the production checks of 0.16.0.

## 0.16.0 (2026-10-22)

- Agents' remaining actions over MCP, and the agent policy's scope: workspace
  defaults, per-project overrides, protected data services.

## 0.15.0 (2026-10-22)

- **Agents over MCP:** a built-in MCP server with OAuth, and a per-project
  policy of allow, ask a person, or never.

## 0.14.2 (2026-10-21)

- A preview's time moves only when the preview changes.

## 0.14.1 (2026-10-21)

- A recommended isolation level per service; new services start at it.

## 0.14.0 (2026-10-21)

- **Preview environments** for pull requests, sandboxed on the panel's own server.

## 0.13.2 (2026-10-21)

- needrestart is suspended while gVisor installs.

## 0.13.1 (2026-10-21)

- **The Sandboxed isolation level** with gVisor. Built with Go 1.26.10.

## 0.13.0 (2026-10-21)

- Not published: it needed Go 1.26.10 for a security fix in `crypto/x509`.

## 0.12.0 (2026-10-20)

- A hardened container runtime, and a probe of what each server supports.

## 0.11.0 (2026-10-20)

- Cycles and the Timeline.

## 0.10.0 (2026-10-20)

- The inbox and My issues: what needs you, updates, mentions, and issues
  opened from failed deploys.

## 0.9.1 (2026-10-20)

- Short times read "just now", never "now ago".

## 0.9.0 (2026-10-20)

- **Docs:** project pages with live service and command blocks, out-of-date
  warnings, runbook runs and versions.

## 0.8.1 (2026-10-20)

- Former issue keys link everywhere; ⌘K finds a key on its own.

## 0.8.0 (2026-10-20)

- Issues, deeper: types, keys, epics and releases, relations and clones.

## 0.7.0 (2026-10-20)

- **Issues**, moved by pushes, pull requests and deploys.

## 0.6.1 (2026-10-20)

- A merge that deletes its branch sends the push webhook for the deletion.

## 0.6.0 (2026-10-20)

- Repository import, pull and push mirrors, and outbound webhooks.
- Git repositories are in the nightly backup and the restore.

## 0.5.1 (2026-10-19)

- Saving a service's settings through the API keeps the fields it leaves out.

## 0.5.0 (2026-10-19)

- The checks API, and deploy on push from Toft's own repositories.

## 0.4.1 (2026-10-19)

- The pull request page refreshes after close, reopen and merge.

## 0.4.0 (2026-10-19)

- Several repositories per project, pull requests, merge and branch protection.

## 0.3.1 (2026-10-19)

- The repository browser.

## 0.3.0 (2026-10-19)

- **Git hosting:** every project has a repository over SSH (port 2222) and
  HTTPS. `install.sh --no-git` leaves git out.

## 0.2.5 (2026-10-18)

- Stopping the backend ends open streams and shells in about 3 seconds.
- Servers lists the servers first.

## 0.2.4 (2026-10-18)

- A host shell open across a restart writes its closing audit entry.

## 0.2.3 (2026-10-18)

- Every root shell is in the audit log.
- Server firewall, security updates, filesystem and backup rows.

## 0.2.2 (2026-10-18)

- Fixes to the panel's host shell after a backend restart.

## 0.2.1 (2026-10-18)

- **The panel looks after itself:** Instance for operators, upgrades from the
  panel, a host shell on the panel's server, the watchdog, the panel's own
  alerts, and `install.sh --adopt`.

## 0.2.0 (2026-10-18)

- Not published: its release run failed a check.

## 0.1.2 (2026-10-17)

- Traefik's files live outside the releases; `toft upgrade` hands over to the
  new release's own `toft`.

## 0.1.1 (2026-10-17)

- Fixes from the first real server: install.sh's Docker address pool, and a role
  check in the API.

## 0.1.0 (2026-10-17)

- The first release: one-command install, the setup wizard, `toft upgrade`
  with automatic rollback, and restore from a recovery kit.
