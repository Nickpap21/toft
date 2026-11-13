/**
 * @deprecated The shell now lives in the root layout (components/shell/
 * app-chrome.tsx). Kept as a passthrough so the pages and segment layouts
 * that still wrap themselves in it need no edits in R1. Removed in R2.
 */
export default function DashboardLayout({ children }: { children: React.ReactNode }) {
  return <>{children}</>;
}
