# Contributing to Toft

Thanks for helping. This page covers what you need for a first pull request:
running Toft locally, the checks CI runs, and how changes get in.

- Questions go in [Discussions](https://github.com/Nickpap21/toft/discussions).
- Bugs and feature requests go in [issues](https://github.com/Nickpap21/toft/issues/new/choose).
- Security problems go through [private reporting](SECURITY.md), never a
  public issue.

For anything larger than a small fix, open an issue first and say what you
plan to change. That saves you work if the change goes in a different
direction from the roadmap.

## Running Toft locally

You need Docker, Go (the version in `backend/go.mod`), Node.js 26 and `make`.

```sh
cp .env.example .env    # then set JWT_SECRET and ENCRYPTION_KEY to `openssl rand -hex 32`
make up                 # Traefik, Postgres, the backend and the frontend
make migrate            # apply the schema
```

- The panel is at <http://localhost:3000>, the API at <http://localhost:8080>
  and Traefik's dashboard at <http://127.0.0.1:8081>.
- On a new database the backend restarts until `make migrate` has applied the
  schema, so its log shows a few FATAL lines first. That's expected.
- Locally, the first account you register becomes the instance operator.

`make help` lists every target.

## The checks

CI runs these on every pull request, cheapest first. Run them before you
push:

| Change | Command |
| --- | --- |
| Backend | `cd backend && test -z "$(gofmt -l ./cmd ./internal ./pkg)" && go vet ./... && make -C .. test-db && TOFT_TEST_DATABASE_URL='postgres://postgres:toft@127.0.0.1:55432/postgres?sslmode=disable' go test -race ./...` |
| Frontend | `cd frontend && npm ci && npm run lint && npm run lint:tokens && npm run typecheck && npm run api:types:check && npm test && npm run build` |

`make test-linux` runs the backend tests on Linux in Docker, as CI does.
Some tests read `/proc`, so a Mac can pass what Linux fails.

Never disable or weaken a test or a lint rule to get a green run. If one
fails and you think it's wrong, say so in the pull request.

## How the code is laid out

- **`backend/`** is Go: Gin for HTTP, pgx with sqlc for the database, and the
  Docker SDK. It holds the panel's server (`cmd/server`), the `toft` CLI
  (`cmd/toft`), the node agent (`cmd/toft-agent`) and git hosting
  (`cmd/toft-git`).
- **`frontend/`** is Next.js (App Router), React, Tailwind and shadcn/ui.
- **`install.sh`** prepares a server and installs a release.

`AGENTS.md` holds the rules for each part, for people and coding agents
alike: the API contract, the schema, layering, authorization, tests and
the frontend's patterns. Read the section for the code you're changing.

## Pull requests

1. Fork the repository, and branch from `master`.
2. Keep one change per pull request, with tests for new behaviour.
3. Sign off every commit (see below).
4. Fill in the template. The checks must pass before review.

A maintainer reviews every pull request. Small, focused changes are reviewed
fastest.

## AI-assisted contributions

Toft is built with heavy AI assistance: much of its code is written with
coding agents and then reviewed, tested and signed off by its maintainers.
AI-assisted pull requests are welcome on the same terms as any other:

- you have read and understood every line you submit;
- the checks pass, and you've run the change;
- your sign-off certifies the DCO for it, so you answer for it as your own.

Coding agents read `AGENTS.md` for the repository's rules.

## Developer Certificate of Origin

Every commit must be signed off, certifying the
[Developer Certificate of Origin 1.1](https://developercertificate.org/):

```sh
git commit -s -m "Describe the change"
```

This adds a line such as `Signed-off-by: Your Name <you@example.com>`, with the
name and email you commit as. There's no CLA. A CI check fails any pull
request with a commit that isn't signed off. `git rebase --signoff master`
fixes the commits you already have.

## Licensing

| Part | Licence |
| --- | --- |
| The server, the web UI and everything else in this repository, unless a directory says otherwise | [AGPL-3.0-or-later](LICENSE) |
| The CLI, SDKs and the OpenAPI/MCP schemas, once they're in their own directories | [Apache-2.0](LICENSES/Apache-2.0.txt), stated by a `LICENSE` file in that directory |

The licences cover the code, not the name. "Toft" and its logo aren't
licensed for use by forks or hosted services. A trademark policy will spell
this out.

## Code of conduct

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).
