package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type FeishuNotifier struct {
	webhookURL string
}

func NewFeishuNotifier(url string) *FeishuNotifier {
	return &FeishuNotifier{webhookURL: url}
}

func (f *FeishuNotifier) Send(ctx context.Context, title, message, confirmCode string) error {
	payload := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"header": map[string]interface{}{
				"title": map[string]interface{}{
					"tag":     "plain_text",
					"content": title,
				},
			},
			"elements": []interface{}{
				map[string]interface{}{
					"tag": "div",
					"text": map[string]interface{}{
						"tag":     "lark_md",
						"content": fmt.Sprintf("%s\n\n**Confirm Code:** %s", message, confirmCode),
					},
				},
			},
		},
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", f.webhookURL, bytes.NewBuffer(body))
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
		return fmt.Errorf("feishu API error: status %d", resp.StatusCode)
	}

	return nil
}
