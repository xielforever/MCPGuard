package wireids

import (
	"bufio"
	"bytes"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"os"
	"sync"
)

type frameReader struct {
	raw     io.ReadCloser
	scanner *bufio.Scanner
	pending []byte
}

func (r *frameReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 {
		if !r.scanner.Scan() {
			if e := r.scanner.Err(); e != nil {
				return 0, e
			}
			return 0, io.EOF
		}
		b, e := Incoming(r.scanner.Bytes())
		if e != nil {
			return 0, e
		}
		r.pending = append(b, '\n')
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}
func (r *frameReader) Close() error { return r.raw.Close() }

type frameWriter struct {
	raw     io.WriteCloser
	mu      sync.Mutex
	pending []byte
}

func (w *frameWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	if len(w.pending) > 128<<10 {
		return 0, errors.New("outgoing frame exceeds limit")
	}
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			break
		}
		b, e := Outgoing(w.pending[:idx])
		if e != nil {
			return 0, e
		}
		b = append(b, '\n')
		for len(b) > 0 {
			n, e := w.raw.Write(b)
			if e != nil {
				return 0, e
			}
			if n == 0 {
				return 0, io.ErrShortWrite
			}
			b = b[n:]
		}
		w.pending = w.pending[idx+1:]
	}
	return len(p), nil
}
func (w *frameWriter) Close() error { return w.raw.Close() }
func Stdio() mcp.Transport {
	r := &frameReader{raw: os.Stdin, scanner: bufio.NewScanner(os.Stdin)}
	r.scanner.Buffer(make([]byte, 4096), MaxFrameBytes+1)
	return &mcp.IOTransport{Reader: r, Writer: &frameWriter{raw: os.Stdout}, MaxLineLength: 70 << 10}
}
