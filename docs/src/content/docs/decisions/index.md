---
title: Decision records
description: Why Toft is built the way it is, one decision per page.
sidebar:
  order: 0
---

A decision record explains one choice that shapes Toft: the problem it
answers, what was decided, and what follows from it. Read them before you
propose a change to one of these areas. A change that undoes a decision starts
as a discussion, not a pull request.

Each record has three parts:

- **Context**: the problem, and what made it a problem.
- **Decision**: what Toft does.
- **Consequences**: what that costs, what it rules out, and what it leaves
  open.

## Decision-log numbers

The project keeps a running decision log, numbered D-01, D-02 and onward.
Code comments cite those numbers, as in
`// Recreate stops the previous slot before the new one starts (D-42).`
The last line of each record lists the log entries it covers, so you can find the record a comment points
at. A few records describe choices made before the log began, and have no
number.

The records describe what the code does today. Where a decision was later
amended, the record describes the amended form.
