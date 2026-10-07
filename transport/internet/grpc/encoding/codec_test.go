package encoding

import (
	"bytes"
	"testing"

	"github.com/xtls/xray-core/common/buf"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestCodecMarshalOwnedHunk(t *testing.T) {
	for _, size := range []int{1, 127, 128, 2000, buf.Size} {
		b := buf.New()
		data := b.Extend(int32(size))
		for i := range data {
			data[i] = byte(i)
		}
		want, err := proto.Marshal(&Hunk{Data: bytes.Clone(data)})
		if err != nil {
			t.Fatal(err)
		}

		out, err := Codec{}.Marshal(newOwnedHunk(b))
		if err != nil {
			t.Fatal(err)
		}
		if got := out.Materialize(); !bytes.Equal(got, want) {
			t.Fatalf("size %d: got %x..., want %x...", size, got[:4], want[:4])
		}
		r := out.Reader()
		out.Free()
		if b.Bytes() == nil {
			t.Fatalf("size %d: buffer released while referenced", size)
		}
		r.Close()
		if b.Bytes() != nil {
			t.Fatalf("size %d: buffer not released", size)
		}
	}
}

func TestCodecUnmarshalHunk(t *testing.T) {
	data := make([]byte, 5000)
	for i := range data {
		data[i] = byte(i * 7)
	}
	wire, err := proto.Marshal(&Hunk{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	// Split the message into buffers at every interesting boundary.
	for _, cut := range []int{1, 2, 3, 100, len(wire) - 1} {
		in := mem.BufferSlice{mem.SliceBuffer(wire[:cut]), mem.SliceBuffer(wire[cut:])}
		var h Hunk
		if err := (Codec{}).Unmarshal(in, &h); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(h.Data, data) {
			t.Fatalf("cut %d: data mismatch", cut)
		}
		if cap(h.Data) != buf.Size {
			t.Fatalf("cut %d: data not pooled, cap %d", cut, cap(h.Data))
		}
	}

	var empty Hunk
	if err := (Codec{}).Unmarshal(nil, &empty); err != nil || empty.Data != nil {
		t.Fatalf("empty: %v %v", err, empty.Data)
	}
}

func TestCodecUnmarshalHunkFallback(t *testing.T) {
	wire, err := proto.Marshal(&Hunk{Data: []byte("abc")})
	if err != nil {
		t.Fatal(err)
	}
	// An unknown field after data is left to the proto codec.
	wire = protowire.AppendTag(wire, 9, protowire.VarintType)
	wire = protowire.AppendVarint(wire, 42)
	var h Hunk
	if err := (Codec{}).Unmarshal(mem.BufferSlice{mem.SliceBuffer(wire)}, &h); err != nil {
		t.Fatal(err)
	}
	if string(h.Data) != "abc" {
		t.Fatalf("got %q", h.Data)
	}

	if err := (Codec{}).Unmarshal(mem.BufferSlice{mem.SliceBuffer([]byte{0x0a, 0x05, 'x'})}, &h); err == nil {
		t.Fatal("truncated hunk decoded")
	}
}
