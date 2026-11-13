"use client";

import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { useState } from "react";
import { BreadcrumbProvider } from "@/context/BreadcrumbContext";
import { ReauthProvider } from "@/components/reauth";
import { useEventStream } from "@/lib/hooks/useEventStream";
import { useTheme } from "@/components/theme-provider";
import { certificateRecorded } from "@/components/ui/temporary-chip";
import type { DomainCheck } from "@/lib/api/domain.service";
import { refreshCounts } from "@/lib/queries";

/** Mounts the global SSE event stream. It needs the session, so it goes
 *  inside AuthProvider (the root layout). */
export function EventStreamMount() {
  useEventStream();
  return null;
}

// Neutral toasts take the theme's raised surface; richColors keeps sonner's
// own success and error colours, which it switches with `theme`.
const TOASTER_STYLE = {
  "--normal-bg": "var(--raised)",
  "--normal-text": "var(--text)",
  "--normal-border": "var(--border)",
} as React.CSSProperties;

export function Providers({ children }: { children: React.ReactNode }) {
  const { mode } = useTheme();
  // Create QueryClient inside state so it isn't re-created on HMR
  const [queryClient] = useState(
    () => {
      const client: QueryClient = new QueryClient({
        // The shell's health dots and ⌘K read ["workspace-services"], the
        // project pages ["project-services"], a project's Overview
        // ["project-overview"].
        // Deploys refresh it over SSE, but stop, delete and create send no
        // event, so refresh it after any successful mutation. Only the
        // mounted queries (at most the sidebar's projects) refetch.
        // The count badges (Needs you, My issues, a project's issues, docs
        // and services) follow any change too.
        mutationCache: new MutationCache({
          onSuccess: () => {
            refreshCounts(client);
            void client.invalidateQueries({ queryKey: ["project-services"] });
            void client.invalidateQueries({ queryKey: ["workspace-services"] });
            void client.invalidateQueries({ queryKey: ["project-overview"] });
          },
        }),
        // A domain check records the certificates it finds valid (D-150);
        // refresh the lists that still show them pending (the service
        // header, the Domains page), so both say the same.
        queryCache: new QueryCache({
          onSuccess: (data, query) => {
            if (query.queryKey[0] !== "domain-check") return;
            for (const queryKey of [["domains", query.queryKey[1]], ["workspace-domains"]]) {
              if (certificateRecorded(data as DomainCheck, client.getQueryData(queryKey))) void client.invalidateQueries({ queryKey });
            }
          },
        }),
        defaultOptions: {
          queries: {
            staleTime: 30 * 1000,  // 30 s
            retry: 1,
          },
        },
      });
      return client;
    }
  );

  return (
    <QueryClientProvider client={queryClient}>
      <BreadcrumbProvider>
        <ReauthProvider>{children}</ReauthProvider>
      </BreadcrumbProvider>
      <Toaster richColors position="bottom-right" theme={mode ?? "system"} style={TOASTER_STYLE} />
    </QueryClientProvider>
  );
}
