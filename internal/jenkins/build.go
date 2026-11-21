package jenkins

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Receipt struct {
	QueueID  int64  `json:"queueId"`
	Existing bool   `json:"existing"`
	JobRef   string `json:"jobRef"`
}

type QueueState struct {
	QueueID     int64  `json:"queueId"`
	State       string `json:"state"`
	BuildNumber *int64 `json:"buildNumber,omitempty"`
}

func buildForm(params map[string]any) url.Values {
	form := make(url.Values, len(params))
	for key, value := range params {
		switch value := value.(type) {
		case string:
			form.Set(key, value)
		case bool:
			form.Set(key, strconv.FormatBool(value))
		}
	}
	return form
}

// Submit checks the approved values against the current job definition. After
// a single POST has begun, an untrustworthy response leaves its effect unknown.
func (c *Client) Submit(ctx context.Context, plan BuildPlan) (Receipt, error) {
	checked, err := c.Prepare(ctx, plan.JobRef, plan.SchemaRevision, plan.Parameters)
	if err != nil {
		return Receipt{}, err
	}
	if plan.FullName != checked.FullName || !maps.Equal(plan.Parameters, checked.Parameters) {
		return Receipt{}, ErrSchemaChanged
	}
	if ctx.Err() != nil {
		return Receipt{}, ErrUnavailable
	}
	operation := "/buildWithParameters"
	if len(plan.Parameters) == 0 {
		operation = "/build"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.endpoint(jobPath(plan.FullName)+operation, nil).String(),
		strings.NewReader(buildForm(plan.Parameters).Encode()))
	if err != nil {
		return Receipt{}, ErrUnavailable
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.APIToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The transport uses a fresh connection and never follows redirects.
	req.GetBody = nil
	resp, err := c.http.Do(req)
	if err != nil {
		return Receipt{}, ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusSeeOther:
		id, err := c.queueID(req.URL, resp.Header.Values("Location"))
		if err != nil {
			return Receipt{}, err
		}
		return Receipt{QueueID: id, Existing: resp.StatusCode == http.StatusSeeOther, JobRef: plan.JobRef}, nil
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusMethodNotAllowed, http.StatusConflict:
		return Receipt{}, ErrRejected
	default:
		return Receipt{}, ErrOutcomeUnknown
	}
}

func (c *Client) queueID(request *url.URL, locations []string) (int64, error) {
	if len(locations) != 1 || locations[0] == "" {
		return 0, ErrOutcomeUnknown
	}
	relative, err := url.Parse(locations[0])
	if err != nil || relative.Opaque != "" {
		return 0, ErrOutcomeUnknown
	}
	u := request.ResolveReference(relative)
	if u.Scheme != c.base.Scheme || !strings.EqualFold(u.Hostname(), c.base.Hostname()) ||
		effectivePort(u) != effectivePort(c.base) || u.User != nil || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return 0, ErrOutcomeUnknown
	}
	item, ok := strings.CutPrefix(u.Path, c.base.Path+"/queue/item/")
	item = strings.TrimSuffix(item, "/")
	id, err := strconv.ParseInt(item, 10, 64)
	if !ok || err != nil || id < 1 || strconv.FormatInt(id, 10) != item {
		return 0, ErrOutcomeUnknown
	}
	return id, nil
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// Queue only reads the queue item in a receipt owned by the caller's action.
// The service must authorize access to that action before calling Queue.
func (c *Client) Queue(ctx context.Context, receipt Receipt) (QueueState, error) {
	fullName, err := c.jobName(receipt.JobRef)
	if err != nil {
		return QueueState{}, err
	}
	if receipt.QueueID < 1 {
		return QueueState{}, ErrInput
	}
	path := "/queue/item/" + strconv.FormatInt(receipt.QueueID, 10) + "/api/json"
	var row struct {
		ID        int64 `json:"id"`
		Cancelled bool  `json:"cancelled"`
		Task      *struct {
			URL string `json:"url"`
		} `json:"task"`
		Executable *struct {
			Number int64 `json:"number"`
		} `json:"executable"`
	}
	if err := c.get(ctx, path, "id,cancelled,task[url],executable[number]", &row); err != nil {
		return QueueState{}, err
	}
	if row.ID != receipt.QueueID || row.Task == nil {
		return QueueState{}, ErrUnavailable
	}
	// Jenkins may publish the public-facing base URL behind a reverse proxy.
	// Compare only its job path, and never contact that reported URL.
	task, err := url.Parse(row.Task.URL)
	if err != nil {
		return QueueState{}, ErrUnavailable
	}
	taskPath := task.Path
	if !strings.HasPrefix(taskPath, "/") {
		taskPath = c.base.Path + "/" + taskPath
	}
	if strings.TrimSuffix(taskPath, "/") != c.base.Path+jobPath(fullName) {
		return QueueState{}, ErrUnavailable
	}
	out := QueueState{QueueID: row.ID, State: "QUEUED"}
	if row.Cancelled {
		out.State = "CANCELLED"
	} else if row.Executable != nil {
		if row.Executable.Number < 1 {
			return QueueState{}, ErrUnavailable
		}
		out.State = "STARTED"
		out.BuildNumber = &row.Executable.Number
	}
	return out, nil
}
