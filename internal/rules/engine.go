package rules

import (
	"regexp"
	"sort"
	"sync"
)

const (
	ActionPass    = "pass"
	ActionConfirm = "confirm"
	ActionBlock   = "block"
)

type Rule struct {
	ID          string
	Name        string
	Description string
	Pattern     string
	ToolName    string
	Action      string
	Enabled     bool
	Priority    int
	regex       *regexp.Regexp
}

type Engine struct {
	mu    sync.RWMutex
	rules []*Rule
}

func NewEngine() *Engine {
	return &Engine{
		rules: make([]*Rule, 0),
	}
}

func (e *Engine) AddRule(r *Rule) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if r.Pattern != "" {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return err
		}
		r.regex = re
	}

	e.rules = append(e.rules, r)
	e.sortRules()
	return nil
}

func (e *Engine) sortRules() {
	sort.Slice(e.rules, func(i, j int) bool {
		return e.rules[i].Priority > e.rules[j].Priority
	})
}

func (e *Engine) Evaluate(method string, params string) (string, *Rule) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.rules {
		if !rule.Enabled {
			continue
		}
		if rule.regex != nil && rule.regex.MatchString(params) {
			return rule.Action, rule
		}
	}
	return ActionPass, nil
}

func (e *Engine) RemoveRule(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, r := range e.rules {
		if r.ID == id {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			break
		}
	}
}

func (e *Engine) ListRules() []*Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	res := make([]*Rule, len(e.rules))
	copy(res, e.rules)
	return res
}
