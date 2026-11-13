package docker

import "testing"

// Free is df's Available, not size minus used: the blocks reserved for root
// are in neither Used nor Available.
func TestParseDFBytesFreeIsAvailable(t *testing.T) {
	out := "Filesystem 1B-blocks Used Available Capacity Mounted on\n/dev/sda1 80300000000 19500000000 57500000000 26% /\n"
	size, used, free, err := parseDFBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if size != 80_300_000_000 || used != 19_500_000_000 || free != 57_500_000_000 {
		t.Errorf("size, used, free = %d, %d, %d", size, used, free)
	}
}
