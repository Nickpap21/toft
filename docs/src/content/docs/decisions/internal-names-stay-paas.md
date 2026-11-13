---
title: Internal names keep the paas prefix
description: Containers, networks, volumes, labels and storage keys keep their paas names; only what people see says Toft.
sidebar:
  order: 12
---

## Context

Toft began under a working name, and its containers, networks, volumes,
labels and storage keys were all named `paas-…`. The product is now Toft.

## Decision

- Internal names stay `paas` for good: the `paas-backend`, `paas-postgres`
  and `paas-git` containers, the `paas-network` network, the `paas.*`
  container labels, the `paas_` API key prefix and the browser's storage
  keys.
- Only what people see says Toft: the product, the CLI (`toft`), and new
  environment variables and systemd units (`TOFT_APP_NETWORKS`,
  `toft-firewall.service`).
- New Go packages and database objects use plain domain nouns, with neither
  prefix.

## Consequences

- Renaming would rename every volume, which means copying all data, and would
  recreate every container. Keeping the names avoids that on every install.
- You will meet both names when you operate a server: `docker ps` shows
  `paas-…` containers running Toft.
- Contributors don't rename these as a side effect of other work.

Decision log: D-40
