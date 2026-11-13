package reservation

import "testing"

func TestApps(t *testing.T) {
	for _, c := range []struct {
		cpus    int
		mem     int64
		wantCPU float64
		wantMem int64
	}{
		{4, 8 << 30, 2.5, 5 << 30},
		{2, 4 << 30, 0.5, 1 << 30},
		{1, 2 << 30, minCPU, minMemory}, // below the floor: the floor
	} {
		cpu, mem := Apps(c.cpus, c.mem)
		if cpu != c.wantCPU || mem != c.wantMem {
			t.Errorf("Apps(%d, %d) = %v, %d; want %v, %d", c.cpus, c.mem, cpu, mem, c.wantCPU, c.wantMem)
		}
	}
}

func TestFits(t *testing.T) {
	for _, c := range []struct {
		cpus int
		mem  int64
		want bool
	}{
		{2, 3826 << 20, true},  // sold as 4 GB: about 3.7 GiB after the kernel's share
		{2, 7 << 29, true},     // 3.5 GiB, the floor itself
		{2, 3400 << 20, false}, // under the floor: the slice would promise more than there is
		{1, 8 << 30, false},
	} {
		if got := Fits(c.cpus, c.mem); got != c.want {
			t.Errorf("Fits(%d, %d) = %v; want %v", c.cpus, c.mem, got, c.want)
		}
	}
}
