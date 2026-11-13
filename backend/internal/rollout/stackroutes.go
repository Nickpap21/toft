package rollout

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// StackRouteViolations returns, for the containers of a compose stack, the
// traefik.* labels that the platform did not set: on the routed service (routed,
// "" when none) any
// key outside allowed (they came from its image and would add routers), on
// every other service a traefik.enable that is not false. Platform slots
// that carry the project label from their image are not the stack's.
func StackRouteViolations(list []container.Summary, project, routed string, allowed map[string]bool) []string {
	var out []string
	for _, c := range list {
		if c.Labels[LabelComposeProject] != project || IsPlatformSlot(c.Labels, SummaryName(c)) {
			continue
		}
		if routed == "" || c.Labels["com.docker.compose.service"] != routed {
			// Traefik ignores a container unless traefik.enable is true
			// (exposedByDefault is off); the guard sets it to false.
			if v, ok := c.Labels["traefik.enable"]; ok && v != "false" {
				out = append(out, SummaryName(c)+": traefik.enable")
			}
			continue
		}
		for _, k := range TraefikKeys(c.Labels) {
			if !allowed[k] {
				out = append(out, SummaryName(c)+": "+k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// CheckStackRoutes lists the stack's containers right after `up` and
// returns an error naming every traefik.* label the platform did not set
// (StackRouteViolations); the caller then takes the stack down.
func CheckStackRoutes(ctx context.Context, dc *client.Client, project, routed string, allowed map[string]bool) error {
	list, err := dc.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelComposeProject+"="+project)),
	})
	if err != nil {
		return fmt.Errorf("check the stack's routes: ContainerList: %w", err)
	}
	if vs := StackRouteViolations(list, project, routed, allowed); len(vs) > 0 {
		return fmt.Errorf("the stack's images carry traefik.* labels the platform did not set (%s); they would add routes of "+
			"their own, so the stack was taken down. Remove them from the images", strings.Join(vs, ", "))
	}
	return nil
}
