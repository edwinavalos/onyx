package vm

import "testing"

// The balloon is opt-in: nothing in Onyx targets it, and issue #3's guest
// memory corruption happens with it attached on a paging host.
func TestWantBalloonDefaultsOff(t *testing.T) {
	t.Setenv("ONYX_BALLOON", "")
	if wantBalloon() {
		t.Fatal("balloon attached by default")
	}
	t.Setenv("ONYX_BALLOON", "1")
	if !wantBalloon() {
		t.Fatal("ONYX_BALLOON=1 did not attach the balloon")
	}
}
