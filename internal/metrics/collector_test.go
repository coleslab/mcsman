package metrics

import (
	"os"
	"testing"
)

func TestParseList(t *testing.T) {
	n, players, ok := ParseList("There are 2 of a max of 20 players online: Steve, Alex")
	if !ok || n != 2 || len(players) != 2 || players[1] != "Alex" {
		t.Errorf("got %d %v %v", n, players, ok)
	}
	n, players, ok = ParseList("There are 0 of a max of 20 players online: ")
	if !ok || n != 0 || len(players) != 0 {
		t.Errorf("empty: %d %v %v", n, players, ok)
	}
	if _, _, ok := ParseList("Unknown command"); ok {
		t.Error("garbage parsed")
	}
}

func TestParseTPS(t *testing.T) {
	cases := map[string]float64{
		"§6TPS from last 1m, 5m, 15m: §a20.0§r, §a20.0§r, §a20.0": 20.0,
		"§6TPS from last 1m, 5m, 15m: §a*20.0, §a19.98, §a19.5":   20.0,
		"§6TPS from last 1m, 5m, 15m: §e17.43, §a19.1, §a19.9":    17.43,
	}
	for in, want := range cases {
		if got := ParseTPS(in); got != want {
			t.Errorf("ParseTPS(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseMSPT(t *testing.T) {
	in := "§6Server tick times §e(§7avg§e/§7min§e/§7max§e)§6 from last 5s§7,§6 10s§7,§6 1m§e:\n§6◴ §a1.2§7/§a0.8§7/§a3.4§e, §a1.3§7/§a0.8§7/§a4.0"
	if got := ParseMSPT(in); got != 1.2 {
		t.Errorf("got %v", got)
	}
}

func TestRSS(t *testing.T) {
	if rssMB(os.Getpid()) <= 0 {
		t.Error("could not read own RSS")
	}
}
