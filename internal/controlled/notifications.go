package controlled

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Telegram notifications never carry approval credentials or business payloads.
// The browser decision endpoint remains independently authenticated. A leased
// outbox is at-least-once: a crash after delivery can produce a duplicate notice.
type NotificationConfig struct{ OrgID, BotToken, ChatID, TestOrigin string }
type Notifier struct {
	service *Service
	cfg     NotificationConfig
	client  *http.Client
}

func OpenNotifier(s *Service, path string) (*Notifier, error) {
	n := &Notifier{service: s, client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192, ResponseHeaderTimeout: 2 * time.Second}}}
	if path == "" {
		return n, nil
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, errors.New("notification configuration unavailable")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(&n.cfg); e != nil {
		return nil, errors.New("invalid notification configuration")
	}
	if n.cfg.BotToken == "" || n.cfg.ChatID == "" || n.cfg.OrgID == "" || strings.ContainsAny(n.cfg.BotToken, "/ ?#\r\n") {
		return nil, errors.New("Telegram requires a bot token and chat ID")
	}
	if n.cfg.TestOrigin != "" {
		u, e := url.Parse(n.cfg.TestOrigin)
		if e != nil || u.Scheme != "http" || !loopbackAddress(u.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, errors.New("notification test receiver must be a loopback origin")
		}
	}
	return n, nil
}
func (n *Notifier) Configured() bool { return n != nil && n.cfg.BotToken != "" }
func (n *Notifier) send(ctx context.Context, id string) error {
	base := "https://api.telegram.org"
	if n.cfg.TestOrigin != "" {
		base = n.cfg.TestOrigin
	}
	body, _ := json.Marshal(map[string]any{"chat_id": n.cfg.ChatID, "text": "MCPGuard: a controlled action is awaiting human review. Action ID: " + id + "\nOpen the local console on the MCPGuard computer: " + n.service.Origin + "/web/#action=" + id, "link_preview_options": map[string]bool{"is_disabled": true}})
	req, e := http.NewRequestWithContext(ctx, "POST", base+"/bot"+n.cfg.BotToken+"/sendMessage", bytes.NewReader(body))
	if e != nil {
		return errors.New("notification request rejected")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := n.client.Do(req)
	if e != nil {
		return errors.New("notification unavailable")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if e != nil || len(raw) > 8192 || resp.StatusCode != 200 {
		return errors.New("notification rejected")
	}
	var ack struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(raw, &ack) != nil || !ack.OK {
		return errors.New("notification acknowledgement rejected")
	}
	return nil
}
func (n *Notifier) Deliver(ctx context.Context) error {
	if !n.Configured() {
		return nil
	}
	_, e := n.service.Store.Pool.Exec(ctx, `UPDATE mcpguard.notification_outbox o SET state=CASE WHEN o.attempts>=5 THEN 'FAILED' ELSE 'SUPERSEDED' END FROM mcpguard.managed_actions a WHERE (a.org_id,a.id)=(o.org_id,o.action_id) AND o.org_id=$1 AND o.state IN ('PENDING','SENDING') AND ((o.state='SENDING' AND o.lease_until<clock_timestamp() AND o.attempts>=5) OR (a.state<>'PENDING_APPROVAL' OR a.expires_at<=clock_timestamp()))`, n.cfg.OrgID)
	if e != nil {
		return e
	}
	// No execution work is performed by this worker, only safe notifications.
	var org, id, actionID string
	var attempts int
	e = n.service.Store.Pool.QueryRow(ctx, `WITH candidate AS (SELECT o.org_id,o.id FROM mcpguard.notification_outbox o JOIN mcpguard.managed_actions a ON (a.org_id,a.id)=(o.org_id,o.action_id) WHERE o.org_id=$1 AND ((o.state='PENDING' AND o.next_at<=clock_timestamp()) OR (o.state='SENDING' AND o.lease_until<clock_timestamp())) AND o.attempts<5 AND a.state='PENDING_APPROVAL' AND a.expires_at>clock_timestamp() ORDER BY o.next_at FOR UPDATE OF o SKIP LOCKED LIMIT 1) UPDATE mcpguard.notification_outbox o SET state='SENDING',attempts=attempts+1,lease_until=clock_timestamp()+interval '15 seconds' FROM candidate c WHERE (o.org_id,o.id)=(c.org_id,c.id) RETURNING o.org_id::text,o.id::text,o.action_id::text,o.attempts`, n.cfg.OrgID).Scan(&org, &id, &actionID, &attempts)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	}
	sendCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	delivery := n.send(sendCtx, actionID)
	cancel()
	state := "SENT"
	if delivery != nil {
		state = "PENDING"
		if attempts >= 5 {
			state = "FAILED"
		}
	}
	finish, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	_, e = n.service.Store.Pool.Exec(finish, `UPDATE mcpguard.notification_outbox SET state=$3,next_at=clock_timestamp()+make_interval(secs=>$4),lease_until=NULL WHERE org_id=$1 AND id=$2 AND state='SENDING' AND attempts=$5`, org, id, state, 1<<attempts, attempts)
	return e
}
