package errors_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	. "github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/log"
)

func TestError(t *testing.T) {
	err := New("TestError")
	if v := err.Error(); !strings.Contains(v, "TestError") {
		t.Error("error: ", v)
	}

	err = New("TestError2").Base(io.EOF)
	if v := err.Error(); !strings.Contains(v, "EOF") {
		t.Error("error: ", v)
	}

	err = New("TestError3").Base(io.EOF)
	err = New("TestError4").Base(err)
	if v := err.Error(); !strings.Contains(v, "EOF") {
		t.Error("error: ", v)
	}
}

func TestErrorMessage(t *testing.T) {
	data := []struct {
		err error
		msg string
	}{
		{
			err: New("a").Base(New("b")),
			msg: "common/errors_test: a > common/errors_test: b",
		},
	}

	for _, d := range data {
		if diff := cmp.Diff(d.msg, d.err.Error()); diff != "" {
			t.Error(diff)
		}
	}
}

type recordingHandler struct {
	level log.Severity
	msgs  []string
}

func (h *recordingHandler) Severity() log.Severity { return h.level }

func (h *recordingHandler) Handle(msg log.Message) {
	h.msgs = append(h.msgs, msg.String())
}

func TestLogSkipsDisabledSeverities(t *testing.T) {
	h := &recordingHandler{level: log.Severity_Warning}
	log.RegisterHandler(h)
	defer log.RegisterHandler(log.NewLogger(log.CreateStdoutLogWriter()))

	LogInfo(context.Background(), "dropped")
	LogDebugInner(context.Background(), io.EOF, "dropped")
	LogWarning(context.Background(), "kept")
	LogWarningInner(context.Background(), New("inner"), "kept with inner")

	want := []string{
		"[Warning] common/errors_test: kept",
		"[Warning] common/errors_test: kept with inner > common/errors_test: inner",
	}
	if diff := cmp.Diff(want, h.msgs); diff != "" {
		t.Error(diff)
	}
}
