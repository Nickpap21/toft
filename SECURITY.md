# Security policy

Toft runs other people's code on servers it controls, so we take reports
seriously and answer them fast.

## Reporting a vulnerability

**Report it privately through GitHub:**
[Report a vulnerability](https://github.com/Nickpap21/toft/security/advisories/new).
Never open a public issue, discussion or pull request for one.

Include what you can:
- the version (Instance → Overview in the panel);
- how Toft was installed;
- the steps to reproduce it;
- what an attacker gains.

A proof of concept helps, but a clear description is enough.

## What happens next

| Step | Within |
| --- | --- |
| We acknowledge your report | 3 working days |
| We assess it, confirm or decline it, and give it a severity | 7 days |
| A fix for a Critical or High finding is released | 30 days |

- We keep you informed in the advisory as the fix progresses, and credit you
  in it unless you'd rather not be named.
- The fix is published as a GitHub security advisory, with a CVE for anything
  Medium or above.
- We ask you to keep the details private until the fixed release is out.

## Supported versions

Until v1.0, only the latest release gets security fixes. Upgrade from
Instance → Overview in the panel, or with `toft upgrade --to <version>`.

## In scope

- the panel's API and web UI;
- the node agent (`toft-agent`);
- git hosting (`toft-git`);
- the `toft` CLI;
- the MCP server;
- `install.sh`, `toft upgrade` and the restore path;
- the container images we publish;
- isolation between workspaces, between apps, and between an app and its
  server.

## Out of scope

- vulnerabilities in the apps people deploy on Toft;
- attacks that need root on a managed server, or the instance operator's
  account;
- volumetric denial of service;
- social engineering of the maintainers;
- missing hardening with no demonstrated impact. Open a regular issue for
  those.

Hardening work we already know about is tracked in public with the
[`security-hardening`](https://github.com/Nickpap21/toft/labels/security-hardening)
label.

## Safe harbour

We won't take legal action against research that follows this policy:
- it's done in good faith;
- it stays on your own installation;
- it doesn't touch other people's data;
- it gives us a reasonable time to fix the problem before you disclose it.
