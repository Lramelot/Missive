package main

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/rs/zerolog"
)

const maxPanicFrames = 8

func panicSummary(p any) string {
	if err, ok := p.(runtime.Error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%T", p)
}

func panicStack() string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	parts := make([]string, 0, maxPanicFrames)
	for len(parts) < maxPanicFrames {
		frame, more := frames.Next()
		if frame.Function != "" && !strings.HasPrefix(frame.Function, "runtime.") {
			name := frame.Function
			if i := strings.LastIndex(name, "/"); i >= 0 {
				name = name[i+1:]
			}
			parts = append(parts, name+":"+strconv.Itoa(frame.Line))
		}
		if !more {
			break
		}
	}
	return strings.Join(parts, " < ")
}

func logRecovered(log zerolog.Logger, where string, p any) {
	log.Error().
		Str("where", where).
		Str("panic", panicSummary(p)).
		Str("stack", panicStack()).
		Msg("Recovered from panic")
}

func goSafe(log zerolog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				logRecovered(log, name, p)
			}
		}()
		fn()
	}()
}

func (b *MetaBackend) recoverHandler(where string) {
	if p := recover(); p != nil {
		logRecovered(b.log, where, p)
	}
}
