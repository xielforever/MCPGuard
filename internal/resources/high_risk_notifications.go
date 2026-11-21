package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/xielforever/MCPGuard/internal/metadata"
)

const (
	HighRiskWeCom            = "WECOM"
	HighRiskFeishu           = "FEISHU"
	HighRiskApprovalRequired = "APPROVAL_REQUIRED"
	HighRiskApproved         = "APPROVED"
	HighRiskRejected         = "REJECTED"

	highRiskNotificationMaxAttempts = 5
	highRiskNotificationTimeout     = 3 * time.Second
)

// HighRiskNotifier delivers only to process-configured local fixture receivers.
// A notice identifies the action and its intended recipient, but carries no
// credentials or SQL data. The console authenticates code retrieval separately.
type HighRiskNotifier struct {
	store     *metadata.Store
	receivers map[string]string
	client    *http.Client
}

type highRiskNotification struct {
	NotificationID string    `json:"notificationId"`
	ActionID       string    `json:"actionId"`
	RecipientID    string    `json:"recipientId"`
	Channel        string    `json:"channel"`
	Event          string    `json:"event"`
	ExpiresAt      time.Time `json:"expiresAt"`
	ClaimPath      string    `json:"claimPath,omitempty"`
}

// HighRiskNotificationReceipt is the persisted delivery state for an action.
type HighRiskNotificationReceipt struct {
	NotificationID string                        `json:"notificationId"`
	ActionID       string                        `json:"actionId"`
	RecipientID    string                        `json:"recipientId"`
	Channel        string                        `json:"channel"`
	Event          string                        `json:"event"`
	ExpiresAt      time.Time                     `json:"expiresAt"`
	State          string                        `json:"state"`
	Attempts       int                           `json:"attempts"`
	AcknowledgedAt *time.Time                    `json:"acknowledgedAt,omitempty"`
	LastFailure    *string                       `json:"lastFailure,omitempty"`
	AttemptLog     []HighRiskNotificationAttempt `json:"attemptLog"`
}

type HighRiskNotificationAttempt struct {
	Number      int       `json:"number"`
	Outcome     string    `json:"outcome"`
	CompletedAt time.Time `json:"completedAt"`
}

func OpenHighRiskNotifier(store *metadata.Store, weComURL, feishuURL string) (*HighRiskNotifier, error) {
	receivers := map[string]string{
		HighRiskWeCom:  weComURL,
		HighRiskFeishu: feishuURL,
	}
	for channel, raw := range receivers {
		receiver, err := highRiskReceiverURL(raw)
		if err != nil {
			return nil, err
		}
		receivers[channel] = receiver
	}
	return &HighRiskNotifier{
		store:     store,
		receivers: receivers,
		client: &http.Client{
			Timeout: highRiskNotificationTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Transport: &http.Transport{
				DisableKeepAlives:      true,
				MaxResponseHeaderBytes: 8192,
			},
		},
	}, nil
}

func highRiskReceiverURL(raw string) (string, error) {
	invalid := errors.New("notification receiver must use a numeric loopback HTTP address and port")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", invalid
	}
	ip, ipErr := netip.ParseAddr(u.Hostname())
	port, portErr := strconv.ParseUint(u.Port(), 10, 16)
	if ipErr != nil || ip.Zone() != "" || !ip.IsLoopback() || portErr != nil || port == 0 {
		return "", invalid
	}
	return u.String(), nil
}

// Enqueue runs inside the caller's plan or decision transaction. A subsequent
// decision supersedes a pending approval invitation that has yet to be recorded
// as acknowledged.
func (n *HighRiskNotifier) Enqueue(ctx context.Context, tx pgx.Tx, orgID, actionID, recipientID, channel, event string, expiresAt *time.Time) error {
	if expiresAt == nil || n.receivers[channel] == "" {
		return ErrInput
	}
	switch event {
	case HighRiskApprovalRequired:
	case HighRiskApproved, HighRiskRejected:
		_, err := tx.Exec(ctx, `UPDATE mcpguard_ra.high_risk_notifications
SET state='SUPERSEDED',lease_until=NULL
WHERE org_id=$1 AND action_id=$2 AND event_kind='APPROVAL_REQUIRED' AND state IN ('PENDING','SENDING')`, orgID, actionID)
		if err != nil {
			return err
		}
	default:
		return ErrInput
	}
	_, err := tx.Exec(ctx, `INSERT INTO mcpguard_ra.high_risk_notifications
(org_id,action_id,recipient_id,notification_id,channel,event_kind,expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (org_id,action_id,event_kind) DO NOTHING`,
		orgID, actionID, recipientID, uuid.NewString(), channel, event, *expiresAt)
	return err
}

// Deliver leases one notification. A retry keeps the same notification ID so
// a receiver can acknowledge duplicates after a gateway interruption.
func (n *HighRiskNotifier) Deliver(ctx context.Context) error {
	_, err := n.store.Pool.Exec(ctx, `UPDATE mcpguard_ra.high_risk_notifications n
SET state=CASE WHEN n.expires_at<=statement_timestamp() THEN 'EXPIRED'
               WHEN n.event_kind='APPROVAL_REQUIRED' AND w.state<>'DEFERRED' THEN 'SUPERSEDED' ELSE 'FAILED' END,lease_until=NULL
FROM mcpguard_ra.write_requests w
WHERE (w.org_id,w.request_id)=(n.org_id,n.action_id) AND n.state IN ('PENDING','SENDING')
AND (n.expires_at<=statement_timestamp() OR (n.event_kind='APPROVAL_REQUIRED' AND w.state<>'DEFERRED')
     OR (n.lease_until<=statement_timestamp() AND n.attempts>=$1))`, highRiskNotificationMaxAttempts)
	if err != nil {
		return err
	}

	var notice highRiskNotification
	var attempts int
	err = n.store.Pool.QueryRow(ctx, `WITH candidate AS (
 SELECT n.notification_id FROM mcpguard_ra.high_risk_notifications n
 JOIN mcpguard_ra.write_requests w ON (w.org_id,w.request_id)=(n.org_id,n.action_id)
 WHERE n.attempts<$1 AND n.expires_at>statement_timestamp()
 AND (n.event_kind<>'APPROVAL_REQUIRED' OR w.state='DEFERRED')
 AND ((n.state='PENDING' AND n.next_at<=statement_timestamp()) OR (n.state='SENDING' AND n.lease_until<=statement_timestamp()))
 ORDER BY n.next_at,n.notification_id LIMIT 1 FOR UPDATE OF n SKIP LOCKED
)
UPDATE mcpguard_ra.high_risk_notifications n
SET state='SENDING',attempts=attempts+1,lease_until=clock_timestamp()+interval '15 seconds'
FROM candidate c WHERE n.notification_id=c.notification_id
RETURNING n.notification_id::text,n.action_id::text,n.recipient_id::text,n.channel,n.event_kind,n.expires_at,n.attempts`, highRiskNotificationMaxAttempts).
		Scan(&notice.NotificationID, &notice.ActionID, &notice.RecipientID, &notice.Channel, &notice.Event, &notice.ExpiresAt, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	sendCtx, cancel := context.WithDeadline(ctx, notice.ExpiresAt)
	failure := n.send(sendCtx, notice)
	cancel()
	state := "ACKNOWLEDGED"
	if failure != "" {
		state = "PENDING"
		if attempts >= highRiskNotificationMaxAttempts {
			state = "FAILED"
		}
	}
	finish, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer done()
	_, err = n.store.Pool.Exec(finish, `WITH delivery AS (
UPDATE mcpguard_ra.high_risk_notifications
SET state=CASE WHEN expires_at<=statement_timestamp() THEN 'EXPIRED' ELSE $3 END,
next_at=statement_timestamp()+make_interval(secs=>$4),lease_until=NULL,
acknowledged_at=CASE WHEN $3='ACKNOWLEDGED' AND expires_at>statement_timestamp() THEN statement_timestamp() END,
last_failure=NULLIF($5,'')
WHERE notification_id=$1 AND state='SENDING' AND attempts=$2
RETURNING notification_id,attempts,state
)
INSERT INTO mcpguard_ra.high_risk_notification_attempts(notification_id,attempt,outcome)
SELECT notification_id,attempts,coalesce(nullif($5,''),state) FROM delivery`, notice.NotificationID, attempts, state, 1<<attempts, failure)
	return err
}

func (n *HighRiskNotifier) send(ctx context.Context, notice highRiskNotification) string {
	if notice.Event == HighRiskApproved {
		notice.ClaimPath = "/console/api/v2/self-service/write-requests/" + notice.ActionID + "/authorization-code"
	}
	body, err := json.Marshal(notice)
	if err != nil {
		return "PAYLOAD_INVALID"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.receivers[notice.Channel], bytes.NewReader(body))
	if err != nil {
		return "RECEIVER_INVALID"
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := n.client.Do(req)
	if err != nil {
		return "RECEIVER_UNAVAILABLE"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "RECEIVER_REJECTED"
	}
	const maxAck = 8192
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAck+1))
	var ack struct {
		NotificationID string `json:"notificationId"`
	}
	if err != nil || len(raw) > maxAck || json.Unmarshal(raw, &ack) != nil || ack.NotificationID != notice.NotificationID {
		return "ACKNOWLEDGEMENT_INVALID"
	}
	return ""
}

// Receipts is scoped to one action. The caller must authorize access to that
// action before querying its delivery metadata.
func (n *HighRiskNotifier) Receipts(ctx context.Context, orgID, actionID string) ([]HighRiskNotificationReceipt, error) {
	rows, err := n.store.Pool.Query(ctx, `SELECT n.notification_id::text,n.action_id::text,n.recipient_id::text,
n.channel,n.event_kind,n.expires_at,n.state,n.attempts,n.acknowledged_at,n.last_failure,
coalesce((SELECT jsonb_agg(jsonb_build_object('number',a.attempt,'outcome',a.outcome,'completedAt',a.completed_at) ORDER BY a.attempt)
 FROM mcpguard_ra.high_risk_notification_attempts a WHERE a.notification_id=n.notification_id),'[]'::jsonb)
FROM mcpguard_ra.high_risk_notifications n WHERE n.org_id=$1 AND n.action_id=$2 ORDER BY n.created_at,n.notification_id`, orgID, actionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	receipts := []HighRiskNotificationReceipt{}
	for rows.Next() {
		var receipt HighRiskNotificationReceipt
		if err := rows.Scan(&receipt.NotificationID, &receipt.ActionID, &receipt.RecipientID, &receipt.Channel, &receipt.Event,
			&receipt.ExpiresAt, &receipt.State, &receipt.Attempts, &receipt.AcknowledgedAt, &receipt.LastFailure, &receipt.AttemptLog); err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}
