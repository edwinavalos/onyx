package guest

import "testing"

func TestLoopbackPortStableAndInRange(t *testing.T) {
	a := loopbackPort("https://github.com")
	b := loopbackPort("https://github.com/")
	c := loopbackPort("HTTPS://GitHub.com")
	if a != b || a != c {
		t.Fatalf("port not stable across equivalent spellings: %d %d %d", a, b, c)
	}
	if a < 40000 || a >= 50000 {
		t.Fatalf("port out of range: %d", a)
	}
	if loopbackPort("https://gitlab.com") == a {
		t.Fatal("distinct upstreams collided (unlucky hash; pick another test host)")
	}
}

func TestEnvName(t *testing.T) {
	if got := envName("api.github.com"); got != "API_GITHUB_COM" {
		t.Fatalf("envName = %q", got)
	}
}
