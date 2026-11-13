package hostproc

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func writeProc(t *testing.T, stat string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"stat":    stat,
		"meminfo": "MemTotal:        8000000 kB\nMemFree:          500000 kB\nMemAvailable:    5000000 kB\nBuffers:          100000 kB\nCached:          2900000 kB\nSwapTotal:       1000000 kB\nSwapFree:         750000 kB\n",
		"loadavg": "0.42 0.38 0.35 1/512 12345\n",
		"uptime":  "1814400.50 7000000.00\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRead(t *testing.T) {
	// user nice system idle iowait irq softirq steal guest guest_nice
	dir := writeProc(t, "cpu  100 0 50 800 50 0 0 0 30 0\ncpu0 1 2 3 4 5 6 7 8 9 10\n")
	r, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.CPUTotal != 1000 || r.CPUBusy != 150 {
		t.Errorf("cpu busy/total = %d/%d, want 150/1000 (guest left out, iowait idle)", r.CPUBusy, r.CPUTotal)
	}
	if r.MemoryTotal != 8000000*1024 || r.MemoryUsed != 3000000*1024 || r.MemoryCache != 3000000*1024 {
		t.Errorf("memory = %+v", r)
	}
	if r.SwapTotal != 1000000*1024 || r.SwapUsed != 250000*1024 {
		t.Errorf("swap = %d/%d", r.SwapUsed, r.SwapTotal)
	}
	if r.Load1 != 0.42 || r.Load15 != 0.35 {
		t.Errorf("load = %v %v", r.Load1, r.Load15)
	}
	if r.Uptime != 21*24*time.Hour+500*time.Millisecond {
		t.Errorf("uptime = %v", r.Uptime)
	}
}

func TestCPUPercent(t *testing.T) {
	prev := Reading{CPUBusy: 150, CPUTotal: 1000}
	cur := Reading{CPUBusy: 250, CPUTotal: 1400}
	if got := CPUPercent(prev, cur); got != 25 {
		t.Errorf("CPUPercent = %v, want 25", got)
	}
	// A reboot between readings resets the counters: no percentage.
	if got := CPUPercent(cur, prev); got != 0 {
		t.Errorf("CPUPercent after a reset = %v, want 0", got)
	}
}

// Disk's free is df's Available, not total minus used: the two differ by the
// blocks reserved for root. The margin is for writes between the two reads.
func TestDiskFreeIsAvailable(t *testing.T) {
	dir := t.TempDir()
	_, _, free, err := Disk(dir)
	if err != nil {
		t.Fatal(err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		t.Fatal(err)
	}
	want := int64(st.Bavail) * int64(st.Bsize)
	if d := free - want; d > 64<<20 || d < -64<<20 {
		t.Errorf("free = %d, statfs Bavail = %d", free, want)
	}
}

func TestReadRefusesMissingFiles(t *testing.T) {
	if _, err := Read(t.TempDir()); err == nil {
		t.Error("Read of an empty directory succeeded")
	}
	dir := writeProc(t, "intr 1 2 3\n")
	if _, err := Read(dir); err == nil {
		t.Error("Read without a cpu line succeeded")
	}
}
