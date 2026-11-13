"use client";

import type { Repository } from "@/lib/api/code.service";
import { cn } from "@/lib/utils";
import { label } from "./ui";

/** Where a git build, or a compose stack's files, come from (D-77, boards
 *  NewService and ServiceSettings): one of the project's repositories, which
 *  needs no credentials, or another repository's URL, whose field and
 *  credentials are children. value is the repository's id, "" for a URL. */
export function RepoSourceChoice({
  repos,
  value,
  onChange,
  name,
  children,
}: {
  repos: Repository[];
  value: string;
  onChange: (repositoryId: string) => void;
  name: string;
  children: React.ReactNode;
}) {
  const row = (on: boolean) =>
    cn("flex min-h-9 cursor-pointer flex-wrap items-center gap-2.5 rounded-lg border px-3 py-1.5", on ? "border-accent bg-accent-soft" : "border-strong");
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className={cn(label, "float-left mb-1.5 w-full")}>Repository</legend>
      {repos.map((r) => (
        <label key={r.id} className={row(value === r.id)}>
          <input type="radio" name={name} checked={value === r.id} onChange={() => onChange(r.id)} className="h-4 w-4 accent-accent" />
          <span className="font-mono text-[13px] text-fg">{r.full_name}</span>
          <span className="text-xs text-fg/70">this project’s repository, no credentials needed</span>
        </label>
      ))}
      <label className={row(value === "")}>
        <input type="radio" name={name} checked={value === ""} onChange={() => onChange("")} className="h-4 w-4 accent-accent" />
        <span className="text-[13px] text-fg">Another repository URL</span>
      </label>
      {value === "" && <div className="flex flex-col gap-3 pt-1">{children}</div>}
    </fieldset>
  );
}
