import { redirect } from "next/navigation";

// Observability is the Logs and metrics tab of Workspace services now (D-147).
export default function ObservabilityRedirect() {
  redirect("/shared?tab=logs");
}
