package rollout

import (
	"context"
	"testing"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlankedIdentityLabels(t *testing.T) {
	image := map[string]string{
		"paas.service_id":            otherSvc,
		"PAAS.network":               "paas-env-x",
		"com.docker.compose.project": "paas-victim-9999ffff",
		"com.docker.compose.service": "db",
		"org.opencontainers.version": "1.0",
		"traefik.enable":             "true",
	}
	own := map[string]string{"paas.service_id": slotSvcID, "traefik.enable": "false"}
	got := blankedIdentityLabels(image, own)
	assert.Equal(t, map[string]string{
		"paas.service_id":            slotSvcID,
		"PAAS.network":               "",
		"com.docker.compose.project": "",
		"com.docker.compose.service": "",
		"traefik.enable":             "false",
	}, got)
	assert.Equal(t, map[string]string{"paas.service_id": slotSvcID, "traefik.enable": "false"}, own, "the input is not changed")
}

// A slot created before image labels were blanked can still carry another
// service's compose project label: it is never part of that stack.
func TestASlotWithAComposeLabelIsNotInTheStack(t *testing.T) {
	f := fakedocker.New(t)
	proj := ComposeProjectName(otherSvc, "victim")
	spoof := f.Add(slotBlue, container.StateRunning, map[string]string{
		"paas.service_id": slotSvcID, "com.docker.compose.project": proj, "com.docker.compose.service": "db",
	})
	f.AddNetwork("paas-network", nil)
	f.Attach(spoof, "paas-network")
	real := f.Add(proj+"-db-1", container.StateRunning, map[string]string{"com.docker.compose.project": proj, "com.docker.compose.service": "db"})
	f.Attach(real, "paas-network")

	o := newNetOps(f.Client(), NetConfig{}, false, nil)
	cs, err := o.endpointContainers(context.Background(), netplan.Endpoint{ServiceID: otherSvc, ServiceName: "victim", Compose: true}, nil)
	require.NoError(t, err)
	require.Len(t, cs, 1)
	assert.Equal(t, proj+"-db-1", cs[0].ref.name)
}
