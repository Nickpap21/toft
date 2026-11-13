package docker

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/service"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
)

func TestRoutedHostsOf(t *testing.T) {
	id := "8f14e45f-0000-0000-0000-000000000000"
	labels := buildTraefikLabels(id, []string{"api.example.com", "*.api.example.com"}, 8080, service.AccessControl{})
	// A label an image might carry for another service's router is not read.
	labels["traefik.http.routers.paas-deadbeef-0.rule"] = "Host(`evil.example.com`)"
	list := []container.Summary{
		{State: "running", Labels: labels},
		{State: "exited", Labels: map[string]string{"traefik.http.routers.paas-8f14e45f-0.rule": "Host(`old.example.com`)"}},
	}
	hosts, running := routedHostsOf(list, id)
	assert.True(t, running)
	assert.Equal(t, []string{"*.api.example.com", "api.example.com"}, hosts)

	hosts, running = routedHostsOf(list[1:], id)
	assert.False(t, running, "a stopped container routes nothing")
	assert.Empty(t, hosts)
}

// A temporary (sslip.io) name serves over HTTP, with the same middlewares as
// over HTTPS, while its certificate waits (D-150); every other name's HTTP
// router redirects.
func TestTemporaryNamesServeHTTP(t *testing.T) {
	const id = "0123456789abcdef"
	labels := buildTraefikLabels(id, []string{"shop.example.com", "web-production-shop.203-0-113-10.sslip.io"}, 8080,
		service.AccessControl{IPAllowlist: []string{"10.0.0.0/8"}})
	assert.Equal(t, "redirect-to-https@file", labels["traefik.http.routers.paas-01234567-0-http.middlewares"])
	assert.Equal(t, "secure-headers@file,paas-01234567-ipallow", labels["traefik.http.routers.paas-01234567-1-http.middlewares"])
	assert.Equal(t, labels["traefik.http.routers.paas-01234567-1.middlewares"], labels["traefik.http.routers.paas-01234567-1-http.middlewares"],
		"no less protected over HTTP")

	labels = buildTraefikLabels(id, []string{"web-production-shop.203-0-113-10.sslip.io"}, 8080,
		service.AccessControl{BasicAuthUser: "admin", BasicAuthPassword: "s3cret"})
	assert.Equal(t, "redirect-to-https@file", labels["traefik.http.routers.paas-01234567-0-http.middlewares"],
		"behind basic auth a temporary name redirects: no password over plain HTTP")

	labels = buildTraefikLabels(id, []string{"web-production-shop.203-0-113-10.sslip.io"}, 8080,
		service.AccessControl{Certified: []string{"web-production-shop.203-0-113-10.sslip.io"}})
	assert.Equal(t, "redirect-to-https@file", labels["traefik.http.routers.paas-01234567-0-http.middlewares"],
		"once its certificate is out a temporary name redirects like any other")
}
