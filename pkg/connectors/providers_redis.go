package connectors

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxRedisValue = 16 << 10

func (p *Providers) getKey(ctx context.Context, def Definition, key string) (any, error) {
	config := def.Config
	clientTLS, err := p.tlsConfig(config.Host, config)
	if err != nil {
		return nil, err
	}
	raw, err := p.policy.DialContext(ctx, "tcp", net.JoinHostPort(config.Host, strconv.Itoa(config.Port)))
	if err != nil {
		if errors.Is(err, ErrDenied) {
			return nil, ErrDenied
		}
		return nil, ErrUnavailable
	}
	defer raw.Close()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if raw.SetDeadline(deadline) != nil {
		return nil, ErrUnavailable
	}
	conn := raw
	if clientTLS != nil {
		secure := tls.Client(raw, clientTLS)
		if secure.HandshakeContext(ctx) != nil {
			return nil, ErrUnavailable
		}
		conn = secure
	}
	reader := bufio.NewReaderSize(conn, 1024)
	if writeRedisCommand(conn, "AUTH", def.Credentials.Username, def.Credentials.Secret) != nil {
		return nil, ErrUnavailable
	}
	ack, err := redisLine(reader)
	if err != nil {
		return nil, err
	}
	if ack != "+OK" {
		return nil, ErrUnavailable
	}
	if writeRedisCommand(conn, "GET", key) != nil {
		return nil, ErrUnavailable
	}
	value, err := redisBulk(reader)
	if err != nil {
		return nil, err
	}
	out := GetKeyResult{Key: key, Found: value != nil}
	if out.Found {
		text := string(value)
		out.Encoding = "utf8"
		if !utf8.Valid(value) {
			text = base64.StdEncoding.EncodeToString(value)
			out.Encoding = "base64"
		}
		out.Value = &text
	}
	return out, nil
}

func writeRedisCommand(w io.Writer, args ...string) error {
	var encoded bytes.Buffer
	fmt.Fprintf(&encoded, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&encoded, "$%d\r\n%s\r\n", len(arg), arg)
	}
	_, err := io.Copy(w, &encoded)
	return err
}

func redisLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadSlice('\n')
	if err != nil || len(line) < 3 || !bytes.HasSuffix(line, []byte("\r\n")) {
		return "", ErrUnavailable
	}
	result := string(line[:len(line)-2])
	if strings.HasPrefix(result, "-WRONGPASS ") || strings.HasPrefix(result, "-NOAUTH ") ||
		strings.HasPrefix(result, "-NOPERM ") {
		return "", ErrDenied
	}
	return result, nil
}

func redisBulk(r *bufio.Reader) ([]byte, error) {
	line, err := redisLine(r)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "$") {
		return nil, ErrUnavailable
	}
	count, err := strconv.Atoi(line[1:])
	if err != nil || count < -1 || count > maxRedisValue {
		return nil, ErrUnavailable
	}
	if count == -1 {
		return nil, nil
	}
	value := make([]byte, count+2)
	if _, err := io.ReadFull(r, value); err != nil || string(value[count:]) != "\r\n" {
		return nil, ErrUnavailable
	}
	return value[:count], nil
}
