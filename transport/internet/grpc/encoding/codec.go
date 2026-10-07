package encoding

import (
	"encoding/binary"
	"sync"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/bytespool"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/encoding/proto"
	"google.golang.org/grpc/mem"
)

// hunkDataTag is the tag of Hunk.data: field 1, length-delimited.
const hunkDataTag = 1<<3 | 2

var (
	protoCodec   = encoding.GetCodecV2(proto.Name)
	hunkDataPool = bytespool.GetPool(buf.Size)
)

// Codec is the gRPC codec of the tunnel server. It decodes Hunk data into
// pooled buffers that HunkReaderWriter adopts, and sends buffers handed over
// by HunkReaderWriter.WriteMultiBuffer without copying them. Everything else
// goes through the proto codec.
type Codec struct{}

func (Codec) Name() string { return proto.Name }

func (Codec) Marshal(v any) (mem.BufferSlice, error) {
	if h, ok := v.(*ownedHunk); ok {
		return h.marshal(), nil
	}
	return protoCodec.Marshal(v)
}

func (Codec) Unmarshal(data mem.BufferSlice, v any) error {
	if h, ok := v.(*Hunk); ok && unmarshalHunk(data, h) {
		return nil
	}
	return protoCodec.Unmarshal(data, v)
}

// unmarshalHunk decodes a Hunk that holds nothing but data, the only form
// clients send, and reports whether data was in that form.
func unmarshalHunk(data mem.BufferSlice, h *Hunk) bool {
	var hdr [1 + binary.MaxVarintLen64]byte
	n := copyAt(hdr[:], data, 0)
	if n == 0 {
		h.Data = nil
		return true
	}
	if hdr[0] != hunkDataTag {
		return false
	}
	size, m := binary.Uvarint(hdr[1:n])
	if m <= 0 {
		return false
	}
	off := 1 + m
	if size != uint64(data.Len()-off) {
		return false
	}
	var d []byte
	if size <= buf.Size {
		d = hunkDataPool.Get().([]byte)[:size]
	} else {
		d = make([]byte, size)
	}
	copyAt(d, data, off)
	h.Data = d
	return true
}

// copyAt copies data from offset off into dst and returns the bytes copied.
func copyAt(dst []byte, data mem.BufferSlice, off int) int {
	n := 0
	for _, b := range data {
		d := b.ReadOnlyData()
		if off >= len(d) {
			off -= len(d)
			continue
		}
		n += copy(dst[n:], d[off:])
		off = 0
		if n == len(dst) {
			break
		}
	}
	return n
}

// ownedHunk is a Hunk whose data is a buffer that gRPC releases once it has
// written it. It is also the mem.BufferPool the buffer goes back to.
type ownedHunk struct {
	b    *buf.Buffer
	data []byte
	hdr  [1 + binary.MaxVarintLen32]byte
}

var ownedHunkPool = sync.Pool{New: func() any { return new(ownedHunk) }}

func newOwnedHunk(b *buf.Buffer) *ownedHunk {
	h := ownedHunkPool.Get().(*ownedHunk)
	h.b = b
	h.data = b.Bytes()
	return h
}

func (h *ownedHunk) marshal() mem.BufferSlice {
	hdr := binary.AppendUvarint(append(h.hdr[:0], hunkDataTag), uint64(len(h.data)))
	return mem.BufferSlice{mem.SliceBuffer(hdr), mem.NewBuffer(&h.data, h)}
}

// Get implements mem.BufferPool. gRPC only puts buffers back.
func (h *ownedHunk) Get(length int) *[]byte {
	b := make([]byte, length)
	return &b
}

// Put implements mem.BufferPool.
func (h *ownedHunk) Put(*[]byte) {
	h.b.Release()
	*h = ownedHunk{}
	ownedHunkPool.Put(h)
}
