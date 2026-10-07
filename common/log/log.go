package log // import "github.com/xtls/xray-core/common/log"

import (
	"sync"
	"sync/atomic"

	"github.com/xtls/xray-core/common/serial"
)

// Message is the interface for all log messages.
type Message interface {
	String() string
}

// Handler is the interface for log handler.
type Handler interface {
	Handle(msg Message)
}

// GeneralMessage is a general log message that can contain all kind of content.
type GeneralMessage struct {
	Severity Severity
	Content  interface{}
}

// String implements Message.
func (m *GeneralMessage) String() string {
	return serial.Concat("[", m.Severity, "] ", m.Content)
}

// Record writes a message into log stream.
func Record(msg Message) {
	logHandler.Handle(msg)
}

// SeverityLimiter is implemented by handlers that drop every GeneralMessage
// less severe than LogLevel.
type SeverityLimiter interface {
	LogLevel() Severity
}

// logLevel is the LogLevel of the registered handler, or Severity_Debug if
// it keeps every GeneralMessage.
var logLevel atomic.Int32

// Enabled reports whether the registered handler may keep a GeneralMessage
// of severity s, so that callers can skip building messages it would drop.
func Enabled(s Severity) bool {
	return int32(s) <= logLevel.Load()
}

var logHandler syncHandler

// RegisterHandler registers a new handler as current log handler. Previous registered handler will be discarded.
func RegisterHandler(handler Handler) {
	if handler == nil {
		panic("Log handler is nil")
	}
	logHandler.Set(handler)
}

type syncHandler struct {
	sync.RWMutex
	Handler
}

func (h *syncHandler) Handle(msg Message) {
	h.RLock()
	defer h.RUnlock()

	if h.Handler != nil {
		h.Handler.Handle(msg)
	}
}

func (h *syncHandler) Set(handler Handler) {
	h.Lock()
	defer h.Unlock()

	h.Handler = handler
	level := Severity_Debug
	if l, ok := handler.(SeverityLimiter); ok {
		level = l.LogLevel()
	}
	logLevel.Store(int32(level))
}
