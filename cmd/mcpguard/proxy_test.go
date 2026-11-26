package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func freeTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestProxyForwardsLoopbackTraffic(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	address := freeTCPAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = runProxy(ctx, []string{"--listen", address, "--target", upstream.Addr().String()})
	}()
	<-ready

	var conn net.Conn
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		conn, err = net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("proxy did not accept a loopback connection")
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("hello proxy")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("hello proxy"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello proxy" {
		t.Fatal("proxy changed the forwarded payload")
	}
}

func TestProxyListenerValidation(t *testing.T) {
	if err := validateProxyListen("127.0.0.1:18083", false); err != nil {
		t.Fatal("loopback listener rejected")
	}
	if err := validateProxyListen("0.0.0.0:18080", false); err == nil {
		t.Fatal("non-loopback listener accepted without the flag")
	}
	if err := validateProxyListen("0.0.0.0:18080", true); err != nil {
		t.Fatal("explicit non-loopback listener rejected")
	}
	if err := validateProxyListen("127.0.0.1:0", false); err == nil {
		t.Fatal("port zero accepted")
	}
}

func TestProxyRejectsNonLoopbackAndMissingTarget(t *testing.T) {
	ctx := context.Background()
	if err := runProxy(ctx, []string{"--listen", "0.0.0.0:18083", "--target", "127.0.0.1:8080"}); err == nil {
		t.Fatal("proxy accepted a non-loopback listen address")
	}
	if err := runProxy(ctx, []string{"--listen", "127.0.0.1:18083"}); err == nil {
		t.Fatal("proxy accepted an empty target")
	}
	if err := runProxy(ctx, []string{"--listen", "127.0.0.1:18083", "--target", "not-an-address"}); err == nil {
		t.Fatal("proxy accepted a target without a port")
	}
}