---
title: Single-server installs are first-class
description: One server can run both the panel and your apps, with no agent and nothing extra to set up.
sidebar:
  order: 9
---

## Context

Many teams start with one server. A platform that demands a separate control
plane, or an agent on every machine, makes the first install harder than the
problem it solves.

## Decision

- One server can run the panel and the apps together (the "control plane and
  apps" role).
- That server uses the local Docker socket. It needs no agent.
- The panel keeps a fixed share of CPU and memory for itself and the system.
  Apps get the rest, through the `toft-apps.slice` systemd unit.
- Apps run on their own networks, behind the deploy policy.
- Adding more servers later is optional.

## Consequences

- A server below a minimum size runs the panel only, since apps would get
  almost nothing.
- Only a future public hosting mode will require a control-plane-only
  server, so that strangers' apps never run beside the panel.
- On the panel's own server, other services are
  [recommended Sandboxed](/toft/decisions/isolation-levels/): a container
  that breaks out there reaches every server's SSH key and every secret.
- Sandboxed with a strict network isn't available on the panel's own server,
  because it needs the agent.

Decision log: D-26
