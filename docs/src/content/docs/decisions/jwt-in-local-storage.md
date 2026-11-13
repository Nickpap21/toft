---
title: The session token lives in localStorage
description: The panel keeps its session token in localStorage and sends it in a header, because the live streams need it there.
sidebar:
  order: 17
---

## Context

The panel follows deploy logs and other live data over server-sent events.
Those endpoints need the token in a header, like every other API route. The
browser's `EventSource` can't send one, and a token in the URL ends up in
logs and history. An httpOnly cookie is out of script's reach, which also
means script can't put it in a header.

## Decision

- The panel keeps a signed session token (a JWT) in `localStorage` and sends
  it as `Authorization: Bearer …` on every request.
- The client treats the token as opaque: it never reads the claims.
- Server-sent event streams are read with `fetch`, so the token travels in
  the header and never in the URL.

## Consequences

- Script running in the panel could read the token. The panel's strict
  Content Security Policy and server-side sanitising of user content are the
  defence; see [user content](/toft/decisions/user-content-domain/).
- Sessions last 30 days. Signing out revokes that one token on the server,
  and signing out everywhere revokes every session issued before.
- Sensitive actions still ask for the password again within a short window.
