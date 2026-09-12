package keychain

import (
	"reflect"
	"testing"
)

func TestParseDump(t *testing.T) {
	dump := `keychain: "/Users/x/Library/Keychains/login.keychain-db"
version: 512
class: "genp"
attributes:
    "acct"<blob>="zeta-token"
    "svce"<blob>="onyx"
keychain: "/Users/x/Library/Keychains/login.keychain-db"
class: "genp"
attributes:
    "acct"<blob>="other-app-key"
    "svce"<blob>="something-else"
keychain: "/Users/x/Library/Keychains/login.keychain-db"
class: "genp"
attributes:
    "acct"<blob>="alpha-key"
    "svce"<blob>="onyx"
`
	got := parseDump(dump)
	want := []string{"alpha-key", "zeta-token"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDump = %v, want %v", got, want)
	}
}

func TestValidKey(t *testing.T) {
	for _, ok := range []string{"a", "github-token", "x.y_z", "A1"} {
		if err := ValidKey(ok); err != nil {
			t.Errorf("ValidKey(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-lead", "has space", "semi;colon", "slash/x"} {
		if err := ValidKey(bad); err == nil {
			t.Errorf("ValidKey(%q) accepted", bad)
		}
	}
}
