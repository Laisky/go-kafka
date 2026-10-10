package dependencysecurity_test

import (
	"encoding/binary"
	"github.com/klauspost/compress/s2"
	"math"
	"testing"
)

func TestS2DictionaryRejectsRepeatOverflow(t *testing.T) {
	for _, repeat := range []uint64{math.MaxInt64 + 1, math.MaxUint64} {
		var header [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(header[:], repeat)
		data := append(append([]byte{}, header[:n]...), make([]byte, s2.MinDictSize)...)
		if s2.NewDict(data) != nil {
			t.Fatalf("overflowed repeat %d accepted", repeat)
		}
	}
	var header [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(header[:], uint64(s2.MinDictSize))
	data := append(append([]byte{}, header[:n]...), make([]byte, s2.MinDictSize)...)
	if s2.NewDict(data) == nil {
		t.Fatal("valid boundary dictionary rejected")
	}
}
