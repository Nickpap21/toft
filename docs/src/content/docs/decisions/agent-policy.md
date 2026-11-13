---
title: AI agents act under a policy people set
description: Agents connect over MCP with OAuth, and every action they take is allowed, sent to a person, or refused by a policy per workspace and project.
sidebar:
  order: 14
---

## Context

AI agents can deploy, roll back, open pull requests and change variables
through Toft. Some of those actions are harmless in a preview and dangerous
in production. A team needs to say which an agent may do alone, and see what
it did.

## Decision

- Agents connect to the MCP endpoint at `/mcp` with OAuth:
  `claude mcp add --transport http toft https://<panel>/mcp`. Their tokens
  are opaque and prefixed `toft_oat_`.
- Each action (deploy, code, issues, variables, secrets, runbooks) in each
  environment class (preview, protected, other) is **Allow**, **Ask a
  person** or **Never**.
- The workspace's table is the default. A project can override single cells;
  a cell it doesn't set follows the workspace, then the built-in default.
- A deploy or rollback of a database or cache counts as protected wherever it
  runs, so by default it asks a person.
- Agents never read secrets. Every agent call is audited, and owners can
  pause all agents in a workspace.

## Consequences

- The policy runs in the use case, so an agent's token meets the same rules
  over the REST API as over MCP.
- Owners set the policy. Loosening a cell asks for the password again. The
  audit log records which level decided each call.
- A policy per service was rejected: it multiplies into cells nobody audits,
  and the usual reason for one (the database) is derived from the service's
  kind.

See the [MCP reference](/toft/reference/mcp/).

Decision log: D-17, D-101
