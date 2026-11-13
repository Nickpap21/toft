---
title: Deploys return 202 and run in the background
description: A deploy request answers at once with 202 Accepted, and the client follows the pipeline's progress over server-sent events.
sidebar:
  order: 18
---

## Context

A deploy can run for minutes: clone, build, pull, migrate, health check,
switch. Holding an HTTP request open that long breaks on proxies and
timeouts, and ties the deploy's fate to one client's connection.

## Decision

- Everything that must be refused is checked first: the server, the deploy
  policy, capacity, the isolation level and, for an agent, the agent policy.
  A refused interactive deploy creates no deployment.
- Then the API records the deployment, starts the pipeline in a goroutine and
  answers `202 Accepted` with the deployment.
- The client follows the log over server-sent events at
  `/api/v1/services/{serviceID}/deployments/{deploymentID}/logs/stream`.

## Consequences

- Closing the browser doesn't stop a deploy.
- A deploy refused from a push is recorded as a failed deployment whose log
  says why, so a push is never silently ignored.
- A pipeline lives in the API process. If the API restarts mid-deploy, it
  marks the deploy failed as interrupted when it starts, works out which
  container is serving, and never runs the deploy again (that would repeat
  migrations). On a server with the [agent](/toft/decisions/node-agent/), the
  agent runs the rollout to the end on its own.
