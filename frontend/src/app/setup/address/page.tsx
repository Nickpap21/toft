"use client";

import { useEffect, useState, useSyncExternalStore } from "react";
import { useRouter } from "next/navigation";
import { useQueryClient } from "@tanstack/react-query";
import { Check, CircleX, LoaderCircle, Minus } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { setupQuery, setupService, type PanelDomainCheck, type Setup } from "@/lib/api/instance.service";
import { cn } from "@/lib/utils";
import { useReauth } from "@/components/reauth";
import { Notice } from "@/components/shell/auth-shell";
import { SetupPage } from "@/components/setup/setup-page";
import { nextStep, setupBody, setupCard, setupFoot, setupInput, setupLead, setupOption, setupPrimary } from "@/components/setup/setup-frame";

// Step 2 (board ReworkSetup, D-62): a domain with a Let's Encrypt
// certificate, or the server's IP. The API moves with the panel (same
// origin), so after the certificate the operator continues on the domain
// and signs in there again.

const noop = () => () => {};
const isIP = (host: string) => /^[\d.]+$/.test(host) || host.includes(":");
const WHY = "Changing the panel’s address moves everyone to it, so Toft asks for your password first.";

export default function AddressStep() {
  const host = useSyncExternalStore(noop, () => window.location.hostname, () => "");
  return (
    <SetupPage step="address" title="Where Toft answers">
      {(setup) => <AddressForm setup={setup} host={host} />}
    </SetupPage>
  );
}

type Phase = "form" | "dns" | "checking" | "slow" | "issued";

function AddressForm({ setup, host }: { setup: Setup; host: string }) {
  const router = useRouter();
  const queryClient = useQueryClient();
  const confirm = useReauth();
  const [mode, setMode] = useState<"domain" | "ip">(setup.domain_set && !setup.domain ? "ip" : "domain");
  const [domain, setDomain] = useState(setup.domain);
  const [phase, setPhase] = useState<Phase>("form");
  const [check, setCheck] = useState<PanelDomainCheck | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const name = domain.trim().toLowerCase().replace(/\.$/, "");

  // Following the certificate: every 3 seconds, and "slow" after 3 minutes.
  useEffect(() => {
    if (phase !== "checking" && phase !== "slow") return;
    const started = Date.now();
    const t = setInterval(async () => {
      const c = await setupService.checkDomain(name).catch(() => null);
      if (c) setCheck(c);
      if (c?.certificate.state === "issued") setPhase("issued");
      else if (Date.now() - started > 180_000) setPhase("slow");
    }, 3000);
    return () => clearInterval(t);
  }, [phase, name]);

  const fail = (err: unknown) =>
    setError(err instanceof ApiError && err.status < 500 ? err.message : "Couldn’t reach Toft. Try again.");

  const save = async (value: string, domainChecked = false) => {
    if (!(await confirm(WHY))) return false;
    const st = await setupService.setDomain(value, domainChecked);
    queryClient.setQueryData(setupQuery.queryKey, st);
    return true;
  };

  const submit = async (useAnyway = false) => {
    setError(null);
    setBusy(true);
    try {
      if (mode === "ip") {
        if (!(setup.domain_set && setup.domain === "") && !(await save(""))) return;
        router.push(nextStep("address"));
        return;
      }
      if (phase === "issued") {
        // The API and the sign-in move with the panel: sign in again there.
        if (host === name) router.push(nextStep("address"));
        else window.location.assign(`https://${name}/?next=${encodeURIComponent(nextStep("address"))}`);
        return;
      }
      const c = await setupService.checkDomain(name);
      setCheck(c);
      if (!c.reaches_panel && !useAnyway) {
        setPhase("dns");
        return;
      }
      if (name !== setup.domain || !setup.domain_set) {
        if (!(await save(name, useAnyway))) return;
        const again = await setupService.checkDomain(name);
        setCheck(again);
        setPhase(again.certificate.state === "issued" ? "issued" : "checking");
        return;
      }
      setPhase(c.certificate.state === "issued" ? "issued" : "checking");
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  };

  const cta =
    mode === "ip"
      ? "Continue with the IP"
      : { form: "Check and continue", dns: "Check again", checking: "Checking…", slow: "Check again", issued: `Continue on ${name}` }[phase];

  const help = "text-xs leading-[18px] text-muted";
  const pick = (id: "domain" | "ip") => {
    setMode(id);
    setPhase("form");
    setError(null);
  };
  return (
    <form
      className={setupCard}
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <div className={setupBody}>
        <p className={setupLead}>Where you and your team open Toft. A domain gets a real certificate, so browsers stop warning.</p>
        <fieldset className="flex flex-col gap-3.5">
          <legend className="sr-only">Where Toft answers</legend>
          <div className={cn(setupOption(mode === "domain"), "cursor-default")}>
            <input
              id="addr-on-domain"
              type="radio"
              name="addr"
              checked={mode === "domain"}
              onChange={() => pick("domain")}
              className="mt-[3px] h-4 w-4 flex-none cursor-pointer accent-[var(--link)]"
            />
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <label htmlFor="addr-on-domain" className="cursor-pointer font-medium pointer-coarse:inline-flex pointer-coarse:min-h-11 pointer-coarse:items-center">A domain</label>
              {mode === "domain" && (
                <>
                  <input
                    id="addr-domain"
                    aria-label="Domain"
                    value={domain}
                    onChange={(e) => {
                      setDomain(e.target.value);
                      setPhase("form");
                    }}
                    required
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="toft.example.com"
                    aria-describedby="addr-domain-help"
                    className={cn(setupInput, "my-1 font-mono")}
                  />
                  <span id="addr-domain-help" className="flex flex-col gap-1">
                    <span className={help}>Create this record at your DNS host first:</span>
                    <span className="flex flex-wrap items-center gap-2 rounded-lg border border-subtle bg-ground px-3 py-2 font-mono text-xs [overflow-wrap:anywhere]">
                      <span>{name || "toft.example.com"}</span>
                      <span className="text-muted">A</span>
                      {isIP(host) ? <span>{host}</span> : <span className="font-sans">this server’s IP address</span>}
                    </span>
                    <span className={help}>Toft checks it, gets a certificate from Let’s Encrypt, and moves to the new address. You sign in again there.</span>
                  </span>
                </>
              )}
            </div>
          </div>
          <label className={setupOption(mode === "ip")}>
            <input type="radio" name="addr" checked={mode === "ip"} onChange={() => pick("ip")} className="mt-[3px] h-4 w-4 flex-none accent-[var(--link)]" />
            <span className="flex min-w-0 flex-1 flex-col gap-1">
              <span className="font-medium">Keep {isIP(host) ? host : "the server’s IP address"} for now</span>
              <span className={help}>Browsers warn about its certificate until it has a domain. Set one any time in Instance.</span>
            </span>
          </label>
        </fieldset>

        {mode === "domain" && !setup.in_progress && host !== name && (
          <Notice kind="warn" className="[overflow-wrap:anywhere]">
            Once the domain is set, Toft stops answering on {host}. If the domain doesn’t reach this server, you can’t get back in.
          </Notice>
        )}
        {mode === "domain" && phase !== "form" && check && <Checks check={check} phase={phase} host={host} />}
        {mode === "domain" && phase === "dns" && check?.addresses.length ? (
          <button type="button" onClick={() => void submit(true)} disabled={busy} className="self-start text-[13px] font-medium inline-link pointer-coarse:min-h-11">
            It points here: use it anyway
          </button>
        ) : null}
        {mode === "domain" && phase === "issued" && (
          <Notice kind="done">
            <span className="font-medium text-fg">{name} is ready.</span>{" "}
            {host === name ? "Setup carries on with this server." : "Continue there and sign in again: your browser keeps a sign-in per address."}
          </Notice>
        )}
        {mode === "domain" && phase === "slow" && (
          <Notice kind="wait">
            Let’s Encrypt hasn’t issued it yet. It checks the domain over port 80, so port 80 must be open to the internet. Toft keeps
            asking; check again in a minute.
          </Notice>
        )}
        {error && <Notice kind="error">{error}</Notice>}
      </div>
      <div className={cn(setupFoot, "justify-end")}>
        <button type="submit" disabled={busy || phase === "checking" || (mode === "domain" && !name)} className={setupPrimary}>
          {busy ? "Checking…" : cta}
        </button>
      </div>
    </form>
  );
}

const ROW = {
  ok: { Icon: Check, color: "text-status-running" },
  bad: { Icon: CircleX, color: "text-status-failed" },
  wait: { Icon: LoaderCircle, color: "text-link" },
  dash: { Icon: Minus, color: "text-muted" },
} as const;

function Checks({ check, phase, host }: { check: PanelDomainCheck; phase: Phase; host: string }) {
  const here = isIP(host) ? host : "this server";
  const dns: [keyof typeof ROW, string, string] =
    check.addresses.length === 0
      ? ["bad", "doesn’t resolve", `${check.domain} has no A or AAAA record yet. Add one pointing at ${here}; a new record can take a few minutes to arrive.`]
      : check.reaches_panel
        ? ["ok", "points here", `${check.domain} resolves to ${check.addresses.join(", ")}.`]
        : ["bad", "points elsewhere", `${check.domain} resolves to ${check.addresses.join(", ")}, which doesn’t reach this panel${isIP(host) ? ` (${host})` : ""}. Change the A record; a change can take a few minutes to arrive.`];
  const exp = check.certificate.expires_at ? check.certificate.expires_at.slice(0, 10) : "";
  const cert: [keyof typeof ROW, string, string] =
    phase === "dns"
      ? ["dash", "waits for DNS", "Let’s Encrypt checks the domain over port 80 once it points here."]
      : check.certificate.state === "issued"
        ? ["ok", "issued", `${check.certificate.issuer || "Let’s Encrypt"} · valid until ${exp} · renewed automatically 30 days before it expires.`]
        : ["wait", "asking Let’s Encrypt", "This usually takes under a minute."];
  return (
    <section aria-labelledby="chk-h" aria-live="polite" className="overflow-hidden rounded-lg border border-subtle bg-ground">
      <h2 id="chk-h" className="border-b border-subtle px-3 py-2 text-[13px] font-semibold">Checks</h2>
      <ul>
        {(
          [
            ["DNS", dns],
            ["Certificate", cert],
          ] as const
        ).map(([title, [kind, state, detail]], i) => {
          const { Icon, color } = ROW[kind];
          return (
            <li key={title} className={cn("flex items-start gap-2.5 px-3 py-2.5", i > 0 && "border-t border-subtle")}>
              <Icon aria-hidden="true" className={cn("mt-0.5 h-4 w-4 flex-none", color, kind === "wait" && "motion-safe:animate-spin")} />
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="font-medium">
                  {title} <span className={cn("font-normal", color)}>{state}</span>
                </span>
                <span className="text-[13px] text-fg/70 [overflow-wrap:anywhere]">{detail}</span>
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
