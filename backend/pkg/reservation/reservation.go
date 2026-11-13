// Package reservation is the fixed share D-62 keeps on a server that runs
// both the panel and apps (D-26): the system's, then the panel's; apps get
// the rest, through the toft-apps.slice systemd unit that toft upgrade
// writes and the panel host's facts report.
package reservation

const (
	SystemCPU    = 0.5
	SystemMemory = 1 << 30
	PanelCPU     = 1.0
	PanelMemory  = 2 << 30

	// The least the slice ever allows, so a host below the floor (which
	// the wizard keeps to Control plane only) still gets a valid unit.
	minCPU    = 0.5
	minMemory = 512 << 20
)

// The least a server needs to run the panel and apps (D-62, D-66, D-132):
// where the apps' share stops being the slice's floor, so the three shares
// never add up to more than the host has. 3.5 GiB, which a server sold as
// 4 GB passes (it reports about 3.7 GiB after the kernel's share).
const (
	MinCPUs   = 2
	MinMemory = SystemMemory + PanelMemory + minMemory
)

// Fits reports whether a host of cpus cores and memory bytes may run the
// panel and apps.
func Fits(cpus int, memory int64) bool { return cpus >= MinCPUs && memory >= MinMemory }

// Apps is what apps on the panel's server may use together, on a host of
// cpus cores and memory bytes.
func Apps(cpus int, memory int64) (cpu float64, mem int64) {
	return max(float64(cpus)-SystemCPU-PanelCPU, minCPU), max(memory-SystemMemory-PanelMemory, minMemory)
}
