---
title: Docker Engine, not Kubernetes
description: Toft runs apps on Docker Engine behind an orchestrator interface, and keeps Kubernetes out of the core.
sidebar:
  order: 1
---

## Context

Toft is for teams that run their own servers, often one or two of them. A
Kubernetes cluster is a large thing to install, upgrade and debug on a single
server, and most of what it offers (scheduling across a fleet, autoscaling)
isn't what these teams need first.

## Decision

- Docker Engine is the runtime. Deployment code talks to it through the
  `deployment.Orchestrator` interface, which the `docker` package implements.
- Kubernetes is not the core. A lightweight distribution such as k3s may come
  later as an optional runtime behind the same interface. It is also the
  likely path to MicroVMs at scale.
- Nomad was ruled out because of its licence (BSL).

## Consequences

- A server needs Docker and nothing else from the cluster world. Each one runs
  its own Traefik and keeps working on its own.
- Deployment logic never imports Docker code directly, so a second runtime is
  a second implementation of the interface, not a rewrite.
- Kubernetes as an optional runtime is on the roadmap under "Later". Docker
  Engine stays the default.

Decision log: D-09
