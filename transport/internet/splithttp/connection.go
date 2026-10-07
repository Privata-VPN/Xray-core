package splithttp

import (
	"io"
	"net"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

type splitConn struct {
	writer     io.WriteCloser
	reader     io.ReadCloser
	remoteAddr net.Addr
	localAddr  net.Addr
	onClose    func()
}

func (c *splitConn) Write(b []byte) (int, error) {
	return c.writer.Write(b)
}

// WriteMultiBuffer implements buf.Writer. Writers that take a MultiBuffer get
// it whole; others get one Write per buffer, as buf.SequentialWriter does.
func (c *splitConn) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if w, ok := c.writer.(buf.Writer); ok {
		return w.WriteMultiBuffer(mb)
	}
	mb, err := buf.WriteMultiBuffer(c.writer, mb)
	buf.ReleaseMulti(mb)
	return err
}

func (c *splitConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

// ReadMultiBuffer implements buf.Reader, passing through readers that
// implement it and reading one buffer at a time from others.
func (c *splitConn) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r, ok := c.reader.(buf.Reader); ok {
		return r.ReadMultiBuffer()
	}
	b, err := buf.ReadBuffer(c.reader)
	if b == nil {
		return nil, err
	}
	return buf.MultiBuffer{b}, err
}

func (c *splitConn) Close() error {
	if c.onClose != nil {
		c.onClose()
	}

	err := c.writer.Close()
	err2 := c.reader.Close()
	if err != nil {
		return err
	}

	if err2 != nil {
		return err
	}

	return nil
}

func (c *splitConn) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *splitConn) RemoteAddr() net.Addr {
	return c.remoteAddr
}

func (c *splitConn) SetDeadline(t time.Time) error {
	// TODO cannot do anything useful
	return nil
}

func (c *splitConn) SetReadDeadline(t time.Time) error {
	// TODO cannot do anything useful
	return nil
}

func (c *splitConn) SetWriteDeadline(t time.Time) error {
	// TODO cannot do anything useful
	return nil
}
