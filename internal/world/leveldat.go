// Package world imports and exports Minecraft worlds.
package world

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// LevelInfo is what we read from level.dat.
type LevelInfo struct {
	DataVersion int32  // world format version
	VersionName string // e.g. "1.21.1"; empty for very old worlds
	LevelName   string
}

// NBT tag types.
const (
	tagEnd = iota
	tagByte
	tagShort
	tagInt
	tagLong
	tagFloat
	tagDouble
	tagByteArray
	tagString
	tagList
	tagCompound
	tagIntArray
	tagLongArray
)

const (
	maxDepth    = 64
	maxArray    = 16 << 20 // elements
	maxLevelDat = 32 << 20 // decompressed bytes
)

var errNBT = errors.New("invalid level.dat")

// ParseLevelDat reads a gzip-compressed level.dat and extracts version
// information. Input is untrusted: sizes and depth are bounded.
func ParseLevelDat(r io.Reader) (*LevelInfo, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNBT, err)
	}
	defer gz.Close()
	p := &nbt{r: bufio.NewReader(io.LimitReader(gz, maxLevelDat))}
	typ, err := p.byte()
	if err != nil || typ != tagCompound {
		return nil, errNBT
	}
	if _, err := p.str(); err != nil {
		return nil, errNBT
	}
	root, err := p.payload(tagCompound, 0)
	if err != nil {
		return nil, err
	}
	data, ok := root.(map[string]any)["Data"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: no Data compound", errNBT)
	}
	info := &LevelInfo{}
	if v, ok := data["DataVersion"].(int32); ok {
		info.DataVersion = v
	}
	if v, ok := data["Version"].(map[string]any); ok {
		info.VersionName, _ = v["Name"].(string)
	}
	info.LevelName, _ = data["LevelName"].(string)
	if info.DataVersion == 0 {
		return nil, fmt.Errorf("%w: no DataVersion (world predates 1.9)", errNBT)
	}
	return info, nil
}

type nbt struct{ r *bufio.Reader }

func (p *nbt) byte() (byte, error) { return p.r.ReadByte() }

func (p *nbt) read(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(p.r, b)
	return b, err
}

func (p *nbt) i16() (int16, error) {
	b, err := p.read(2)
	return int16(binary.BigEndian.Uint16(b)), err
}

func (p *nbt) i32() (int32, error) {
	b, err := p.read(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.BigEndian.Uint32(b)), nil
}

func (p *nbt) i64() (int64, error) {
	b, err := p.read(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b)), nil
}

func (p *nbt) str() (string, error) {
	n, err := p.i16()
	if err != nil {
		return "", err
	}
	b, err := p.read(int(uint16(n)))
	return string(b), err
}

func (p *nbt) arrayLen() (int, error) {
	n, err := p.i32()
	if err != nil {
		return 0, err
	}
	if n < 0 || n > maxArray {
		return 0, errNBT
	}
	return int(n), nil
}

// skip discards n bytes without allocating them.
func (p *nbt) skip(n int) error {
	_, err := p.r.Discard(n)
	return err
}

func (p *nbt) payload(typ byte, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errNBT
	}
	switch typ {
	case tagByte:
		b, err := p.byte()
		return int8(b), err
	case tagShort:
		return p.i16()
	case tagInt:
		return p.i32()
	case tagLong:
		return p.i64()
	case tagFloat:
		v, err := p.i32()
		return math.Float32frombits(uint32(v)), err
	case tagDouble:
		v, err := p.i64()
		return math.Float64frombits(uint64(v)), err
	case tagString:
		return p.str()
	case tagByteArray, tagIntArray, tagLongArray:
		n, err := p.arrayLen()
		if err != nil {
			return nil, err
		}
		size := map[byte]int{tagByteArray: 1, tagIntArray: 4, tagLongArray: 8}[typ]
		return nil, p.skip(n * size) // contents not needed
	case tagList:
		et, err := p.byte()
		if err != nil {
			return nil, err
		}
		n, err := p.arrayLen()
		if err != nil {
			return nil, err
		}
		if et == tagEnd && n > 0 {
			return nil, errNBT
		}
		out := make([]any, 0, min(n, 1024))
		for range n {
			v, err := p.payload(et, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case tagCompound:
		m := map[string]any{}
		for {
			t, err := p.byte()
			if err != nil {
				return nil, err
			}
			if t == tagEnd {
				return m, nil
			}
			if t > tagLongArray {
				return nil, errNBT
			}
			name, err := p.str()
			if err != nil {
				return nil, err
			}
			v, err := p.payload(t, depth+1)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
	}
	return nil, errNBT
}
