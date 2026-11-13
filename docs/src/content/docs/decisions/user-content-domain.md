---
title: User content never runs as the panel
description: Files, markdown and other content people write are served so they can't run script in the panel's origin.
sidebar:
  order: 8
---

## Context

Toft shows content its users wrote: repository files, READMEs, issue and doc
pages. An SVG or HTML file served from the panel's own origin could run
script with a signed-in person's access, and the panel holds server keys and
secrets.

## Decision

- User-authored content is served from a separate cookieless domain or in a
  sandbox. Toft uses the sandbox: no separate domain is needed.
- Raw repository files carry `Content-Security-Policy: sandbox;
  default-src 'none'`, `X-Content-Type-Options: nosniff` and
  `Cross-Origin-Resource-Policy: same-origin`. Raster images keep their type;
  any other text, SVG and HTML included, is `text/plain`; binary files are
  downloads.
- Markdown is rendered and sanitised on the server: raw HTML left out, links
  only to http, https and mailto, heading ids prefixed `user-content-`.
- The panel has a strict Content Security Policy, which loads no outside
  images.
- Fleet-level operator actions ask for the password again within a short
  window (10 minutes): host shells, host-key replacement, operator and
  account changes.

## Consequences

- A README can't load outside images or take an id the panel uses. An image
  from elsewhere becomes a link to it.
- Opening a raw file never gives repository bytes a document in the panel's
  origin that can run script.

Decision log: D-23
