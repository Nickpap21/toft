---
title: AGPL for the server, Apache-2.0 for the clients
description: The server and web UI are AGPL-3.0-or-later, the CLI, SDKs and schemas will be Apache-2.0, and every commit carries a DCO sign-off.
sidebar:
  order: 10
---

## Context

Toft is open source and self-hosted. The licence has to keep improvements to
a hosted Toft open, without making it awkward to embed the CLI or the API
schemas in other tools.

## Decision

- **AGPL-3.0-or-later** for the server, the web UI and everything else in
  the repository, unless a directory says otherwise.
- **Apache-2.0** for the CLI, SDKs and the OpenAPI and MCP schemas, each in
  its own directory with its own `LICENSE` file.
- A **DCO sign-off** on every commit (`git commit -s`), and no CLA.
- The name "Toft" and its logo are not licensed. A trademark policy will
  spell this out.

## Consequences

- Someone who runs a modified Toft as a service must offer its source to its
  users.
- The Apache-2.0 parts apply once they move into their own directories. Until
  then, the repository is AGPL-3.0-or-later. See `CONTRIBUTING.md` for the
  table.
- A CI check fails any pull request with a commit that isn't signed off.

Decision log: D-27
