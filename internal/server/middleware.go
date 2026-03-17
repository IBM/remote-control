package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

var middlewareCh = alog.UseChannel("ENDPOINT")

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker interface for WebSocket support
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("responseWriter does not implement http.Hijacker")
	}
	return hijacker.Hijack()
}

// loggingMiddleware logs each request method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		middlewareCh.Log(alog.DEBUG, "%s %s %d %s", r.Method, r.URL.Path, rw.status, time.Since(start))
	})
}

// recoveryMiddleware catches panics and returns a 500 response.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				middlewareCh.Log(alog.ERROR, "PANIC: %v\n%s", rec, debug.Stack())
				writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
