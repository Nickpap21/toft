// Package hostproc reads a Linux host's CPU, memory, load and uptime from
// /proc, and a filesystem's size. /proc/stat, /proc/meminfo, /proc/loadavg
// and /proc/uptime are not namespaced, so the backend's container reads the
// panel host's own (D-68).
package hostproc

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Reading is one look at the host. The CPU counters are jiffies since boot:
// CPUPercent turns two readings into a percentage.
type Reading struct {
	CPUBusy, CPUTotal uint64
	// Bytes. MemoryUsed is what MemAvailable leaves, as free -h's "used";
	// MemoryCache is the page cache and buffers, which the kernel gives back.
	MemoryTotal, MemoryUsed, MemoryCache int64
	SwapTotal, SwapUsed                  int64
	Load1, Load5, Load15                 float64
	Uptime                               time.Duration
}

// Read reads root's proc files: "/proc" on a host.
func Read(root string) (Reading, error) {
	var r Reading
	stat, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return r, err
	}
	if r.CPUBusy, r.CPUTotal, err = parseStat(stat); err != nil {
		return r, err
	}
	mem, err := os.ReadFile(filepath.Join(root, "meminfo"))
	if err != nil {
		return r, err
	}
	kb := parseMeminfo(mem)
	if kb["MemTotal"] == 0 {
		return r, fmt.Errorf("meminfo: no MemTotal")
	}
	r.MemoryTotal = kb["MemTotal"] * 1024
	r.MemoryUsed = (kb["MemTotal"] - kb["MemAvailable"]) * 1024
	r.MemoryCache = (kb["Cached"] + kb["Buffers"]) * 1024
	r.SwapTotal = kb["SwapTotal"] * 1024
	r.SwapUsed = (kb["SwapTotal"] - kb["SwapFree"]) * 1024

	load, err := os.ReadFile(filepath.Join(root, "loadavg"))
	if err != nil {
		return r, err
	}
	if f := strings.Fields(string(load)); len(f) >= 3 {
		r.Load1, _ = strconv.ParseFloat(f[0], 64)
		r.Load5, _ = strconv.ParseFloat(f[1], 64)
		r.Load15, _ = strconv.ParseFloat(f[2], 64)
	}
	up, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return r, err
	}
	if f := strings.Fields(string(up)); len(f) >= 1 {
		secs, _ := strconv.ParseFloat(f[0], 64)
		r.Uptime = time.Duration(secs * float64(time.Second))
	}
	return r, nil
}

// parseStat sums the first line of /proc/stat: busy is everything but idle
// and iowait. guest and guest_nice are left out, as user already counts them.
func parseStat(b []byte) (busy, total uint64, err error) {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 5 || f[0] != "cpu" {
		return 0, 0, fmt.Errorf("stat: no cpu line")
	}
	var idle uint64
	for i, s := range f[1:] {
		if i >= 8 {
			break
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("stat: %w", err)
		}
		total += v
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	return total - idle, total, nil
}

func parseMeminfo(b []byte) map[string]int64 {
	kb := make(map[string]int64)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err == nil {
			kb[k] = n
		}
	}
	return kb
}

// CPUPercent is the share of all CPUs busy between two readings, 0 to 100.
func CPUPercent(prev, cur Reading) float64 {
	if cur.CPUTotal <= prev.CPUTotal || cur.CPUBusy < prev.CPUBusy {
		return 0
	}
	return float64(cur.CPUBusy-prev.CPUBusy) / float64(cur.CPUTotal-prev.CPUTotal) * 100
}

// Disk is the size of the filesystem holding path, what is used on it and
// what is free, in bytes, as df counts them: free is df's Available, without
// the blocks reserved for root, so it is less than total minus used.
func Disk(path string) (total, used, free int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bs := int64(st.Bsize)
	return int64(st.Blocks) * bs, int64(st.Blocks-st.Bfree) * bs, int64(st.Bavail) * bs, nil
}
