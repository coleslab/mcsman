package java

import "testing"

func TestMajorFor(t *testing.T) {
	cases := map[string]int{
		"1.16.5": 8, "1.17.1": 16, "1.18": 17, "1.20.4": 17,
		"1.20.5": 21, "1.21.1": 21, "1.21.10": 21, "26.1": 25,
	}
	for v, want := range cases {
		if got := MajorFor(v); got != want {
			t.Errorf("MajorFor(%s) = %d, want %d", v, got, want)
		}
	}
}
