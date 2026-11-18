package mail

import "testing"

func TestSPFAllows(t *testing.T) {
	ip, host := "203.0.113.5", "mail.example.com"

	cases := []struct {
		record   string
		mx, apex bool
		want     bool
	}{
		{"v=spf1 a:mail.example.com ~all", false, false, true},
		{"v=spf1 ip4:203.0.113.5 -all", false, false, true},
		{"v=spf1 ip4:203.0.113.0/24 -all", false, false, true},
		{"v=spf1 mx ~all", true, false, true},
		{"v=spf1 mx ~all", false, false, false},
		{"v=spf1 a ~all", false, true, true},
		{"v=spf1 include:_spf.google.com ~all", true, true, false},
		{"v=spf1 +a:mail.example.com ~all", false, false, true},
	}

	for _, c := range cases {
		if got := spfAllows(c.record, ip, host, c.mx, c.apex); got != c.want {
			t.Errorf("%q (mx %v, apex %v): got %v", c.record, c.mx, c.apex, got)
		}
	}
}
