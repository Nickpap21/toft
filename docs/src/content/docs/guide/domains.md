---
title: Domains and TLS
description: Give the panel and your services their own names, with Let's Encrypt certificates from each server's Traefik, wildcards included.
sidebar:
  order: 4
---

Every server runs its own Traefik. It routes the domains of the services on
that server and gets their certificates from Let's Encrypt, so a server keeps
serving and renewing when the panel is down.

## The panel's address

The setup wizard's **Address** step asks how people reach the panel:

- **A domain.** Toft gets it a certificate from Let's Encrypt. The panel and its API move
  to `https://<your-domain>`. Webhooks and links use it too.
- **Keep the IP for now.** Nothing to set up, but browsers warn about
  the self-signed certificate and the connection is not trusted. For a test or
  a private network.

For a domain:

1. Create an A record for it pointing at the panel's server, and an AAAA
   record if the server has IPv6. One name is enough: the API is served on the
   same address, under `/api`.
2. Enter it under **Domain** and choose **Check and continue**. The panel
   checks that the name reaches it, then asks Let's Encrypt for a certificate.
   Let's Encrypt checks the domain over port 80, so port 80 must be open to
   the internet.
3. Once the certificate is issued, choose **Continue on** your domain and sign
   in again there: your browser keeps a sign-in per address.

When setup finishes with a domain, the panel stops answering on the server's
IP address. Change the address later under **Instance → Overview → Setup →
Change address**. Changing it moves everyone to the new address, so Toft asks
for your password first. The [install guide](/toft/install/) covers setting
the address when you install.

## Add a domain to a service

A domain belongs to one service that answers HTTP: a service needs a port
before it can take one. Each hostname can belong to only one service on the
instance.

1. Open the service's **Domains** tab and choose **Add domain**.
2. Enter an exact name, such as `app.example.com`, or a wildcard, such as
   `*.example.com`. Names are saved in lower case. An IP address is not a
   domain.
3. Create the DNS record (below), then deploy the service.

Traefik starts routing a new domain at the service's next deploy, and asks
Let's Encrypt for its certificate at that deploy. Point the record first.
Removing a domain also takes effect at the next deploy; until then, requests
to it still reach the service.

For a compose service, **Settings → Domain routing** picks the compose service
and container port that the domains reach.

The workspace's **Domains** page lists every hostname the workspace serves,
with the ones that need attention first.

## A temporary address at creation

When you create a web app from **New service**, the summary offers **Give it
an address now**, on by default, so the first deploy ends with a link that
works. Databases, caches, admin tools (pgAdmin, Adminer, phpMyAdmin,
RedisInsight, Mailpit, MailHog) and workers get none. An API or CLI create gives an
address only when it asks (`"address": true`).

The address is a **temporary** name under `sslip.io`, which answers with the
server's address: `<service>-<environment>-<project>.<a-b-c-d>.sslip.io`. A
server with no public IPv4 address gets none. It is never a name under the
project's preview domain: that wildcard belongs to previews.

A temporary name is for a first look; use your own domain for anything with
logins. Let's Encrypt counts every `sslip.io` certificate, from everyone, against
one shared weekly limit, so a temporary name's certificate can take hours or
days. Until it arrives the name answers over plain HTTP (unless the service
has basic auth, whose password never crosses plain HTTP), and the service's
**Domains** tab shows its certificate as *Pending*. On a server provisioned
before this, Toft restarts Traefik once within five minutes of the upgrade to
allow the plain-HTTP answer (its routes pause for a few seconds; the server's
page shows it as a provisioning run). When you add a domain of your own, it comes first: in lists and
in the app's `TOFT_URL`. Moving the service to another server moves its
temporary name to that server's address in the same deploy.

## DNS records

The service's **Domains** tab shows, under **Point DNS here**, the public
address of the server the service runs on.

| Domain | Record |
| --- | --- |
| `app.example.com` | `A` record for `app.example.com`, pointing at the server's address |
| `*.example.com` | `A` record for `*.example.com`, pointing at the server's address |

A name reaches the service once its record points at the server and the
service has deployed with it. For each domain the tab shows where its DNS
points (*Points here*, *Points elsewhere*, *No record yet*), the certificate
the server presents for it, and whether the running container is routed for
it.

## Certificates

Traefik on each server has two Let's Encrypt resolvers.

| Domain | Challenge | Setup |
| --- | --- | --- |
| Exact name | HTTP-01, when a deploy adds the route | Nothing. Port 80 must reach the server. |
| Wildcard | DNS-01, through the server's DNS provider | A DNS provider on the server |

Certificates live in a Docker volume on the server and survive Traefik being
recreated. Traefik renews them on its own.

Every routed domain answers plain HTTP with a permanent redirect to HTTPS
(a temporary one excepted, above), and gets these headers: HSTS for one year
with `includeSubDomains`,
`X-Frame-Options: DENY` and `X-Content-Type-Options: nosniff`. With HSTS on
`includeSubDomains`, browsers that visited `example.com` refuse plain HTTP on
its subdomains too.

## Wildcard domains

A wildcard such as `*.example.com` routes every name one level below it that
has no route of its own: `acme.example.com`, but not `a.b.example.com` or
`example.com` itself. An exact name on the same server always wins over a
wildcard.

Its certificate needs the DNS-01 challenge, which Traefik answers by writing a
TXT record through your DNS provider's API. To set one up, on the server's
page:

1. Under **DNS provider**, choose **Set up**.
2. Enter the **Provider**: a [lego provider name](https://go-acme.github.io/lego/dns/),
   lowercase, such as `cloudflare`.
3. Add its **Variables**, as lego names them, such as `CF_DNS_API_TOKEN` with
   its value. One to ten variables, named in capitals. Names starting with
   `TRAEFIK_` or ending in `_FILE` are refused. Saving replaces all of them,
   and values are never shown again.
4. Choose **Save**, then **Re-provision** the server to apply it.

Re-provisioning writes each variable to a root-only file under
`/opt/paas-traefik/dns` on the server and restarts Traefik with the DNS
resolver. The server's routes pause for a few seconds; certificates are kept.
**Remove** takes the provider away, and the next **Re-provision** deletes the
credentials from the server.

Without a DNS provider, a deploy with a wildcard domain is refused before
anything changes. Wildcards don't run on the panel's own server: deploy the
service to a server you added.

## What Toft doesn't do

- **Default hostnames after creation.** A service gets a generated name only
  when it is created with one (above); add a domain to reach any other
  service. Preview environments get `sslip.io` names without a preview domain
  ([Previews](/toft/guide/previews/)).
- **Redirects between names.** There are no redirect rules, such as `www` to
  the bare domain. Add both names to the service, or redirect in your app.
- **Certificates you bring.** Every certificate comes from Let's Encrypt.
