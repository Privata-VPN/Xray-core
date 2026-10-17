// Package errors is a drop-in replacement for Golang lib 'errors'.
package errors // import "github.com/xtls/xray-core/common/errors"

import (
	"context"
	"runtime"
	"strings"

	c "github.com/xtls/xray-core/common/ctx"
	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/serial"
)

const trim = len("github.com/xtls/xray-core/")

type hasInnerError interface {
	// Unwrap returns the underlying error of this one.
	Unwrap() error
}

// Error is an error object with underlying error.
type Error struct {
	prefix   []interface{}
	message  []interface{}
	callerPC uintptr // return PC into the function that created the error
	inner    error
}

// callerPackage returns the package, relative to this module, of the
// function at return PC pc. It is resolved only when an error is formatted,
// since looking up the caller is costly and most errors are never printed.
func callerPackage(pc uintptr) string {
	if pc == 0 {
		return ""
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	details := frame.Function
	if len(details) >= trim {
		details = details[trim:]
	}
	i := strings.Index(details, ".")
	if i > 0 {
		details = details[:i]
	}
	return details
}

func callerPC(skip int) uintptr {
	var pc [1]uintptr
	runtime.Callers(skip+2, pc[:])
	return pc[0]
}

// Error implements error.Error().
func (err *Error) Error() string {
	builder := strings.Builder{}
	for _, prefix := range err.prefix {
		builder.WriteByte('[')
		builder.WriteString(serial.ToString(prefix))
		builder.WriteString("] ")
	}

	if caller := callerPackage(err.callerPC); len(caller) > 0 {
		builder.WriteString(caller)
		builder.WriteString(": ")
	}

	msg := serial.Concat(err.message...)
	builder.WriteString(msg)

	if err.inner != nil {
		builder.WriteString(" > ")
		builder.WriteString(err.inner.Error())
	}

	return builder.String()
}

// Unwrap implements hasInnerError.Unwrap()
func (err *Error) Unwrap() error {
	if err.inner == nil {
		return nil
	}
	return err.inner
}

func (err *Error) Base(e error) *Error {
	err.inner = e
	return err
}

// String returns the string representation of this error.
func (err *Error) String() string {
	return err.Error()
}

type ExportOptionHolder struct {
	SessionID uint32
}

type ExportOption func(*ExportOptionHolder)

// New returns a new error object with message formed from given arguments.
func New(msg ...interface{}) *Error {
	return &Error{
		message:  msg,
		callerPC: callerPC(1),
	}
}

func LogDebug(ctx context.Context, msg ...interface{}) {
	doLog(ctx, nil, log.Severity_Debug, msg...)
}

func LogDebugInner(ctx context.Context, inner error, msg ...interface{}) {
	doLog(ctx, inner, log.Severity_Debug, msg...)
}

func LogInfo(ctx context.Context, msg ...interface{}) {
	doLog(ctx, nil, log.Severity_Info, msg...)
}

func LogInfoInner(ctx context.Context, inner error, msg ...interface{}) {
	doLog(ctx, inner, log.Severity_Info, msg...)
}

func LogWarning(ctx context.Context, msg ...interface{}) {
	doLog(ctx, nil, log.Severity_Warning, msg...)
}

func LogWarningInner(ctx context.Context, inner error, msg ...interface{}) {
	doLog(ctx, inner, log.Severity_Warning, msg...)
}

func LogError(ctx context.Context, msg ...interface{}) {
	doLog(ctx, nil, log.Severity_Error, msg...)
}

func LogErrorInner(ctx context.Context, inner error, msg ...interface{}) {
	doLog(ctx, inner, log.Severity_Error, msg...)
}

func doLog(ctx context.Context, inner error, severity log.Severity, msg ...interface{}) {
	if log.GetSeverity() < severity {
		return
	}
	err := &Error{
		message:  msg,
		callerPC: callerPC(2),
		inner:    inner,
	}
	if ctx != nil && ctx != context.Background() {
		id := uint32(c.IDFromContext(ctx))
		if id > 0 {
			err.prefix = append(err.prefix, id)
		}
	}
	log.Record(&log.GeneralMessage{
		Severity: severity,
		Content:  err,
	})
}

// Cause returns the root cause of this error.
func Cause(err error) error {
	if err == nil {
		return nil
	}
L:
	for {
		switch inner := err.(type) {
		case hasInnerError:
			if inner.Unwrap() == nil {
				break L
			}
			err = inner.Unwrap()
		default:
			break L
		}
	}
	return err
}
