"use client";

import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ApiError } from "@/lib/api/client";
import { environmentService, type Environment } from "@/lib/api/environment.service";
import { cn } from "@/lib/utils";
import { field, label, outline, primary } from "./ui";

/** New environment, from the Overview and the sidebar's environment switcher. Its
 *  colour and protection follow the name (production is protected)
 *  and are changed on its page. */
export function NewEnvironmentDialog({
  projectId,
  open,
  onOpenChange,
  onCreated,
}: {
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated?: (env: Environment) => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: (n: string) => environmentService.create(projectId, n),
    onSuccess: (env) => {
      queryClient.invalidateQueries({ queryKey: ["environments", projectId] });
      queryClient.invalidateQueries({ queryKey: ["project-overview", projectId] });
      queryClient.invalidateQueries({ queryKey: ["workspace-environments"] });
      toast.success(`${env.name} created`);
      setName("");
      onOpenChange(false);
      onCreated?.(env);
    },
    onError: (err: unknown) =>
      toast.error(
        err instanceof ApiError && err.status === 409
          ? "An environment with this name exists"
          : "Failed to create the environment",
      ),
  });

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !create.isPending && onOpenChange(false)}>
      <DialogContent className="max-w-[480px]">
        <DialogHeader>
          <DialogTitle>New environment</DialogTitle>
          <DialogDescription>
            An environment holds its own services, shared variables and secrets,
            such as a staging copy of production. Its colour and protection
            follow its name; change them on its page.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate(name.trim());
          }}
          className="flex flex-col gap-4"
        >
          <div className="flex flex-col gap-1.5">
            <label htmlFor="ne-name" className={label}>
              Name
            </label>
            <input
              id="ne-name"
              autoFocus
              required
              maxLength={100}
              placeholder="staging"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className={cn(field, "font-mono text-[13px]")}
            />
          </div>
          <DialogFooter>
            <button type="button" className={outline} onClick={() => onOpenChange(false)} disabled={create.isPending}>
              Cancel
            </button>
            <button type="submit" className={primary} disabled={!name.trim() || create.isPending}>
              {create.isPending ? "Creating…" : "Create environment"}
            </button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
