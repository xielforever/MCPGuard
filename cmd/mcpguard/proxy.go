package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// runProxy exposes one approved TCP destination. By default it binds only to
// loopback; the explicit --allow-non-loopback flag is used only for the
// cluster-internal gateway ingress sidecar, which is restricted by a
// NetworkPolicy to the console frontend pods.
func runProxy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:18083", "TCP listen address")
	target := fs.String("target", "", "approved upstream TCP address")
	allowNonLoopback := fs.Bool("allow-non-loopback", false, "allow a cluster-only ingress listener")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := validateProxyListen(*listen, *allowNonLoopback); err != nil {
		return err
	}
	if strings.TrimSpace(*target) == "" {
		return errors.New("proxy target is required")
	}
	if _, _, err := net.SplitHostPort(*target); err != nil {
		return errors.New("proxy target must be a host and port")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return errors.New("proxy could not listen")
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

func validateProxyListen(address string, allowNonLoopback bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return errors.New("proxy listen address must be a host and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !allowNonLoopback) {
		return errors.New("proxy listen address must be an explicit loopback host and port unless --allow-non-loopback is set")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return errors.New("proxy listen address must use a valid TCP port")
	}
	return nil
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