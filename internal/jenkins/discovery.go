package jenkins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxFolders     = 32
	maxJobs        = 256
	maxParameters  = 32
	maxChoices     = 64
	maxValueBytes  = 4096
	maxBuildValues = 16 << 10
	jobTree        = "name,fullName,buildable,jobs[name],property[_class,parameterDefinitions[_class,name,description,choices,defaultParameterValue[value]]]"
)

type Job struct {
	JobRef    string `json:"jobRef"`
	FullName  string `json:"fullName"`
	Name      string `json:"name"`
	Buildable bool   `json:"buildable"`
}

type Parameter struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Description  string   `json:"description,omitempty"`
	Choices      []string `json:"choices,omitempty"`
	HasDefault   bool     `json:"hasDefault"`
	DefaultValue any      `json:"defaultValue,omitempty"`
	Required     bool     `json:"required"`
}

type JobDescription struct {
	Job
	Parameters     []Parameter `json:"parameters"`
	SchemaRevision string      `json:"schemaRevision"`
	CanSubmit      bool        `json:"canSubmit"`
}

type listingItem struct {
	Name      string         `json:"name"`
	FullName  string         `json:"fullName"`
	Buildable *bool          `json:"buildable"`
	Jobs      *[]listingItem `json:"jobs"`
}

func (c *Client) ListJobs(ctx context.Context) ([]Job, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	todo := slices.Clone(c.cfg.Folders)
	seen := make(map[string]bool)
	jobs := make(map[string]Job)
	for len(todo) > 0 {
		folder := todo[0]
		todo = todo[1:]
		if seen[folder] {
			continue
		}
		if len(seen) >= maxFolders {
			return nil, ErrUnavailable
		}
		seen[folder] = true
		var listing listingItem
		if err := c.get(ctx, jobPath(folder)+"/api/json", "jobs[name,fullName,buildable,jobs[name]]", &listing); err != nil {
			return nil, err
		}
		if listing.Jobs == nil {
			return nil, ErrUnavailable
		}
		for _, entry := range *listing.Jobs {
			if !safeName(entry.Name) {
				continue
			}
			fullName := entry.Name
			if folder != "" {
				fullName = folder + "/" + fullName
			}
			if !c.inScope(fullName) || (entry.FullName != "" && entry.FullName != fullName) {
				continue
			}
			if entry.Jobs != nil {
				if strings.Count(fullName, "/") < maxDepth-1 {
					todo = append(todo, fullName)
				}
			} else if entry.Buildable != nil {
				jobs[fullName] = Job{c.reference(fullName), fullName, entry.Name, *entry.Buildable}
				if len(jobs) > maxJobs {
					return nil, ErrUnavailable
				}
			}
		}
	}
	out := make([]Job, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, job)
	}
	slices.SortFunc(out, func(a, b Job) int { return strings.Compare(a.FullName, b.FullName) })
	return out, nil
}

type rawParameter struct {
	Class       string   `json:"_class"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Choices     []string `json:"choices"`
	Default     *struct {
		Value json.RawMessage `json:"value"`
	} `json:"defaultParameterValue"`
}

type jobDetails struct {
	listingItem
	Properties *[]struct {
		Class       string          `json:"_class"`
		Definitions *[]rawParameter `json:"parameterDefinitions"`
	} `json:"property"`
}

func (c *Client) DescribeJob(ctx context.Context, jobRef string) (JobDescription, error) {
	fullName, err := c.jobName(jobRef)
	if err != nil {
		return JobDescription{}, err
	}
	var details jobDetails
	if err := c.get(ctx, jobPath(fullName)+"/api/json", jobTree, &details); err != nil {
		return JobDescription{}, err
	}
	parts := strings.Split(fullName, "/")
	name := parts[len(parts)-1]
	if details.Name != name || details.Jobs != nil || details.Buildable == nil ||
		(details.FullName != "" && details.FullName != fullName) {
		return JobDescription{}, ErrDenied
	}
	if details.Properties == nil {
		return JobDescription{}, ErrUnavailable
	}
	job := Job{jobRef, fullName, name, *details.Buildable}
	out := JobDescription{Job: job, CanSubmit: job.Buildable, Parameters: make([]Parameter, 0)}
	parametersSeen := make(map[string]bool)
	for _, property := range *details.Properties {
		if property.Definitions == nil {
			if property.Class == "hudson.model.ParametersDefinitionProperty" {
				return JobDescription{}, ErrUnavailable
			}
			continue
		}
		if property.Class != "hudson.model.ParametersDefinitionProperty" {
			return JobDescription{}, ErrUnavailable
		}
		for _, raw := range *property.Definitions {
			if len(out.Parameters) >= maxParameters || !validParameterName(raw.Name) || parametersSeen[raw.Name] {
				return JobDescription{}, ErrUnavailable
			}
			parametersSeen[raw.Name] = true
			param := parameter(raw)
			out.Parameters = append(out.Parameters, param)
			if param.Type == "unsupported" {
				out.CanSubmit = false
			}
		}
	}
	slices.SortFunc(out.Parameters, func(a, b Parameter) int { return strings.Compare(a.Name, b.Name) })
	raw, _ := json.Marshal([]any{c.binding, fullName, out.Buildable, out.Parameters})
	sum := sha256.Sum256(raw)
	out.SchemaRevision = hex.EncodeToString(sum[:])
	return out, nil
}

func validParameterName(name string) bool {
	return name != "" && len(name) <= 128 && name == strings.TrimSpace(name) &&
		utf8.ValidString(name) && !strings.ContainsFunc(name, unicode.IsControl)
}

func parameter(raw rawParameter) Parameter {
	unsupported := Parameter{Name: raw.Name, Type: "unsupported", Required: true}
	name := strings.ToUpper(raw.Name)
	switch name {
	case "TOKEN", "CAUSE", "DELAY", "JSON", "STATUSCODE", "REDIRECTTO":
		// Jenkins may interpret these as request controls, not job values.
		return unsupported
	}
	for _, marker := range []string{"SECRET", "PASSWORD", "TOKEN", "CREDENTIAL", "PRIVATE_KEY", "API_KEY"} {
		if strings.Contains(name, marker) {
			return unsupported
		}
	}
	parameterType := "unsupported"
	switch raw.Class {
	case "hudson.model.StringParameterDefinition":
		parameterType = "string"
	case "hudson.model.TextParameterDefinition":
		parameterType = "text"
	case "hudson.model.BooleanParameterDefinition":
		parameterType = "boolean"
	case "hudson.model.ChoiceParameterDefinition":
		parameterType = "choice"
	}
	if parameterType == "unsupported" {
		return unsupported
	}
	out := Parameter{Name: raw.Name, Type: parameterType, Required: true}
	if description := raw.Description; validString(description, 1024) {
		out.Description = description
	}
	if parameterType == "choice" {
		if len(raw.Choices) == 0 || len(raw.Choices) > maxChoices {
			return unsupported
		}
		for _, choice := range raw.Choices {
			if !validString(choice, maxValueBytes) {
				return unsupported
			}
		}
		out.Choices = raw.Choices
	}
	if raw.Default != nil {
		var value any
		if json.Unmarshal(raw.Default.Value, &value) == nil && out.validValue(value) {
			out.HasDefault = true
			out.DefaultValue = value
			out.Required = false
		}
	}
	return out
}

func validString(value string, max int) bool {
	return len(value) <= max && utf8.ValidString(value) &&
		!strings.ContainsFunc(value, func(r rune) bool {
			return unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r'
		})
}

func (p Parameter) validValue(value any) bool {
	switch p.Type {
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "string", "text", "choice":
		s, ok := value.(string)
		return ok && validString(s, maxValueBytes) && (p.Type != "choice" || slices.Contains(p.Choices, s))
	default:
		return false
	}
}

type BuildPlan struct {
	JobRef         string         `json:"jobRef"`
	FullName       string         `json:"fullName"`
	SchemaRevision string         `json:"schemaRevision"`
	Parameters     map[string]any `json:"parameters"`
}

// Prepare resolves and validates each effective value before it can be frozen
// for independent approval.
func (c *Client) Prepare(ctx context.Context, jobRef, schemaRevision string, values map[string]any) (BuildPlan, error) {
	job, err := c.DescribeJob(ctx, jobRef)
	if err != nil {
		return BuildPlan{}, err
	}
	if job.SchemaRevision != schemaRevision {
		return BuildPlan{}, ErrSchemaChanged
	}
	if !job.CanSubmit {
		return BuildPlan{}, ErrDenied
	}
	resolved := make(map[string]any, len(job.Parameters))
	provided := 0
	for _, p := range job.Parameters {
		value, ok := values[p.Name]
		if ok {
			provided++
		} else if p.HasDefault {
			value = p.DefaultValue
		} else {
			return BuildPlan{}, ErrInput
		}
		if !p.validValue(value) {
			return BuildPlan{}, ErrInput
		}
		resolved[p.Name] = value
	}
	if provided != len(values) || len(buildForm(resolved).Encode()) > maxBuildValues {
		return BuildPlan{}, ErrInput
	}
	return BuildPlan{jobRef, job.FullName, job.SchemaRevision, resolved}, nil
}
