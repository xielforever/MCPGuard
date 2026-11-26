package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/xielforever/MCPGuard/internal/runtimeconfig"
)

func probe(ctx context.Context, kind string) error {
	var path string
	switch kind {
	case "live":
		path = "/healthz"
	case "ready":
		path = "/readyz"
	default:
		return errors.New("usage: mcpguard probe live|ready")
	}
	address, err := runtimeconfig.ListenAddress()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		return errors.New("invalid gateway probe address")
	}
	client := &http.Client{
		Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return errors.New("gateway probe could not reach the loopback endpoint")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("gateway probe failed")
	}
	return nil
}
