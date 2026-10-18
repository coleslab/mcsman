package cronspec

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	base := time.Date(2026, 10, 18, 3, 30, 0, 0, time.Local)
	cases := map[string]time.Time{
		"1h":        base.Add(time.Hour),
		"1d":        base.Add(24 * time.Hour),
		"0 4 * * *": time.Date(2026, 10, 18, 4, 0, 0, 0, time.Local),
		"@daily":    time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local),
	}
	for spec, want := range cases {
		s, err := Parse(spec)
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		if got := s.Next(base); !got.Equal(want) {
			t.Errorf("%s: next = %v, want %v", spec, got, want)
		}
	}
	for _, off := range []string{"", "off", "OFF"} {
		if s, err := Parse(off); s != nil || err != nil {
			t.Errorf("%q: %v %v", off, s, err)
		}
	}
	for _, bad := range []string{"1m", "every hour", "61 * * * *"} {
		if err := Validate(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
