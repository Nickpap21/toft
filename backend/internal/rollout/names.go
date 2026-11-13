package rollout

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

var sanitizeRe = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName converts a service name to a DNS/container-safe lowercase string.
// Non-alphanumeric characters are collapsed to a single dash; result is capped at 30 chars.
func sanitizeName(name string) string {
	s := sanitizeRe.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}

// BlueContainerName returns the canonical blue-slot container name.
// When serviceName is empty it falls back to the legacy paas-svc-{id} prefix.
// When envName is provided the container name is prefixed with the sanitised env name,
// e.g. paas-production-myapp-abc12345-blue.
func BlueContainerName(serviceID, serviceName, envName string) string {
	if serviceName == "" {
		return "paas-svc-" + serviceID + "-blue"
	}
	id := serviceID
	if len(serviceID) >= 8 {
		id = serviceID[:8]
	}
	if envName != "" {
		return "paas-" + sanitizeName(envName) + "-" + sanitizeName(serviceName) + "-" + id + "-blue"
	}
	return "paas-" + sanitizeName(serviceName) + "-" + id + "-blue"
}

// GreenContainerName returns the canonical green-slot container name.
func GreenContainerName(serviceID, serviceName, envName string) string {
	if serviceName == "" {
		return "paas-svc-" + serviceID + "-green"
	}
	id := serviceID
	if len(serviceID) >= 8 {
		id = serviceID[:8]
	}
	if envName != "" {
		return "paas-" + sanitizeName(envName) + "-" + sanitizeName(serviceName) + "-" + id + "-green"
	}
	return "paas-" + sanitizeName(serviceName) + "-" + id + "-green"
}

// IsSlotName reports whether name has the shape of a container name the
// platform gives serviceID's own containers: a blue/green slot (any
// environment prefix and any past service name, since a rename leaves the
// old slot running until the next deploy) or the pre-blue/green
// paas-svc-<id>. Tenants cannot create such a name: compose files may not
// use container names starting with "paas-" (D-19: no exception grants it;
// only the operator's TOFT_DEPLOY_POLICY=warn migration mode lets it
// through), compose names its own containers <project>-<service>-<n>, and
// Docker names unnamed containers adjective_surname. So a container with
// this name and a matching paas.service_id label is one the platform
// created and labelled, and the label can be trusted: an image label
// cannot override the label the platform sets.
func IsSlotName(name, serviceID string) bool {
	if serviceID == "" || !strings.HasPrefix(name, "paas-") {
		return false
	}
	legacy := ContainerNameForService(serviceID)
	if name == legacy || name == legacy+"-blue" || name == legacy+"-green" {
		return true
	}
	id := serviceID
	if len(id) >= 8 {
		id = id[:8]
	}
	return strings.HasSuffix(name, "-"+id+"-blue") || strings.HasSuffix(name, "-"+id+"-green")
}

// Labels the platform puts on the containers it runs. paas.* labels are
// reserved: the deploy policy refuses them in compose files (D-19).
const (
	LabelServiceID      = "paas.service_id"
	LabelDeploymentID   = "paas.deployment_id"
	LabelComposeProject = "com.docker.compose.project"
	LabelComposeService = "com.docker.compose.service"
)

// BasicAuthUsersLabel is the label holding the htpasswd entry of serviceID's
// basic-auth middleware.
func BasicAuthUsersLabel(serviceID string) string {
	return fmt.Sprintf("traefik.http.middlewares.paas-%s-basicauth.basicauth.users", serviceID[:8])
}

// htpasswdCost is the bcrypt cost of the htpasswd entries the platform
// writes. The entries sit in container labels, readable to anyone with
// Docker on the server, and Traefik checks one on every request: 8 makes
// guessing 16 times slower than the minimum and stays a few milliseconds
// per request. Entries written at the old cost (4) are replaced when that
// costs no time without routes (StartSlot), or the password changes.
const htpasswdCost = 8

func ContainerNameForService(serviceID string) string {
	return "paas-svc-" + serviceID
}

// ComposeProjectName returns the docker compose project name for a service.
// When serviceName is empty it falls back to the legacy paas-{id[:8]} prefix.
func ComposeProjectName(serviceID, serviceName string) string {
	id := serviceID
	if len(id) > 8 {
		id = id[:8]
	}
	if serviceName == "" {
		return "paas-" + id
	}
	return "paas-" + sanitizeName(serviceName) + "-" + id
}

// ComposeProjectNames returns the current project name plus the legacy id-only
// name. Used so that `down` runs against both the current and any previously
// used project name (handles renames / format migrations).
func ComposeProjectNames(serviceID, serviceName string) []string {
	current := ComposeProjectName(serviceID, serviceName)
	id := serviceID
	if len(id) > 8 {
		id = id[:8]
	}
	legacy := "paas-" + id
	if legacy == current {
		return []string{current}
	}
	return []string{current, legacy}
}

// ServiceContainers lists serviceID's slot containers, running or not.
func ServiceContainers(ctx context.Context, dc *client.Client, serviceID string) ([]container.Summary, error) {
	list, err := dc.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", LabelServiceID+"="+serviceID)),
	})
	if err != nil {
		return nil, fmt.Errorf("ContainerList: %w", err)
	}
	return SlotsOf(list, serviceID), nil
}

// SlotsOf keeps the containers of list that are serviceID's own slots: the
// ones with a slot's reserved name (IsSlotName). A container can carry the
// paas.service_id label because its image does, and tenants choose their
// images, so the label alone does not make a container the service's.
func SlotsOf(list []container.Summary, serviceID string) []container.Summary {
	out := list[:0:0]
	for _, c := range list {
		if c.Labels[LabelServiceID] == serviceID && IsSlotName(SummaryName(c), serviceID) {
			out = append(out, c)
		}
	}
	return out
}

// SummaryName is a listed container's name, without the leading "/".
func SummaryName(c container.Summary) string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// IsPlatformSlot reports whether a container is a blue/green slot the
// platform created for some service (its paas.service_id label with that
// service's reserved slot name). Such a container is never in a compose
// project, whatever com.docker.compose.* labels it inherited from its image
// (containers created before those were blanked keep them).
func IsPlatformSlot(labels map[string]string, name string) bool {
	id := labels[LabelServiceID]
	return id != "" && IsSlotName(name, id)
}
