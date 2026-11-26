package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"
)

// runProxy exposes one approved TCP destination on a loopback-only address.
// The isolated CCE acceptance uses it for the cluster-internal Jenkins HTTP
// endpoint: the connector deliberately refuses plain HTTP to non-loopback
// destinations, while the proxy keeps that rule intact and never exposes the
// Jenkins port outside the pod network namespace.
func runProxy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:18083", "loopback TCP listen address")
	target := fs.String("target", "", "approved upstream TCP address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || host == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("proxy listen address must be an explicit loopback host and port")
	}
	if strings.TrimSpace(*target) == "" {
		return errors.New("proxy target is required")
	}
	if _, _, err := net.SplitHostPort(*target); err != nil {
		return errors.New("proxy target must be a host and port")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.New("proxy could not listen on the loopback address")
	}
	defer listener.Close()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	slog.Info("connector proxy ready", "listen", *listen, "target", *target)
	for {
		client, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("connector proxy stopped accepting connections")
		}
		go proxyConn(ctx, client, *target)
	}
}

func proxyConn(ctx context.Context, client net.Conn, target string) {
	defer client.Close()
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	upstream, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	copyOne := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyOne(upstream, client)
	go copyOne(client, upstream)
	<-done
	<-done
}