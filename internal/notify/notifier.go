package notify

import (
	"context"
	"fmt"
)

type Notifier interface {
	Send(ctx context.Context, title, message, confirmCode string) error
}

type NotifyManager struct {
	notifiers map[string]Notifier
}

func NewNotifyManager() *NotifyManager {
	return &NotifyManager{
		notifiers: make(map[string]Notifier),
	}
}

func (m *NotifyManager) RegisterNotifier(name string, n Notifier) {
	m.notifiers[name] = n
}

func (m *NotifyManager) SendNotification(ctx context.Context, provider, title, message, confirmCode string) error {
	if n, ok := m.notifiers[provider]; ok {
		return n.Send(ctx, title, message, confirmCode)
	}
	return fmt.Errorf("notifier %s not found", provider)
}
