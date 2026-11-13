---
title: App containers reach their own host only on web ports and DNS
description: An nftables table loaded at boot limits what app containers can reach on the server they run on.
sidebar:
  order: 11
---

## Context

Docker's networks let a container open connections to its own host. That
reaches whatever listens there, the panel's API on a single-server install
included, and the cloud provider's metadata address, which can hand out the
instance's credentials.

## Decision

- A Toft-owned nftables table, `ip toft`, is loaded at boot by
  `toft-firewall.service`, before Docker starts.
- Traffic arriving on any bridge (every Docker network) may open connections
  to the host's own addresses only on TCP 80 and 443 (Traefik) and on DNS.
- Nothing in `169.254.0.0/16` (cloud metadata) is reachable from a container
  except DNS.
- This holds on every server, in both app network modes.
- Replies to connections the host opened, the internet, and other containers
  on the same network are not affected.
- An untrusted preview's own network also can't reach private addresses
  outside it, nor SMTP.

## Consequences

- Apps lose the cloud instance's role through the metadata address, with no
  opt-out.
- If the table fails to load at boot, Docker still starts (fail-open). The
  provision log and a check script show it.
- The provisioner and the installer install the same two files
  (`firewall.nft` and the unit). Change them in the repository, never on a
  server.
- Private address ranges are not blocked for ordinary services until shared
  server pools exist, and then only with an allowlist.

Decision log: D-37
