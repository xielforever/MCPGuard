package proxy

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/xielforever/MCPGuard/internal/auth"
	"github.com/xielforever/MCPGuard/internal/rules"
)

type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

type BackendSender interface {
	Send(req []byte) ([]byte, error)
}

type MCPProxy struct {
	ruleEngine  *rules.Engine
	authService *auth.ConfirmService
	backend     BackendSender
}

func NewMCPProxy(engine *rules.Engine, authSvc *auth.ConfirmService, backend BackendSender) *MCPProxy {
	return &MCPProxy{
		ruleEngine:  engine,
		authService: authSvc,
		backend:     backend,
	}
}

func (p *MCPProxy) HandleRequest(reqBytes []byte) ([]byte, error) {
	var req MCPRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return nil, err
	}

	// Only evaluate rules if it's a tool execution or dangerous method
	if req.Method == "tools/call" {
		action, matchedRule := p.ruleEngine.Evaluate(req.Method, string(req.Params))
		log.Printf("Evaluated request: %s, Action: %s, Rule: %v", req.Method, action, matchedRule)

		switch action {
		case rules.ActionPass:
			return p.ForwardToBackend(reqBytes)
		case rules.ActionConfirm:
			// Trigger Auth Flow
			confirm := p.authService.CreateConfirmation(req.ID, "tools/call", string(req.Params))
			return p.createPendingResponse(req.ID, confirm.ID, confirm.Code), nil
		case rules.ActionBlock:
			return p.createErrorResponse(req.ID, -32000, "Operation blocked by security policy"), nil
		}
	}

	// For all other methods or default pass
	return p.ForwardToBackend(reqBytes)
}

func (p *MCPProxy) ForwardToBackend(reqBytes []byte) ([]byte, error) {
	if p.backend == nil {
		return nil, fmt.Errorf("backend not initialized")
	}
	return p.backend.Send(reqBytes)
}

func (p *MCPProxy) createPendingResponse(id interface{}, confirmID, code string) []byte {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    -32001,
			"message": "Confirmation required",
			"data": map[string]interface{}{
				"confirm_id": confirmID,
				"message":    fmt.Sprintf("Please provide confirmation code: %s", code),
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func (p *MCPProxy) createErrorResponse(id interface{}, code int, message string) []byte {
	resp := MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	b, _ := json.Marshal(resp)
	return b
}
