package auth

import (
	"crypto/rand"
	"math/big"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xielforever/MCPGuard/internal/config"
)

const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusExpired   = "expired"
	StatusRejected  = "rejected"
)

type Confirmation struct {
	ID        string
	Code      string
	RequestID interface{}
	ToolName  string
	Params    string
	Status    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type ConfirmService struct {
	config        config.AuthConfig
	confirmations sync.Map
}

func NewConfirmService(cfg config.AuthConfig) *ConfirmService {
	svc := &ConfirmService{
		config: cfg,
	}
	go svc.CleanupExpired()
	return svc
}

func (s *ConfirmService) CreateConfirmation(reqID interface{}, toolName, params string) *Confirmation {
	code := s.generateCode(s.config.CodeLength)
	id := uuid.New().String()
	
	confirm := &Confirmation{
		ID:        id,
		Code:      code,
		RequestID: reqID,
		ToolName:  toolName,
		Params:    params,
		Status:    StatusPending,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(s.config.CodeExpiry),
	}
	
	s.confirmations.Store(id, confirm)
	return confirm
}

func (s *ConfirmService) VerifyCode(id, code string) bool {
	val, ok := s.confirmations.Load(id)
	if !ok {
		return false
	}
	
	confirm := val.(*Confirmation)
	if confirm.Status != StatusPending || time.Now().After(confirm.ExpiresAt) {
		confirm.Status = StatusExpired
		return false
	}
	
	if confirm.Code == code {
		confirm.Status = StatusConfirmed
		return true
	}
	
	return false
}

func (s *ConfirmService) CleanupExpired() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		now := time.Now()
		s.confirmations.Range(func(key, value interface{}) bool {
			confirm := value.(*Confirmation)
			if confirm.Status == StatusPending && now.After(confirm.ExpiresAt) {
				confirm.Status = StatusExpired
			}
			// Clean up old records (e.g. older than 1 hour)
			if now.Sub(confirm.ExpiresAt) > 1*time.Hour {
				s.confirmations.Delete(key)
			}
			return true
		})
	}
}

func (s *ConfirmService) generateCode(length int) string {
	const charset = "0123456789"
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[n.Int64()]
	}
	return string(b)
}
