package volume

import (
	"os"
	"testing"
)

func TestEnsureRejectsNonPositiveSizeWithoutCreatingVolume(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name   string
		sizeMB int64
	}{
		{name: "zero", sizeMB: 0},
		{name: "negative", sizeMB: -1},
	} {
		if _, err := Ensure(dir, tc.name, tc.sizeMB); err == nil {
			t.Errorf("Ensure(%d) succeeded", tc.sizeMB)
		}
		if _, err := os.Stat(Path(dir, tc.name)); !os.IsNotExist(err) {
			t.Errorf("Ensure(%d) left volume behind: %v", tc.sizeMB, err)
		}
	}
}
