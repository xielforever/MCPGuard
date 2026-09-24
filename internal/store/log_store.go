package store

import (
	"time"
)

type AuditLog struct {
	ID          string    `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	ToolName    string    `json:"tool_name"`
	Params      string    `json:"params"`
	Action      string    `json:"action"`
	RuleName    string    `json:"rule_name"`
	ConfirmCode string    `json:"confirm_code"`
	Status      string    `json:"status"`
	UserID      string    `json:"user_id"`
}

func (s *Store) CreateLog(log *AuditLog) error {
	_, err := s.db.Exec(`
		INSERT INTO audit_logs (id, timestamp, tool_name, params, action, rule_name, confirm_code, status, user_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.ID, log.Timestamp, log.ToolName, log.Params, log.Action, log.RuleName, log.ConfirmCode, log.Status, log.UserID)
	return err
}

func (s *Store) ListLogs(limit, offset int) ([]*AuditLog, error) {
	rows, err := s.db.Query(`
		SELECT id, timestamp, tool_name, params, action, rule_name, confirm_code, status, user_id 
		FROM audit_logs 
		ORDER BY timestamp DESC 
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(&l.ID, &l.Timestamp, &l.ToolName, &l.Params, &l.Action, &l.RuleName, &l.ConfirmCode, &l.Status, &l.UserID); err != nil {
			return nil, err
		}
		result = append(result, &l)
	}
	return result, nil
}

func (s *Store) GetLogStats() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT action, COUNT(*) FROM audit_logs GROUP BY action`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make(map[string]int)
	for rows.Next() {
		var action string
		var count int
		if err := rows.Scan(&action, &count); err != nil {
			return nil, err
		}
		stats[action] = count
	}
	return stats, nil
}
