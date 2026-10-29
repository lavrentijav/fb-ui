package audit

import "testing"

func TestRecordable(t *testing.T) {
	for _, c := range []struct {
		m, p string
		want bool
	}{
		{"GET", "/panel/api/inbounds/list", false},
		{"POST", "/panel/api/inbounds/add", true},
		{"DELETE", "/panel/api/x", true},
		{"POST", "/panel/api/server/status", false},
	} {
		if got := Recordable(c.m, c.p); got != c.want {
			t.Fatalf("%s %s = %v", c.m, c.p, got)
		}
	}
	if Actor("", "") != "unknown" || Actor("root", "admin") != "root" || Actor("", "admin") != "token:admin" {
		t.Fatal("actor naming")
	}
}
