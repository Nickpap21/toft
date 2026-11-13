---
title: Sign-up is invite-only
description: The first account on an instance runs it, and everyone after joins by invitation.
sidebar:
  order: 7
---

## Context

A self-hosted panel is reachable from the internet as soon as it starts.
Open registration would let anyone who finds the address create an account
on a system that holds server keys and secrets.

## Decision

- Every install starts in team mode. The first account created on an empty
  instance becomes the instance operator and the owner of a new workspace.
  When the installer sets a setup code, that first account needs it.
- After that, registration is invite-only: an account is created only from
  an invitation, in one transaction with the membership it grants.
- Only instance operators create workspaces.
- Two further modes are reserved and not accepted yet: sign-up by email
  domain, and an approval queue.
- A public hosting mode, with open sign-up, trust tiers and abuse tools, is a
  separate gate, built only when hosting strangers becomes a priority.

## Consequences

- A registration without an invitation, on an instance that already has
  users, is refused before any password is hashed.
- Instance operators are separate from workspace roles (owner, developer,
  viewer).

Decision log: D-21
