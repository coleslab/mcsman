package world

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"testing"
)

// nbtWriter builds NBT for tests.
type nbtWriter struct{ bytes.Buffer }

func (w *nbtWriter) tag(t byte, name string) {
	w.WriteByte(t)
	binary.Write(w, binary.BigEndian, uint16(len(name)))
	w.WriteString(name)
}
func (w *nbtWriter) i32(v int32) { binary.Write(w, binary.BigEndian, v) }
func (w *nbtWriter) str(s string) {
	binary.Write(w, binary.BigEndian, uint16(len(s)))
	w.WriteString(s)
}

// LevelDat builds a minimal gzipped level.dat.
func LevelDat(dataVersion int32, name string) []byte {
	var w nbtWriter
	w.tag(tagCompound, "")
	w.tag(tagCompound, "Data")
	w.tag(tagInt, "DataVersion")
	w.i32(dataVersion)
	w.tag(tagString, "LevelName")
	w.str("My World")
	w.tag(tagLongArray, "Skipped")
	w.i32(3)
	w.Write(make([]byte, 24))
	w.tag(tagList, "Players")
	w.WriteByte(tagInt)
	w.i32(2)
	w.i32(1)
	w.i32(2)
	w.tag(tagCompound, "Version")
	w.tag(tagString, "Name")
	w.str(name)
	w.WriteByte(tagEnd)
	w.WriteByte(tagEnd)
	w.WriteByte(tagEnd)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	gz.Write(w.Bytes())
	gz.Close()
	return out.Bytes()
}

func TestParseLevelDat(t *testing.T) {
	info, err := ParseLevelDat(bytes.NewReader(LevelDat(3955, "1.21.1")))
	if err != nil {
		t.Fatal(err)
	}
	if info.DataVersion != 3955 || info.VersionName != "1.21.1" || info.LevelName != "My World" {
		t.Errorf("%+v", info)
	}
}

func TestParseLevelDatRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"not gzip": []byte("hello"),
		"huge array": func() []byte {
			var w nbtWriter
			w.tag(tagCompound, "")
			w.tag(tagByteArray, "x")
			w.i32(1 << 30)
			var out bytes.Buffer
			gz := gzip.NewWriter(&out)
			gz.Write(w.Bytes())
			gz.Close()
			return out.Bytes()
		}(),
		"truncated": LevelDat(1, "x")[:20],
	}
	for name, b := range cases {
		if _, err := ParseLevelDat(bytes.NewReader(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestRealLevelDat parses a real world when MCSMAN_TEST_LEVELDAT is set.
func TestRealLevelDat(t *testing.T) {
	p := os.Getenv("MCSMAN_TEST_LEVELDAT")
	if p == "" {
		t.Skip("set MCSMAN_TEST_LEVELDAT to a real level.dat")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := ParseLevelDat(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: DataVersion=%d Version=%q Level=%q", p, info.DataVersion, info.VersionName, info.LevelName)
}
