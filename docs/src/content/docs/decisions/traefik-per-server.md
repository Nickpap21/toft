---
title: Traefik on every server, not a central proxy
description: Each server runs its own Traefik with its own certificates, so it keeps serving its apps when the panel is down.
sidebar:
  order: 20
---

## Context

A central proxy in front of every server would be one place to configure
domains and certificates. It would also be one place to fail: if it or the
panel went down, every app on every server would go with it.

## Decision

- Every server runs its own Traefik (`paas-traefik`), set up by the
  provisioner.
- Each Traefik gets its own certificates from Let's Encrypt for the domains
  of the apps on that server, wildcards included.
- Traefik reads each app's routes from its container's labels. On a server
  with the agent, the agent writes them as files instead, and Traefik no
  longer needs the Docker socket.

## Consequences

- A server is self-contained: its apps keep serving, with valid certificates,
  while the panel is down or unreachable.
- A domain points at the server its app runs on, not at the panel. Moving a
  service to another server means pointing its DNS there.
- Each server answers on ports 80 and 443.
