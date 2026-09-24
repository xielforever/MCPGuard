package rules

func BuiltinRules() []*Rule {
	return []*Rule{
		{
			ID:          "builtin-sql-danger",
			Name:        "SQL Danger",
			Description: "Intercept dangerous SQL operations",
			Pattern:     "(?i)(DELETE|DROP|TRUNCATE|ALTER\\s+TABLE)",
			ToolName:    "db_query",
			Action:      ActionConfirm,
			Enabled:     true,
			Priority:    100,
		},
		{
			ID:          "builtin-file-danger",
			Name:        "File Danger",
			Description: "Intercept dangerous file deletions",
			Pattern:     "(?i)(rm\\s+-rf|rmdir|del)",
			ToolName:    "execute_command",
			Action:      ActionConfirm,
			Enabled:     true,
			Priority:    100,
		},
		{
			ID:          "builtin-k8s-danger",
			Name:        "K8s Danger",
			Description: "Intercept kubernetes deletions",
			Pattern:     "(?i)(kubectl\\s+delete)",
			ToolName:    "execute_command",
			Action:      ActionConfirm,
			Enabled:     true,
			Priority:    100,
		},
		{
			ID:          "builtin-shell-danger",
			Name:        "Shell Danger",
			Description: "Block critical shell commands",
			Pattern:     "(?i)(shutdown|reboot|format)",
			ToolName:    "execute_command",
			Action:      ActionBlock,
			Enabled:     true,
			Priority:    200,
		},
	}
}
