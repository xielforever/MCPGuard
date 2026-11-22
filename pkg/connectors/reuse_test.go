package connectors_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xielforever/MCPGuard/pkg/connectors"
)

func TestExecutionDefinitionJSONOmitsPrivateFields(t *testing.T) {
	definition := connectors.Definition{
		ID: "orders", OrgID: "test-organization", Type: connectors.Redis,
		Config:      connectors.Config{Host: "private-host", Port: 6379},
		Credentials: connectors.Credentials{Username: "fixture-user", Secret: "fixture-secret"},
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["ID"] == nil || fields["OrgID"] == nil || fields["Type"] == nil {
		t.Fatal("execution definition exposed fields beyond its binding identity")
	}
}

func TestJenkinsReadReturnsPublicResultTypes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("Jenkins connector attempted a mutation")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/api/json":
			_, _ = io.WriteString(w, `{"jobs":[{"name":"orders-validation","buildable":true}]}`)
		case "/job/orders-validation/api/json":
			_, _ = io.WriteString(w, `{"name":"orders-validation","buildable":true,"property":[
			 {"_class":"hudson.model.ParametersDefinitionProperty","parameterDefinitions":[
			 {"_class":"hudson.model.StringParameterDefinition","name":"REF","defaultParameterValue":{"value":"main"}}]}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	policy, err := connectors.NewEgressPolicy("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	providers := connectors.NewProviders(policy)
	ctx := context.Background()
	definition := connectors.Definition{
		ID: "orders-ci", OrgID: "00000000-0000-4000-8000-000000000001", Type: connectors.Jenkins,
		Config:      connectors.Config{BaseURL: upstream.URL, Folder: "/"},
		Credentials: connectors.Credentials{Username: "reader", Secret: "fixture-read-key"},
	}
	definition.Config, err = providers.Validate(ctx, definition.Type, definition.Config, definition.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := providers.Read(ctx, definition, connectors.ListJobs, connectors.ReadInput{})
	jobs, ok := listed.(connectors.ListJobsResult)
	if err != nil || !ok || len(jobs.Jobs) != 1 || jobs.Jobs[0].Name != "orders-validation" {
		t.Fatalf("unexpected usable jobs result: %T, err=%v", listed, err)
	}
	described, err := providers.Read(ctx, definition, connectors.DescribeJob, connectors.ReadInput{JobRef: jobs.Jobs[0].JobRef})
	job, ok := described.(connectors.DescribeJobResult)
	if err != nil || !ok || job.JobRef != jobs.Jobs[0].JobRef || !job.Buildable || job.CanSubmit ||
		job.SchemaRevision == "" || len(job.Parameters) != 1 || job.Parameters[0].DefaultValue != "main" {
		t.Fatalf("unexpected usable description result: %T, err=%v", described, err)
	}
	encoded, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"jobRef", "fullName", "name", "buildable", "parameters", "schemaRevision", "canSubmit"} {
		if fields[name] == nil {
			t.Fatalf("missing existing Jenkins result field %s", name)
		}
	}
	if _, err := providers.JenkinsForBuild(definition); !errors.Is(err, connectors.ErrDenied) {
		t.Fatalf("existing read-only connector allowed build: %v", err)
	}
	definition.Config.AllowBuilds = true
	described, err = providers.Read(ctx, definition, connectors.DescribeJob, connectors.ReadInput{JobRef: job.JobRef})
	buildJob, ok := described.(connectors.DescribeJobResult)
	if err != nil || !ok || !buildJob.CanSubmit || buildJob.SchemaRevision != job.SchemaRevision {
		t.Fatalf("unexpected build-enabled description: %T, err=%v", described, err)
	}
	client, err := providers.JenkinsForBuild(definition)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Prepare(ctx, buildJob.JobRef, buildJob.SchemaRevision, map[string]any{"REF": "release"})
	if err != nil || plan.FullName != job.FullName || plan.Parameters["REF"] != "release" {
		t.Fatalf("build plan did not use the same job definition: %v", err)
	}
}
