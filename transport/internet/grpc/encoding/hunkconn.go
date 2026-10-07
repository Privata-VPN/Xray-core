package encoding

import (
	"context"
	"io"

	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/net/cnc"
	"github.com/xtls/xray-core/common/signal/done"
)

type HunkConn interface {
	Context() context.Context
	Send(*Hunk) error
	Recv() (*Hunk, error)
	SendMsg(m interface{}) error
	RecvMsg(m interface{}) error
}

type StreamCloser interface {
	CloseSend() error
}

type HunkReaderWriter struct {
	hc     HunkConn
	cancel context.CancelFunc
	done   *done.Instance

	buf   []byte
	index int

	// ownedSends hands buffers to gRPC instead of copying them; the
	// stream's codec must be Codec.
	ownedSends bool
}

func NewHunkReadWriter(hc HunkConn, cancel context.CancelFunc) *HunkReaderWriter {
	return &HunkReaderWriter{hc: hc, cancel: cancel, done: done.New()}
}

func NewHunkConn(hc HunkConn, cancel context.CancelFunc, trustedXForwardedFor []string) net.Conn {
	return newHunkConn(hc, cancel, trustedXForwardedFor, false)
}

// NewServerHunkConn is NewHunkConn for streams of a server using Codec.
func NewServerHunkConn(hc HunkConn, cancel context.CancelFunc, trustedXForwardedFor []string) net.Conn {
	return newHunkConn(hc, cancel, trustedXForwardedFor, true)
}

func newHunkConn(hc HunkConn, cancel context.CancelFunc, trustedXForwardedFor []string, ownedSends bool) net.Conn {
	rAddr := remoteAddrFromContext(hc.Context(), trustedXForwardedFor)
	lAddr := localAddrFromContext(hc.Context())
	wrc := NewHunkReadWriter(hc, cancel)
	wrc.ownedSends = ownedSends
	return cnc.NewConnection(
		cnc.ConnectionInput(wrc),
		cnc.ConnectionOutput(wrc),
		cnc.ConnectionOnClose(wrc),
		cnc.ConnectionRemoteAddr(rAddr),
		cnc.ConnectionLocalAddr(lAddr),
	)
}

func (h *HunkReaderWriter) forceFetch() error {
	hunk, err := h.hc.Recv()
	if err != nil {
		if err == io.EOF {
			return err
		}

		return errors.New("failed to fetch hunk from gRPC tunnel").Base(err)
	}

	h.buf = hunk.Data
	h.index = 0

	return nil
}

func (h *HunkReaderWriter) Read(buf []byte) (int, error) {
	if h.done.Done() {
		return 0, io.EOF
	}

	if h.index >= len(h.buf) {
		if err := h.forceFetch(); err != nil {
			return 0, err
		}
	}
	n := copy(buf, h.buf[h.index:])
	h.index += n

	return n, nil
}

func (h *HunkReaderWriter) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if h.done.Done() {
		return nil, io.EOF
	}
	if h.index >= len(h.buf) {
		if err := h.forceFetch(); err != nil {
			return nil, err
		}
	}

	if cap(h.buf) >= buf.Size {
		b := h.buf
		h.index = len(h.buf)
		return buf.MultiBuffer{buf.NewExisted(b)}, nil
	}

	b := buf.New()
	_, err := b.ReadFrom(h)
	if err != nil {
		return nil, err
	}
	return buf.MultiBuffer{b}, nil
}

func (h *HunkReaderWriter) Write(buf []byte) (int, error) {
	if h.done.Done() {
		return 0, io.ErrClosedPipe
	}

	err := h.hc.Send(&Hunk{Data: buf[:]})
	if err != nil {
		return 0, errors.New("failed to send data over gRPC tunnel").Base(err)
	}
	return len(buf), nil
}

// WriteMultiBuffer implements buf.Writer, sending each buffer as one hunk.
func (h *HunkReaderWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if h.done.Done() {
		buf.ReleaseMulti(mb)
		return io.ErrClosedPipe
	}
	for i, b := range mb {
		if b.IsEmpty() {
			b.Release()
			continue
		}
		var err error
		if h.ownedSends {
			err = h.hc.SendMsg(newOwnedHunk(b))
		} else {
			err = h.hc.Send(&Hunk{Data: b.Bytes()})
			b.Release()
		}
		if err != nil {
			buf.ReleaseMulti(mb[i+1:])
			return errors.New("failed to send data over gRPC tunnel").Base(err)
		}
	}
	return nil
}

func (h *HunkReaderWriter) Close() error {
	if h.cancel != nil {
		h.cancel()
	}
	if sc, match := h.hc.(StreamCloser); match {
		return sc.CloseSend()
	}

	return h.done.Close()
}
