import { redirect } from "next/navigation";

// The old log viewer's address: deployments are the service's Deployments
// tab now, each with a page of its own (D-44).
export default async function DeploymentsRedirect({ params, searchParams }: { params: Promise<{ id: string; serviceId: string }>; searchParams: Promise<{ deployment?: string }> }) {
  const [{ id, serviceId }, { deployment }] = await Promise.all([params, searchParams]);
  redirect(`/projects/${id}/services/${serviceId}?tab=deployments${deployment ? `&deployment=${encodeURIComponent(deployment)}` : ""}`);
}
