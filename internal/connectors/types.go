package connectors

import (
	"errors"
	"time"

	connector "github.com/xielforever/MCPGuard/pkg/connectors"
)

var (
	ErrInput       = connector.ErrInput
	ErrDenied      = connector.ErrDenied
	ErrConflict    = errors.New("connector configuration conflict")
	ErrNotFound    = errors.New("connector not found")
	ErrUnavailable = connector.ErrUnavailable
)

type CreateInput struct {
	Name         string                `json:"name"`
	Type         connector.Kind        `json:"type"`
	Config       connector.Config      `json:"config"`
	Credentials  connector.Credentials `json:"credentials"`
	PrincipalIDs []string              `json:"principalIds"`
}

type Summary struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       connector.Kind `json:"type"`
	Revision   int64          `json:"revision"`
	Enabled    bool           `json:"enabled"`
	TestStatus string         `json:"testStatus"`
	TestedAt   *time.Time     `json:"testedAt,omitempty"`
}

type Event struct {
	Type     string    `json:"type"`
	Revision int64     `json:"revision"`
	ActorID  string    `json:"actorId"`
	At       time.Time `json:"at"`
}
