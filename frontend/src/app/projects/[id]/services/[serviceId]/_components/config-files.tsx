"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { FileCode2, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  configFileService,
  type ConfigFile,
} from "@/lib/api/configfile.service";
import { ApiError } from "@/lib/api/client";
import { readFailed } from "@/components/shell/unreachable";
import type { Service } from "@/lib/api/service.service";
import { cn } from "@/lib/utils";
import { isoTime } from "./deployment-view";

// Config files (D-45), part of Settings → Storage (board:
// ReworkServiceSettings.dc.html, D-155), whose footer adds one. Files are
// written on the server and mounted read-only into the container at each
// deploy.

const input =
  "h-9 w-full rounded-md border border-strong/70 bg-ground px-2.5 font-mono text-[13px] text-fg placeholder:text-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus";

const basename = (p: string) => p.split("/").filter(Boolean).pop() ?? p;
const dirname = (p: string) => {
  const parts = p.split("/").filter(Boolean);
  return parts.length <= 1 ? "/" : `/${parts.slice(0, -1).join("/")}`;
};

interface ConfigFilesProps {
  service: Service;
  canDeploy: boolean;
  onDeploy: () => void;
  /** The add form, opened by the card's Add config file. */
  adding: boolean;
  onAdding: (open: boolean) => void;
}

export function ConfigFiles({
  service,
  canDeploy,
  onDeploy,
  adding,
  onAdding,
}: ConfigFilesProps) {
  const serviceId = service.id;
  const name = service.name;
  const queryClient = useQueryClient();
  const read = useQuery({
    queryKey: ["config-files", serviceId],
    queryFn: () => configFileService.list(serviceId),
    staleTime: 30_000,
  });
  const { data, isPending, refetch } = read;
  const files = [...(data ?? [])].sort((a, b) => a.path.localeCompare(b.path));
  const [picked, setPicked] = useState<string | null>(null);
  const selected = files.find((f) => f.id === picked) ?? files[0] ?? null;
  // Edits per file, kept when another file is opened.
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const dirty = (f: ConfigFile) =>
    drafts[f.id] !== undefined && drafts[f.id] !== f.content;
  const [newPath, setNewPath] = useState("");
  const [pathError, setPathError] = useState("");
  const [deleting, setDeleting] = useState<ConfigFile | null>(null);

  const anyDirty = files.some(dirty);
  useEffect(() => {
    if (!anyDirty) return;
    const handler = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [anyDirty]);

  const deployNow = canDeploy && service.runtime_status !== "idle";
  const saved = (message: string) =>
    toast.success(
      message,
      deployNow
        ? {
            description: "It applies from the next deploy.",
            action: { label: "Deploy now", onClick: onDeploy },
          }
        : undefined,
    );

  const save = useMutation({
    mutationFn: (f: { path: string; content: string }) =>
      configFileService.upsert(serviceId, f),
    onSuccess: (file, f) => {
      queryClient.invalidateQueries({ queryKey: ["config-files", serviceId] });
      setDrafts((d) => {
        const n = { ...d };
        delete n[file.id];
        return n;
      });
      setPicked(file.id);
      if (f.content === "" && !files.some((x) => x.path === f.path)) {
        onAdding(false);
        setNewPath("");
        toast.success(`${basename(file.path)} added`, {
          description: "Write its content, then save.",
        });
      } else saved(`${basename(file.path)} saved`);
    },
    // 400 explains an invalid path (absolute container path, no "..").
    onError: (err: unknown, f) => {
      const msg =
        err instanceof ApiError && err.status === 400 ? err.message : "";
      if (adding && f.content === "" && msg) setPathError(msg);
      else toast.error(msg || "Failed to save the config file");
    },
  });
  const remove = useMutation({
    mutationFn: (f: ConfigFile) => configFileService.delete(serviceId, f.id),
    onSuccess: (_, f) => {
      queryClient.invalidateQueries({ queryKey: ["config-files", serviceId] });
      setDrafts((d) => {
        const n = { ...d };
        delete n[f.id];
        return n;
      });
      if (picked === f.id) setPicked(null);
      setDeleting(null);
      saved(`${basename(f.path)} deleted`);
    },
    onError: () => toast.error("Failed to delete the config file"),
  });

  const add = () => {
    const p = newPath.trim();
    if (!p) return;
    if (files.some((f) => f.path === p)) {
      setPathError(`${p} is already a config file of ${name}.`);
      return;
    }
    save.mutate({ path: p, content: "" });
  };
  const addForm = (
    <form
      className="flex flex-col gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        add();
      }}
    >
      <label htmlFor="cf-path" className="text-[13px] text-fg">
        Path in the container
      </label>
      <div className="flex flex-wrap gap-2">
        <input
          id="cf-path"
          autoFocus
          className={cn(
            input,
            "min-w-0 flex-[1_1_240px]",
            pathError && "border-status-failed",
          )}
          value={newPath}
          placeholder="/etc/nginx/conf.d/default.conf"
          spellCheck={false}
          aria-invalid={!!pathError}
          aria-describedby="cf-path-help"
          onChange={(e) => {
            setNewPath(e.target.value);
            setPathError("");
          }}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              onAdding(false);
              setNewPath("");
              setPathError("");
            }
          }}
        />
        <Button
          type="submit"
          variant="outline"
          disabled={!newPath.trim() || save.isPending}
        >
          {save.isPending ? "Adding…" : "Add"}
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={() => {
            onAdding(false);
            setNewPath("");
            setPathError("");
          }}
        >
          Cancel
        </Button>
      </div>
      <p
        id="cf-path-help"
        className={cn(
          "text-xs",
          pathError ? "text-status-failed-fg" : "text-muted",
        )}
      >
        {pathError ||
          "An absolute path to a file, such as /etc/app/config.yml. The file replaces whatever the image has there."}
      </p>
    </form>
  );

  if (readFailed(read))
    return (
      <div className="flex flex-col items-start gap-2 text-[13px]">
        <p className="text-fg">Could not load the config files.</p>
        <Button variant="outline" size="sm" onClick={() => refetch()}>
          Try again
        </Button>
      </div>
    );
  if (isPending)
    return (
      <div className="h-24 rounded-lg bg-raised/40 motion-safe:animate-pulse" />
    );

  if (files.length === 0)
    return (
      <div className="flex flex-col gap-3">
        <p className="max-w-[640px] text-[13px] leading-5 text-muted">
          {name} has no config files. A config file is written on the server
          and mounted read-only into the container at each deploy, at the path
          you give: an nginx site, an app&apos;s config.yml. For values,
          variables and secrets are simpler.
        </p>
        {canDeploy && adding && <div className="max-w-[640px]">{addForm}</div>}
      </div>
    );

  const content = selected ? (drafts[selected.id] ?? selected.content) : "";
  const unsaved = files.filter(dirty).length;

  return (
    <div className="flex flex-col gap-3">
      <p className="text-[13px] text-muted">
        Written on the server and mounted read-only into the container at each
        deploy. Changes apply from the next deploy.
      </p>
      <div className="grid overflow-hidden rounded-lg border border-subtle md:grid-cols-[280px_1fr]">
        <div className="flex min-w-0 flex-col border-b border-subtle md:border-b-0 md:border-r">
          <div className="flex items-center justify-between gap-2 border-b border-subtle px-3 py-2.5">
            <span className="text-[13px] font-semibold text-fg">
              Files{" "}
              <span className="font-normal text-muted">· {files.length}</span>
            </span>
          </div>
          <ul className="flex flex-col py-1">
            {files.map((f) => {
              const on = selected?.id === f.id;
              return (
                <li
                  key={f.id}
                  className={cn(
                    "group flex items-center gap-1 pr-1.5",
                    on && "bg-accent-soft",
                  )}
                >
                  <button
                    type="button"
                    aria-current={on ? "true" : undefined}
                    onClick={() => setPicked(f.id)}
                    className="flex min-w-0 flex-1 items-start gap-2.5 px-3 py-2 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus"
                  >
                    <FileCode2
                      className={cn(
                        "mt-0.5 h-4 w-4 flex-none",
                        on ? "text-link" : "text-muted",
                      )}
                      aria-hidden="true"
                    />
                    <span className="flex min-w-0 flex-col">
                      <span className="flex items-center gap-1.5">
                        <span
                          className="truncate font-mono text-xs font-medium text-fg"
                          title={f.path}
                        >
                          {basename(f.path)}
                        </span>
                        {dirty(f) && (
                          <span className="inline-flex items-center gap-1 text-[11px] text-status-pending-fg">
                            <span
                              className="h-1.5 w-1.5 rounded-full bg-current"
                              aria-hidden="true"
                            />
                            unsaved
                          </span>
                        )}
                      </span>
                      <span
                        className="truncate font-mono text-[11px] text-muted"
                        title={f.path}
                      >
                        {dirname(f.path)}
                      </span>
                    </span>
                  </button>
                  {canDeploy && (
                    <button
                      type="button"
                      aria-label={`Delete ${f.path}`}
                      onClick={() => setDeleting(f)}
                      className="inline-flex h-7 w-7 flex-none items-center justify-center rounded-md text-muted hover:bg-raised hover:text-status-failed-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-focus pointer-coarse:h-11 pointer-coarse:w-11"
                    >
                      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
          {canDeploy && adding && (
            <div className="border-t border-subtle p-3">{addForm}</div>
          )}
        </div>

        {selected && (
          <div className="flex min-w-0 flex-col">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-subtle px-4 py-2.5">
              <span className="min-w-0 break-all font-mono text-xs text-fg">
                {selected.path}
              </span>
              <span className="text-xs text-muted">
                read-only in the container
              </span>
              <span className="flex-1" />
              <span className="font-mono text-xs text-muted">
                saved {isoTime(selected.updated_at)}
              </span>
            </div>
            <label htmlFor="cf-content" className="sr-only">
              Content of {selected.path}
            </label>
            <textarea
              id="cf-content"
              value={content}
              readOnly={!canDeploy}
              spellCheck={false}
              onChange={(e) =>
                setDrafts((d) => ({ ...d, [selected.id]: e.target.value }))
              }
              className="min-h-[420px] w-full flex-1 resize-y bg-ground p-4 font-mono text-xs leading-5 text-fg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-focus"
            />
            {canDeploy && dirty(selected) && (
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-subtle bg-raised/40 px-4 py-2.5 text-[13px]">
                <span className="text-fg">
                  Unsaved changes{unsaved > 1 ? ` · ${unsaved} files` : ""}
                </span>
                <span className="text-muted">
                  Applies from the next deploy.
                </span>
                <span className="flex-1" />
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={save.isPending}
                  onClick={() =>
                    setDrafts((d) => {
                      const n = { ...d };
                      delete n[selected.id];
                      return n;
                    })
                  }
                >
                  Discard
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={save.isPending}
                  onClick={() => save.mutate({ path: selected.path, content })}
                >
                  {save.isPending ? "Saving…" : "Save"}
                </Button>
              </div>
            )}
          </div>
        )}
      </div>

      <Dialog
        open={deleting !== null}
        onOpenChange={(o) => !o && !remove.isPending && setDeleting(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              Delete {deleting ? basename(deleting.path) : ""}?
            </DialogTitle>
            <DialogDescription>
              <span className="font-mono">{deleting?.path}</span> is deleted
              from the panel. The running container keeps it until the next
              deploy, which starts without it. This can&apos;t be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setDeleting(null)}
              disabled={remove.isPending}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => deleting && remove.mutate(deleting)}
              disabled={remove.isPending}
            >
              {remove.isPending ? "Deleting…" : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
