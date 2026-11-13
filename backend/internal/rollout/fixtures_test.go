package rollout

import (
	"testing"

	"github.com/Nickpap21/toft/backend/internal/netplan"
	"github.com/Nickpap21/toft/backend/internal/rollout/fakedocker"
	"github.com/docker/docker/api/types/container"
)

const (
	slotSvcID = "1234abcd-0000-0000-0000-000000000001"
	otherSvc  = "9999ffff-0000-0000-0000-000000000002"
)

var (
	slotBlue  = BlueContainerName(slotSvcID, "web", "")
	slotGreen = GreenContainerName(slotSvcID, "web", "")

	netEnv   = netplan.Env("0e000000-0000-0000-0000-0000000000e1", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1")
	netOther = netplan.Env("0e000000-0000-0000-0000-0000000000e2", "0a000000-0000-0000-0000-0000000000a1", "0b000000-0000-0000-0000-0000000000b1")
)

func svcLabels(serviceID string) map[string]string {
	return map[string]string{"paas.service_id": serviceID}
}

func envPlan() netplan.Plan {
	return netplan.Plan{ServiceID: slotSvcID, ServiceName: "web", Alias: "web", Home: netEnv}
}

// hostWithProxy is a fake host with the control plane's network and the
// proxy on it.
func hostWithProxy(t *testing.T) (*fakedocker.Docker, *fakedocker.Container) {
	f := fakedocker.New(t)
	f.AddNetwork("paas-network", nil)
	px := f.Add("paas-traefik", container.StateRunning, nil)
	f.Attach(px, "paas-network")
	return f, px
}
