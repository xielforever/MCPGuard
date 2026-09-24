package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type TelegramNotifier struct {
	token  string
	chatID string
}

func NewTelegramNotifier(token, chatID string) *TelegramNotifier {
	return &TelegramNotifier{token: token, chatID: chatID}
}

func (t *TelegramNotifier) Send(ctx context.Context, title, message, confirmCode string) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)
	
	text := fmt.Sprintf("<b>%s</b>\n%s\n\n<b>Confirm Code:</b> <code>%s</code>", title, message, confirmCode)
	
	payload := map[string]interface{}{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "HTML",
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram API error: status %d", resp.StatusCode)
	}

	return nil
}
