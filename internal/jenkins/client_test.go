package jenkins

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const (
	testOrgID      = "d225a2cc-982e-4c97-8a12-a53c59952482"
	testParameters = `[
	  {"_class":"hudson.model.StringParameterDefinition","name":"GIT_REF"},
	  {"_class":"hudson.model.BooleanParameterDefinition","name":"RUN_TESTS","defaultParameterValue":{"value":false}}
	]`
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Config{
		ControllerID: "ci", OrgID: testOrgID, BaseURL: server.URL + "/jenkins",
		Username: "reader", APIToken: "fixture-api-token", Folders: []string{"orders"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func replyFixture(w io.Writer, jsonBody string) {
	_, _ = io.WriteString(w, jsonBody)
}

func describeFixture(w io.Writer, params string) {
	replyFixture(w, `{"name":"build","fullName":"orders/build","buildable":true,
		"property":[{"_class":"hudson.model.ParametersDefinitionProperty","parameterDefinitions":`+params+`}]}`)
}

func TestDiscoverFreezeAndReadQueue(t *testing.T) {
	var changed atomic.Bool
	var posts atomic.Int32
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		username, token, ok := r.BasicAuth()
		if !ok || username != "reader" || token != "fixture-api-token" {
			http.Error(w, "expected fixture identity", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /jenkins/job/orders/api/json":
			replyFixture(w, `{"jobs":[{"name":"build","fullName":"orders/build","buildable":true},
				{"name":"../foreign","buildable":true},
				{"name":"other","fullName":"foreign/other","buildable":true}]}`)
		case "GET /jenkins/job/orders/job/build/api/json":
			params := testParameters
			if changed.Load() {
				params = `[{"_class":"hudson.model.ChoiceParameterDefinition","name":"GIT_REF","choices":["main","release"]}]`
			}
			describeFixture(w, params)
		case "POST /jenkins/job/orders/job/build/buildWithParameters":
			posts.Add(1)
			if err := r.ParseForm(); err != nil || r.PostForm.Get("GIT_REF") != "feature/orders+summary" ||
				r.PostForm.Get("RUN_TESTS") != "false" || len(r.PostForm) != 2 {
				http.Error(w, "unexpected frozen form", http.StatusBadRequest)
				return
			}
			w.Header().Set("Location", "/jenkins/queue/item/36/")
			w.WriteHeader(http.StatusCreated)
		case "GET /jenkins/queue/item/36/api/json":
			replyFixture(w, `{"id":36,"cancelled":false,"task":{"url":"https://jenkins.example.org/jenkins/job/orders/job/build/"},"executable":{"number":5}}`)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := t.Context()
	jobs, err := client.ListJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].FullName != "orders/build" {
		t.Fatalf("unexpected discovery: %v %v", jobs, err)
	}
	if _, err := client.DescribeJob(ctx, jobs[0].JobRef+"a"); !errors.Is(err, ErrDenied) {
		t.Fatalf("modified job reference was accepted: %v", err)
	}
	job, err := client.DescribeJob(ctx, jobs[0].JobRef)
	if err != nil || !job.CanSubmit || !job.Parameters[0].Required || !job.Parameters[1].HasDefault ||
		job.Parameters[1].DefaultValue != false {
		t.Fatalf("unexpected build schema: %+v %v", job, err)
	}
	if _, err := client.Prepare(ctx, job.JobRef, job.SchemaRevision, map[string]any{"GIT_REF": "main", "RUN_TESTS": "false"}); !errors.Is(err, ErrInput) {
		t.Fatalf("boolean type mismatch accepted: %v", err)
	}
	if _, err := client.Prepare(ctx, job.JobRef, job.SchemaRevision, map[string]any{"GIT_REF": "main", "UNDECLARED": true}); !errors.Is(err, ErrInput) {
		t.Fatalf("undeclared parameter accepted: %v", err)
	}
	plan, err := client.Prepare(ctx, job.JobRef, job.SchemaRevision, map[string]any{"GIT_REF": "feature/orders+summary"})
	if err != nil || plan.Parameters["RUN_TESTS"] != false {
		t.Fatalf("boolean default did not freeze: %+v %v", plan, err)
	}
	r, err := client.Submit(ctx, plan)
	if err != nil || r.QueueID != 36 || r.Existing || posts.Load() != 1 {
		t.Fatalf("unexpected queue receipt: %+v %v (%d POSTs)", r, err, posts.Load())
	}
	queue, err := client.Queue(ctx, r)
	if err != nil || queue.State != "STARTED" || queue.BuildNumber == nil || *queue.BuildNumber != 5 {
		t.Fatalf("unexpected queue status: %+v %v", queue, err)
	}
	changed.Store(true)
	if _, err := client.Submit(ctx, plan); !errors.Is(err, ErrSchemaChanged) || posts.Load() != 1 {
		t.Fatalf("changed job definition led to POST: %v (%d POSTs)", err, posts.Load())
	}
}

func TestSubmitResponseHandlingNeverFollowsRedirect(t *testing.T) {
	for _, test := range []struct {
		name     string
		code     int
		location string
		existing bool
		want     error
	}{
		{"existing queue", http.StatusSeeOther, "/jenkins/queue/item/101/", true, nil},
		{"outside controller", http.StatusSeeOther, "https://foreign.example.org/jenkins/queue/item/101/", false, ErrOutcomeUnknown},
		{"wrong path", http.StatusCreated, "/queue/item/101/", false, ErrOutcomeUnknown},
		{"generic redirect", http.StatusFound, "/jenkins/queue/item/101/", false, ErrOutcomeUnknown},
		{"http rejection", http.StatusConflict, "", false, ErrRejected},
		{"ambiguous server error", http.StatusInternalServerError, "", false, ErrOutcomeUnknown},
		{"lost response", 0, "", false, ErrOutcomeUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			var posts, unexpected atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /jenkins/job/orders/job/build/api/json":
					replyFixture(w, `{"name":"build","fullName":"orders/build","buildable":true,"property":[]}`)
				case "POST /jenkins/job/orders/job/build/build":
					posts.Add(1)
					if test.code == 0 {
						conn, _, err := http.NewResponseController(w).Hijack()
						if err != nil {
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						_ = conn.Close()
						return
					}
					w.Header().Set("Location", test.location)
					w.WriteHeader(test.code)
				default:
					unexpected.Add(1)
					http.NotFound(w, r)
				}
			})
			// Use the same reference issuer as discovery, without an extra
			// network request in each response-handling case.
			job, err := client.DescribeJob(t.Context(), client.reference("orders/build"))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := client.Prepare(t.Context(), job.JobRef, job.SchemaRevision, nil)
			if err != nil {
				t.Fatal(err)
			}
			r, err := client.Submit(t.Context(), plan)
			if !errors.Is(err, test.want) || posts.Load() != 1 || unexpected.Load() != 0 {
				t.Fatalf("result=%+v error=%v POSTs=%d unexpected=%d", r, err, posts.Load(), unexpected.Load())
			}
			if err == nil && (r.QueueID != 101 || r.Existing != test.existing) {
				t.Fatalf("unexpected receipt: %+v", r)
			}
		})
	}
}

func TestUnsupportedParameterDefaultsDoNotLeaveJenkinsClient(t *testing.T) {
	p := parameter(rawParameter{
		Class: "hudson.model.PasswordParameterDefinition", Name: "PASSWORD", Description: "private value",
		Default: &struct {
			Value json.RawMessage `json:"value"`
		}{Value: json.RawMessage(`"private fixture token"`)},
	})
	if p.Type != "unsupported" || p.Description != "" || p.HasDefault || p.DefaultValue != nil {
		t.Fatalf("unsafe parameter exposed: %+v", p)
	}
}
