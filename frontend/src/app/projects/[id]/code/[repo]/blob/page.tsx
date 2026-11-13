"use client";

import { useEffect, useState } from "react";
import Image from "next/image";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Download, History } from "lucide-react";
import { ApiError } from "@/lib/api/client";
import { useDocumentTitle } from "@/lib/use-document-title";
import { cn } from "@/lib/utils";
import { CommitBar, CopyButton, PathCrumbs, RefPicker } from "@/components/code/parts";
import { codeHref, errorCode, formatSize, useCode } from "@/components/code/repo";
import { CodeLines, imageURL, Markdown } from "@/components/code/views";
import { LoadFailed } from "@/components/shell/unreachable";
import { card } from "@/app/projects/_lib/ui";

const button =
  "inline-flex h-7 items-center gap-1.5 rounded-md border border-subtle bg-raised px-2.5 text-xs text-fg hover:bg-raised/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";

// A file (board CodeFile): its lines, highlighted; markdown rendered or as
// source; a raster image as a picture; anything binary as its size with
// Download. Copy and Download; no Raw view, since the panel's token isn't a
// cookie a new tab would carry (the API's raw serves scripts, D-73).
export default function BlobPage() {
  const sp = useSearchParams();
  const router = useRouter();
  const ref = sp.get("ref") ?? "";
  const path = sp.get("path") ?? "";
  const { repo, api, detail, base } = useCode();
  const [now] = useState(() => Date.now());
  const [source, setSource] = useState(false);
  const blob = useQuery({
    queryKey: ["code-blob", api.id, ref, path],
    queryFn: () => api.blob(ref, path),
    enabled: !!path,
    retry: false,
  });
  const code = errorCode(blob.error);
  useEffect(() => {
    if (code === "not_a_file") router.replace(codeHref(base, "", ref, path));
  }, [code, base, ref, path, router]);
  const name = path.split("/").pop() ?? "";
  const b = blob.data;
  const image = useQuery({
    queryKey: ["code-image", api.id, ref, path, b?.ref.sha],
    queryFn: () => imageURL(api, ref, path),
    enabled: !!b?.binary,
  });
  useDocumentTitle(`${name || "File"} · ${repo?.name ?? "Code"} · Toft`);

  const refName = b?.ref.name ?? (ref || detail.default_branch);
  const lines = b && !b.binary ? (b.text.endsWith("\n") ? b.text.slice(0, -1) : b.text).split("\n").length : 0;

  const download = async () => {
    try {
      const bytes = await api.raw(ref, path);
      // An octet-stream Blob: the browser saves it, never opens it here.
      const url = URL.createObjectURL(new Blob([bytes], { type: "application/octet-stream" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 10_000);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Couldn’t download the file");
    }
  };

  return (
    <>
      <div className="flex flex-wrap items-center gap-3">
        <RefPicker api={api} current={refName} hrefFor={(r) => codeHref(base, "blob", r, path)} />
        <PathCrumbs refName={ref} path={path} />
        <Link href={codeHref(base, "commits", ref, path)} className="inline-flex h-8 items-center gap-1.5 rounded-lg border border-strong px-2.5 text-[13px] text-fg hover:bg-raised">
          <History className="h-3.5 w-3.5" aria-hidden="true" />
          History
        </Link>
      </div>

      {blob.error && code !== "not_a_file" ? (
        <section role="alert" data-read-state="failed" className={cn(card, "flex flex-col gap-2 p-5")}>
          <h2 className="text-[15px] font-semibold text-fg">
            {code === "ref_not_found" ? `No branch, tag or commit called ${ref}` : code === "path_not_found" ? `No file at ${path} on ${refName}` : "Couldn’t read the file"}
          </h2>
          <p className="text-[13px] text-fg/70">
            <Link href={codeHref(base, "")} className="text-link hover:underline">
              Back to {detail.default_branch}
            </Link>
          </p>
        </section>
      ) : !b ? (
        <div role="status" aria-label="Loading" className="h-72 rounded-[10px] bg-surface motion-safe:animate-pulse" />
      ) : (
        <section aria-label={name} className={card}>
          {b.last_commit && <CommitBar commit={b.last_commit} now={now} />}
          <div className="flex flex-wrap items-center justify-between gap-2.5 border-b border-subtle py-2 pl-4 pr-3 text-xs text-fg/70">
            <span>
              {b.kind === "symlink" ? (
                "Symbolic link"
              ) : b.binary ? (
                <span className="font-mono text-fg">{formatSize(b.size)}</span>
              ) : (
                <>
                  <span className="font-mono text-fg">{lines}</span> {lines === 1 ? "line" : "lines"} · <span className="font-mono text-fg">{formatSize(b.size)}</span>
                </>
              )}
            </span>
            <span className="flex flex-wrap items-center gap-1.5">
              {b.html && (
                <span role="group" aria-label="Show" className="inline-flex gap-0.5 rounded-lg border border-subtle bg-ground p-0.5">
                  {[
                    { label: "Preview", on: !source, set: false },
                    { label: "Code", on: source, set: true },
                  ].map((o) => (
                    <button
                      key={o.label}
                      type="button"
                      aria-pressed={o.on}
                      onClick={() => setSource(o.set)}
                      className={cn("h-6 rounded-md px-2.5 text-xs font-medium", o.on ? "bg-raised text-fg" : "text-fg/70 hover:text-fg")}
                    >
                      {o.label}
                    </button>
                  ))}
                </span>
              )}
              {!b.binary && (
                <CopyButton text={b.text} label="Copy the file" className={button}>
                  Copy
                </CopyButton>
              )}
              <button type="button" onClick={download} aria-label={`Download ${name}`} className={cn(button, "w-7 justify-center px-0")}>
                <Download className="h-[13px] w-[13px]" aria-hidden="true" />
              </button>
            </span>
          </div>
          {b.html && !source ? (
            <div className="max-w-[860px] px-6 pb-6 pt-5">
              <Markdown html={b.html} refName={ref} />
            </div>
          ) : b.binary ? (
            image.data ? (
              <div className="flex justify-center bg-ground p-6">
                {/* A data: URL of a PNG, JPEG, GIF or WebP (imageURL). */}
                <Image src={image.data} alt={name} width={0} height={0} unoptimized className="h-auto max-h-[70vh] w-auto max-w-full" />
              </div>
            ) : image.error ? (
              <div className="px-4 py-4">
                <LoadFailed what="the image" onRetry={() => void image.refetch()} />
              </div>
            ) : (
              <p className="px-4 py-4 text-[13px] text-fg/70">{image.isLoading ? "Loading…" : "Binary file, not shown. Download it to open it."}</p>
            )
          ) : (
            <CodeLines text={b.text} path={b.kind === "symlink" ? "" : path} />
          )}
          {b.truncated && (
            <p className="border-t border-subtle px-4 py-2.5 text-xs text-muted">The file is {formatSize(b.size)}; its first megabyte is shown. Download it for the rest.</p>
          )}
        </section>
      )}
    </>
  );
}
