package main

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
)

// An object's bytes describe themselves: a header with a magic, the object's name hash, its
// version and its size, then bytes that follow from the name and the version. A reader can so
// tell which version it got, and whether a single byte of it is missing or wrong.
const (
	headerLen = 32
	magic     = 0x4152554d41434853 // "ARUMACHS"
)

func nameHash(name string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return h.Sum64()
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// content is the reader of version v of name, size bytes long (at least headerLen), from off.
type content struct {
	name     string
	v, size  int64
	off      int64
	seed     uint64
	hdr      [headerLen]byte
	end      int64
	word     uint64
	wordFrom int64
}

func newContent(name string, v, size, off, n int64) *content {
	c := &content{name: name, v: v, size: size, off: off, end: off + n, seed: nameHash(name) ^ uint64(v)*0x9e3779b97f4a7c15, wordFrom: -1}
	binary.BigEndian.PutUint64(c.hdr[0:], magic)
	binary.BigEndian.PutUint64(c.hdr[8:], nameHash(name))
	binary.BigEndian.PutUint64(c.hdr[16:], uint64(v))
	binary.BigEndian.PutUint64(c.hdr[24:], uint64(size))
	return c
}

func (c *content) byteAt(i int64) byte {
	if i < headerLen {
		return c.hdr[i]
	}
	w := i / 8
	if w != c.wordFrom {
		c.word, c.wordFrom = splitmix(c.seed+uint64(w)), w
	}
	return byte(c.word >> (8 * uint(i%8)))
}

func (c *content) Read(p []byte) (int, error) {
	if c.off >= c.end {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), c.end-c.off))
	for i := range n {
		p[i] = c.byteAt(c.off + int64(i))
	}
	c.off += int64(n)
	return n, nil
}

func body(name string, v, size, off, n int64) []byte {
	b := make([]byte, n)
	_, _ = io.ReadFull(newContent(name, v, size, off, n), b)
	return b
}

func md5Of(name string, v, size int64) string {
	h := md5.New()
	_, _ = io.Copy(h, newContent(name, v, size, 0, size))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

var errNotOurs = errors.New("not an object this tool wrote")

// verify reads an object's bytes whole and returns the version they hold; an error says where
// they are wrong: another name, cut short, or a byte that is not the one written.
func verify(r io.Reader, name string) (v int64, err error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, fmt.Errorf("header: %w", err)
	}
	if binary.BigEndian.Uint64(hdr[0:]) != magic || binary.BigEndian.Uint64(hdr[8:]) != nameHash(name) {
		return 0, errNotOurs
	}
	v = int64(binary.BigEndian.Uint64(hdr[16:]))
	size := int64(binary.BigEndian.Uint64(hdr[24:]))
	want := newContent(name, v, size, headerLen, size-headerLen)
	buf := make([]byte, 64<<10)
	exp := make([]byte, 64<<10)
	off := int64(headerLen)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if off+int64(n) > size {
				return v, fmt.Errorf("version %d: more than its %d bytes", v, size)
			}
			_, _ = io.ReadFull(want, exp[:n])
			for i := range n {
				if buf[i] != exp[i] {
					return v, fmt.Errorf("version %d: byte %d is wrong", v, off+int64(i))
				}
			}
			off += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return v, fmt.Errorf("version %d: read at %d: %w", v, off, rerr)
		}
	}
	if off != size {
		return v, fmt.Errorf("version %d: %d of its %d bytes", v, off, size)
	}
	return v, nil
}
