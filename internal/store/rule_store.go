package store

import (
	"github.com/xielforever/MCPGuard/internal/rules"
)

func (s *Store) CreateRule(r *rules.Rule) error {
	_, err := s.db.Exec(`
		INSERT INTO rules (id, name, description, pattern, tool_name, action, enabled, priority)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, r.ID, r.Name, r.Description, r.Pattern, r.ToolName, r.Action, r.Enabled, r.Priority)
	return err
}

func (s *Store) GetRule(id string) (*rules.Rule, error) {
	var r rules.Rule
	err := s.db.QueryRow(`SELECT id, name, description, pattern, tool_name, action, enabled, priority FROM rules WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Description, &r.Pattern, &r.ToolName, &r.Action, &r.Enabled, &r.Priority)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpdateRule(r *rules.Rule) error {
	_, err := s.db.Exec(`
		UPDATE rules SET name=?, description=?, pattern=?, tool_name=?, action=?, enabled=?, priority=? WHERE id=?
	`, r.Name, r.Description, r.Pattern, r.ToolName, r.Action, r.Enabled, r.Priority, r.ID)
	return err
}

func (s *Store) DeleteRule(id string) error {
	_, err := s.db.Exec(`DELETE FROM rules WHERE id = ?`, id)
	return err
}

func (s *Store) ListRules() ([]*rules.Rule, error) {
	rows, err := s.db.Query(`SELECT id, name, description, pattern, tool_name, action, enabled, priority FROM rules ORDER BY priority DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*rules.Rule
	for rows.Next() {
		var r rules.Rule
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Pattern, &r.ToolName, &r.Action, &r.Enabled, &r.Priority); err != nil {
			return nil, err
		}
		result = append(result, &r)
	}
	return result, nil
}
