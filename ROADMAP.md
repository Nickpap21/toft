# Roadmap

What we're building next. It changes as we learn; what shipped is in the
[changelog](https://nickpap21.github.io/toft/changelog/). Ideas and requests are welcome in
[Discussions](https://github.com/Nickpap21/toft/discussions).

## Next

What we're working on after 1.0.

- **A tunnel-free agent.** The node agent stops carrying a Docker tunnel and
  enforces what the panel asks of it, so the panel never holds the equivalent
  of root on a server.
- **The agent for servers behind NAT,** with SSH only to bootstrap, and agent
  upgrades in waves across a fleet.
- **Per-project roles,** beside today's workspace roles.
- **One-click server images:** a cloud-init snippet for any provider, then
  marketplace images.
- **Deploy history detail:** each health-check attempt shown on its deployment.

## Later

- **Public hosting mode:** shared server pools for people who don't know each
  other, with Sandboxed as the minimum, sandboxed builds on a separate build
  pool, quotas and a usage ledger. Apps never run on the control plane in this
  mode.
- **Kubernetes as an optional runtime** behind the same orchestrator interface.
  Docker Engine stays the default ([why](https://nickpap21.github.io/toft/decisions/docker-not-kubernetes/)).
