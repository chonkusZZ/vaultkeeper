package server

import "testing"

func TestValidSnapPath(t *testing.T) {
	ok := []string{"/", "/data/x", "/C/Users/me", `C:\Users\me`, "D:/x"}
	bad := []string{"", "relative/x", "-rf", "--include", "/a\x00b", "/a\nb", "x"}
	for _, p := range ok {
		if !validSnapPath(p) {
			t.Errorf("%q should be valid", p)
		}
	}
	for _, p := range bad {
		if validSnapPath(p) {
			t.Errorf("%q should be rejected", p)
		}
	}
}

func TestSnapshotIDs(t *testing.T) {
	for _, s := range []string{"latest", "f180d04c", "f180d04c30040b7725cd4cacc90110916484fe9a9c61ef6bd95c1239bf8c6be1"} {
		if !snapRe.MatchString(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "--help", "abc", "f180d04c; rm -rf /", "../x"} {
		if snapRe.MatchString(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestAsciiName(t *testing.T) {
	if got := asciiName("rép\"ort\\.txt"); got != `r_p_ort_.txt` {
		t.Errorf("got %q", got)
	}
}
