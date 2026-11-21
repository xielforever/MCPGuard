package resources

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHighRiskNotificationLocalChannels(t *testing.T) {
	const (
		actionID    = "5a4e18bb-5ccc-44f7-8032-0bf0dae97ba2"
		reviewerID  = "1b5397cf-9a41-4abf-bee1-c0ae19c6d008"
		requesterID = "c48b89f5-5004-44bf-abc2-1833b560f790"
		pendingID   = "e1d6da87-7240-4ccc-803b-d842ff99d821"
		approvedID  = "8f53d8bf-d726-4f9a-9131-079de6ab5147"
	)
	expiresAt := time.Now().UTC().Add(time.Minute)
	var attempts atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var notice highRiskNotification
		raw, err := io.ReadAll(r.Body)
		if err != nil || json.Unmarshal(raw, &notice) != nil || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Error("fixture receiver got an invalid request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if notice.ActionID != actionID || !notice.ExpiresAt.Equal(expiresAt) || strings.Contains(string(raw), `"authorizationCode"`) || r.URL.RawQuery != "" {
			t.Error("notification did not retain the safe action identifier and deadline")
		}
		switch r.URL.Path {
		case "/wecom":
			if notice.Channel != HighRiskWeCom || notice.Event != HighRiskApprovalRequired || notice.RecipientID != reviewerID || notice.NotificationID != pendingID || notice.ClaimPath != "" {
				t.Error("unexpected WeCom pending notice")
			}
			switch attempts.Add(1) {
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case 2:
				_ = json.NewEncoder(w).Encode(map[string]string{"notificationId": approvedID})
				return
			}
		case "/feishu":
			if notice.Channel != HighRiskFeishu || notice.Event != HighRiskApproved || notice.RecipientID != requesterID || notice.NotificationID != approvedID ||
				notice.ClaimPath != "/console/api/v2/self-service/write-requests/"+actionID+"/authorization-code" {
				t.Error("unexpected Feishu approval notice")
			}
		default:
			t.Errorf("unexpected notification receiver path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"notificationId": notice.NotificationID})
	}))
	defer receiver.Close()
	notifier, err := OpenHighRiskNotifier(nil, receiver.URL+"/wecom", receiver.URL+"/feishu")
	if err != nil {
		t.Fatal(err)
	}
	pending := highRiskNotification{NotificationID: pendingID, ActionID: actionID, RecipientID: reviewerID, Channel: HighRiskWeCom, Event: HighRiskApprovalRequired, ExpiresAt: expiresAt}
	for _, expected := range []string{"RECEIVER_REJECTED", "ACKNOWLEDGEMENT_INVALID", ""} {
		if failure := notifier.send(t.Context(), pending); failure != expected {
			t.Fatalf("WeCom delivery failure = %q, want %q", failure, expected)
		}
	}
	approved := highRiskNotification{NotificationID: approvedID, ActionID: actionID, RecipientID: requesterID, Channel: HighRiskFeishu, Event: HighRiskApproved, ExpiresAt: expiresAt}
	if failure := notifier.send(t.Context(), approved); failure != "" {
		t.Fatalf("Feishu delivery failure = %q", failure)
	}
}

func TestHighRiskNotificationReceiverIsLoopbackAndDoesNotRedirect(t *testing.T) {
	for _, address := range []string{
		"https://127.0.0.1:80/wecom", "http://localhost:80/wecom", "http://192.0.2.1:80/wecom",
		"http://127.0.0.1/wecom", "http://test@127.0.0.1:80/wecom", "http://127.0.0.1:80/wecom?test=1",
		"http://127.0.0.1:80/wecom#test", "http://127.0.0.1:0/wecom",
	} {
		if _, err := highRiskReceiverURL(address); err == nil {
			t.Errorf("accepted unexpected receiver address %q", address)
		}
	}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-target" {
			t.Error("notification followed a redirect")
			return
		}
		http.Redirect(w, r, "/redirect-target", http.StatusTemporaryRedirect)
	}))
	defer receiver.Close()
	notifier, err := OpenHighRiskNotifier(nil, receiver.URL+"/test", receiver.URL+"/test")
	if err != nil {
		t.Fatal(err)
	}
	notice := highRiskNotification{NotificationID: "8f53d8bf-d726-4f9a-9131-079de6ab5147", Channel: HighRiskWeCom}
	if failure := notifier.send(t.Context(), notice); failure != "RECEIVER_REJECTED" {
		t.Fatalf("redirect result = %q, want RECEIVER_REJECTED", failure)
	}
}
