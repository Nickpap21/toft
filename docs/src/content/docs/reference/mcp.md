---
title: MCP and agents
description: Connect Claude Code or another MCP client to Toft, the tools it gets, and the policy that decides what it may do.
sidebar:
  order: 2
---

Agents work in Toft over MCP (the Model Context Protocol). Each agent acts as
the person who connected it, never with more than that person's role. The
workspace's agent policy decides what an agent does alone, what waits for a
person, and what it never does. Every call lands in the audit log.

## Connect an agent

The MCP endpoint is `/mcp` on your panel's address. It speaks Streamable HTTP
and signs agents in with OAuth, with Toft as the authorization server.

For Claude Code, run:

```sh
claude mcp add --transport http toft https://<panel-domain>/mcp
```

Then run `/mcp` in Claude Code and choose **Authenticate**. Toft opens in your
browser, where you choose:

- the **workspace**: one per connection; connect again for another;
- the **projects**: every project in the workspace, new ones included, or
  only the ones you pick;
- the **abilities**: Read is always on; Deploy and roll back, Branches and
  pull requests, Issues and comments, Variables and secrets, and Runbook
  steps are each optional. A viewer can only give Read.

Select **Allow** and go back to your terminal. The agent gets a token of its
own, never your session, for 90 days. You or an owner can revoke it at any
time in **Settings → Agents**, under **Connected agents**.

Other MCP clients connect to the same URL. **Settings → Agents → Connect
Claude Code** shows the exact command and URL for your panel.

## Tools

Read tools always run. Act tools run under the agent policy, and each takes a
`reason` and a `plan`: the reason is required when the call waits for a
person, who reads both.

| Tool | Ability | What it does |
| --- | --- | --- |
| `list_projects` | Read | The projects the connection reaches, each with its services and the environment each is in: IDs, names, status, source and server. The other tools take these IDs. |
| `get_service` | Read | One service: its source, settings, status, domains and isolation, with its last 10 deployments. |
| `get_deploy_logs` | Read | A deployment's status and the last lines of its log; the service's latest deployment by default. |
| `get_capacity` | Read | Each server's capacity: what it has, what is allocated, and to which workspaces and projects. |
| `search_docs` | Read | Searches the projects' docs pages (runbooks, notes) by title and text, or reads one page's markdown. |
| `get_issue` | Read | An issue: title, description, status, priority, assignee, cycle and what links to it. |
| `get_diff` | Read | A diff in a project's repository: a pull request's, a commit's, or between two branches. |
| `deploy` | Deploy and roll back | Deploys a service. By default it waits and returns the outcome with the last log lines. |
| `rollback` | Deploy and roll back | Rolls a service back, by default to the last deployment that ran before the current one. |
| `create_preview` | Deploy and roll back | Starts a pull request's [preview](/toft/guide/previews/), or starts a stopped one again. |
| `create_branch` | Branches and pull requests | Makes a branch and returns a password that pushes to that branch alone over HTTPS, for 24 hours. |
| `open_pr` | Branches and pull requests | Opens a pull request. Its preview waits for a person to approve each commit and runs Sandboxed with no secrets. A person merges it. |
| `create_issue` | Issues and comments | Opens an issue with a title, a markdown description, and optionally a priority, labels and a type. |
| `comment` | Issues and comments | Comments on an issue or a pull request, in markdown. Mentions notify people. |
| `set_env_var` | Variables and secrets | Sets one plain variable of a service or an environment, read at the next deploy. Never deletes or replaces the others. |
| `set_secret` | Variables and secrets | Sets a secret's value. Nobody can read it back, the agent included, and a person approving the call never sees the value. |
| `run_runbook_step` | Runbook steps | Runs one command of a docs page, counting from 1, in its service's container (never on a server's host), for at most 10 minutes. |

No tool reads a secret, changes a role, deletes the workspace or removes a
server, whatever the policy says.

An agent may make at most this many act calls an hour per connection, whether
they run or ask: 20 `create_branch`, 10 `open_pr`, 20 `create_issue`, 60
`comment`, 60 `set_env_var`, 20 `set_secret`, 30 `run_runbook_step` and 20
`create_preview`.

## The agent policy

**Settings → Agents → What agents may do** is a table of actions against
classes of environment. Owners change it, and loosening a cell asks for the
password. It applies to every agent in the workspace.

Each cell is one of:

- **Allow**: the call runs at once.
- **Ask a person**: the call waits for an owner or developer to approve it.
- **Never**: the call is refused.

The columns are **Previews** (pull requests' previews), **Other environments**
and **Protected** (the protected environments, such as `production`). The
defaults:

| Action | Previews | Other environments | Protected |
| --- | --- | --- | --- |
| Deploy or roll back | Allow | Allow | Ask a person |
| Change variables | Allow | Ask a person | Ask a person |
| Set a secret | Ask a person | Ask a person | Ask a person |
| Run runbook steps | Ask a person | Ask a person | Ask a person |

Two rows touch no environment, so each has a single cell, Allow by default:
**Open branches and pull requests** (an agent pushes only to branches it
created) and **Open issues and comment**. Reading is always allowed, narrowed
to the projects chosen when the agent was connected.

**Protected data services.** A database or cache (Postgres, MySQL, Redis and
the like) counts as Protected wherever it runs, for deploys, variables,
secrets and runbook steps. A preview's copy of one stays a preview.

**Per-project rules.** Under **Projects with their own rules**, an owner
selects **Add a project** to give one project its own cells. A cell the project doesn't set
follows the workspace's. The audit log records which level decided each call.

The policy applies to an agent's token on the HTTP API too, not only to the
MCP tools.

### What "Ask a person" does

The agent's call waits. It shows in **Settings → Agents → Agents waiting for
a person**, with the agent's reason. An owner or developer selects **Review**,
reads the reason and the plan, and approves or denies it. For `set_secret`,
the value stays hidden from the reviewer.

- Approved: the call runs as asked and the agent gets its result.
- Denied: nothing runs, and the agent is told who denied it.
- Nobody decides within 30 minutes: the request expires and nothing runs.

An agent may ask for at most 10 approvals an hour.

### Pause all agents

An owner's **Pause all agents** in Settings → Agents refuses every agent call in the
workspace from that moment, and turns down the waiting requests. Deploys
already running finish. Connections stay, so nobody signs in again when an
owner resumes. An instance operator can also **Pause on this panel**, which
does the same in every workspace.

## The HTTP API

Everything the panel, the CLI and the MCP tools do goes through Toft's HTTP
API. Scripts and CI call it with an [API key](/toft/reference/cli/#api-keys).
The contract is browsable in the [API reference](/toft/reference/api/). Each
instance also serves the reference for the version it runs at
`/api/v1/docs` on its API address, with a place to try calls with your key,
and the raw OpenAPI document at `/api/v1/openapi.yaml`.
