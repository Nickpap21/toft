---
title: An outbound agent for remote servers
description: A server can connect to the panel through a small agent that dials out, instead of accepting SSH from the panel.
sidebar:
  order: 2
---

## Context

Over SSH, the panel dials each server, so the server must accept inbound SSH
from the panel's address. That rules out servers behind NAT or strict
firewalls. A rollout also lives in the panel's memory: if the panel restarts
mid-deploy, the server is left half-way.

## Decision

- Toft has a node agent, `toft-agent`, built in four phases: thin, reliable,
  enforcing, then fleet.
- It is for remote servers. A single-server install keeps the local Docker
  socket and no agent.
- The agent dials `wss://<panel>/api/v1/agent/connect` on port 443, honours
  `HTTPS_PROXY`, and runs an SSH server inside the WebSocket. The panel is the
  SSH client, as it is for any server.
- gRPC was considered and rejected: two new dependencies, protoc in CI, h2c
  or a new port, and every remote operation rewritten as an RPC.

## Consequences

- Every caller that runs something on a server (the Docker tunnel, builds,
  logs, exec, the provisioner, gVisor, backups) works unchanged over SSH or
  the agent.
- The agent's key is pinned when it joins, and an agent is never trusted on
  first use. New abilities are typed requests, never a shell command the panel
  builds. The agent upgrades itself only to signed releases. Release CI
  signs each binary's version, architecture and SHA-256 together. The agent
  rebuilds that line from the version it was offered, its own architecture
  and the hash it computed. So the version it checks is the version it
  installs, and it refuses an upgrade that has no signature.
- Built: the thin and reliable phases. The agent keeps the desired state,
  runs rollouts to the end across a panel restart, writes Traefik's routes,
  spools logs and buffers metrics while the panel is away.
- Not built yet: the enforcing and fleet phases, a tunnel-free agent and
  fleet-wide upgrade waves. They are on the [roadmap](/toft/roadmap/).

Decision log: D-10, D-106, D-123
