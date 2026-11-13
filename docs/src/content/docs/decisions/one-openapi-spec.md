---
title: One OpenAPI spec is the contract
description: The web client, the CLI's models and the MCP tools all come from one OpenAPI file, and each instance serves its own API reference.
sidebar:
  order: 5
---

## Context

Toft has three clients of its API: the web panel, the `toft` CLI and AI agents
over MCP. Hand-written types beside each would drift from the server, and a
reference page written by hand would drift from all of them.

## Decision

- `backend/api/openapi.yaml` is the single source of truth. A new or changed
  endpoint goes into the spec first.
- The web client's types and the CLI's models are generated from it. The MCP
  tools are declared in it (`x-toft-tools`).
- The API reference lives in the instance: `GET /api/v1/docs` renders the
  embedded spec of the version that runs, so "Try it" works with the reader's
  own key. The guide to authentication, paging and errors is written in the
  spec's `info.description`, so it renders anywhere the spec does.
- That page uses Scalar, loaded from a CDN at a pinned version with an
  integrity hash rather than vendored. The panel itself loads nothing from a
  CDN; an instance that blocks CDNs still gets the YAML at
  `/api/v1/openapi.yaml`.

## Consequences

- A route table test, response checks and a tools test fail when the code and
  the spec disagree.
- This site's [API reference](/toft/reference/api/) renders the same file.
- A hand-made API page was rejected as a second source of truth.

Decision log: D-18, D-60
