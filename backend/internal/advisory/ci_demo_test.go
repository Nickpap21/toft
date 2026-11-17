package advisory

import "testing"

// Deliberately failing: proves the backend summary check goes red. Never merged.
func TestCIDemoFails(t *testing.T) { t.Fatal("deliberate failure for the CI demo") }
