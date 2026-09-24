package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"

	"github.com/xielforever/MCPGuard/internal/auth"
	"github.com/xielforever/MCPGuard/internal/config"
	"github.com/xielforever/MCPGuard/internal/rules"
	"github.com/xielforever/MCPGuard/internal/store"
)

type StdioProxy struct {
	config      config.ProxyConfig
	ruleEngine  *rules.Engine
	authService *auth.ConfirmService
	store       *store.Store
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	proxy       *MCPProxy
}

func NewStdioProxy(cfg config.ProxyConfig, engine *rules.Engine, auth *auth.ConfirmService, s *store.Store) *StdioProxy {
	return &StdioProxy{
		config:      cfg,
		ruleEngine:  engine,
		authService: auth,
		store:       s,
	}
}

func (s *StdioProxy) Start() error {
	s.cmd = exec.Command(s.config.BackendCommand, s.config.BackendArgs...)

	var err error
	s.stdin, err = s.cmd.StdinPipe()
	if err != nil {
		return err
	}

	s.stdout, err = s.cmd.StdoutPipe()
	if err != nil {
		return err
	}

	s.cmd.Stderr = os.Stderr

	if err := s.cmd.Start(); err != nil {
		return err
	}

	s.proxy = NewMCPProxy(s.ruleEngine, s.authService, s)

	go s.readClientLoop()

	return nil
}

func (s *StdioProxy) readClientLoop() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		reqBytes := scanner.Bytes()
		respBytes, err := s.proxy.HandleRequest(reqBytes)
		if err != nil {
			log.Printf("Error handling request: %v", err)
			continue
		}
		if respBytes != nil {
			fmt.Println(string(respBytes))
		}
	}
}

func (s *StdioProxy) Send(req []byte) ([]byte, error) {
	_, err := s.stdin.Write(append(req, '\n'))
	if err != nil {
		return nil, err
	}

	// In a real stdio proxy, reading the exact response requires parsing JSON-RPC
	// For simplicity in this structure, we read one line as response
	// In production, this needs a proper message framed reader
	reader := bufio.NewReader(s.stdout)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	return line, nil
}

func (s *StdioProxy) Stop() error {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}
