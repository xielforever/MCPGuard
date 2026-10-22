package wireids

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type response struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *response) Header() http.Header { return r.header }
func (r *response) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *response) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.body.Write(p)
}

// HTTP protects SDK JSON responses. GET SSE is passed through unchanged; this
// read slice advertises no server-initiated calls or dynamic notifications.
func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, MaxFrameBytes+1))
		_ = r.Body.Close()
		if e != nil {
			http.Error(w, "request read failed", 400)
			return
		}
		if len(b) > MaxFrameBytes {
			http.Error(w, "request too large", 413)
			return
		}
		b, e = Incoming(b)
		if e != nil {
			code := -32600
			var syntax *json.SyntaxError
			if errors.As(e, &syntax) {
				code = -32700
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":null,"error":{"code":`+strconv.Itoa(code)+`,"message":"Invalid JSON-RPC envelope or request ID"}}`)
			return
		}
		r = r.Clone(r.Context())
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		out := &response{header: make(http.Header)}
		next.ServeHTTP(out, r)
		b = out.body.Bytes()
		if len(b) > 0 && strings.HasPrefix(out.header.Get("Content-Type"), "application/json") {
			b, e = Outgoing(b)
			if e != nil {
				http.Error(w, "response ID mapping failed", 500)
				return
			}
		}
		for k, vs := range out.header {
			w.Header()[k] = vs
		}
		w.Header().Del("Content-Length")
		if out.status == 0 {
			out.status = 200
		}
		w.WriteHeader(out.status)
		_, _ = w.Write(b)
	})
}
