---
title: Git hosting built on the git binary
description: Toft hosts repositories itself, with git doing storage and transport in an isolated container, and git over SSH on port 2222.
sidebar:
  order: 6
---

## Context

Toft puts the project at the centre: a push opens a pull request, the pull
request gets a preview, the issue moves, and the deploy links back to all of
it. That needs the git host inside the platform. Reimplementing git's storage
and wire protocols would be a large, risky surface.

## Decision

- Toft builds its own git hosting on the canonical git binary. Everything
  people see (the repository browser, pull requests, reviews, checks) is
  Toft's. git does the storage and the transport.
- git runs isolated from the start. The `toft-git` program runs alone in the
  `paas-git` container: no network, a read-only filesystem, every capability
  dropped, and only the repositories and a socket the API calls it on.
- Git over SSH listens on port 2222 by default (`TOFT_GIT_SSH_PORT`), so it
  doesn't collide with the server's own SSH on 22.

## Consequences

- Clones and pushes work over SSH and over HTTPS. Over HTTPS, git signs in
  with an API key.
- Markdown is rendered inside the git container, so a hostile README costs
  that container's CPU, not the API's.
- An operator who wants git on port 22 moves the host's own SSH daemon to
  another port first.

Decision log: D-20, D-28
