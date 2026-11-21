package connectors

import "errors"

// MaxRedisKeyBytes is the connector's limit for both probes and reads.
const MaxRedisKeyBytes = 1024

// Kind identifies a supported backend.
type Kind string

const (
	PostgreSQL Kind = "postgresql"
	Redis      Kind = "redis"
	Jenkins    Kind = "jenkins"
	Kubernetes Kind = "kubernetes"
)

const (
	ListTables      = "list_tables"
	GetKey          = "get"
	ListJobs        = "list_jobs"
	DescribeJob     = "describe_job"
	ListDeployments = "list_deployments"
)

var (
	ErrInput       = errors.New("invalid connector input")
	ErrDenied      = errors.New("connector access denied")
	ErrUnavailable = errors.New("connector unavailable")
)

// Config contains the endpoint and fixed read scope for one backend kind.
type Config struct {
	Host          string `json:"host,omitempty"`
	Port          int    `json:"port,omitempty"`
	TLS           bool   `json:"tls,omitempty"`
	CACertificate string `json:"caCertificate,omitempty"`
	Database      string `json:"database,omitempty"`
	Schema        string `json:"schema,omitempty"`
	KeyPrefix     string `json:"keyPrefix,omitempty"`
	ProbeKey      string `json:"probeKey,omitempty"`
	BaseURL       string `json:"baseUrl,omitempty"`
	Folder        string `json:"folder,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
}

// Credentials holds private backend authentication material.
type Credentials struct {
	Username string `json:"username,omitempty"`
	Secret   string `json:"secret"`
}

// Definition is an authorized connection binding from the host application.
// A Jenkins binding requires a stable connector ID and organization UUID
// to keep job references unique to that endpoint and its authorized scope.
// JSON representations omit configuration and credentials.
type Definition struct {
	ID          string
	OrgID       string
	Type        Kind
	Config      Config      `json:"-"`
	Credentials Credentials `json:"-"`
}

// ReadInput carries the operation-specific selector.
type ReadInput struct {
	Key    string `json:"key,omitempty"`
	JobRef string `json:"jobRef,omitempty"`
}

type ListTablesResult struct {
	Schema    string   `json:"schema"`
	Tables    []string `json:"tables"`
	Truncated bool     `json:"truncated"`
}

type GetKeyResult struct {
	Key      string  `json:"key"`
	Found    bool    `json:"found"`
	Value    *string `json:"value,omitempty"`
	Encoding string  `json:"encoding,omitempty"`
}

type ListJobsResult struct {
	Jobs []JenkinsJob `json:"jobs"`
}

type JenkinsJob struct {
	JobRef    string `json:"jobRef"`
	FullName  string `json:"fullName"`
	Name      string `json:"name"`
	Buildable bool   `json:"buildable"`
}

type JenkinsParameter struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Description  string   `json:"description,omitempty"`
	Choices      []string `json:"choices,omitempty"`
	HasDefault   bool     `json:"hasDefault"`
	DefaultValue any      `json:"defaultValue,omitempty"`
	Required     bool     `json:"required"`
}

type DescribeJobResult struct {
	JenkinsJob
	Parameters     []JenkinsParameter `json:"parameters"`
	SchemaRevision string             `json:"schemaRevision"`
	CanSubmit      bool               `json:"canSubmit"`
}

type ListDeploymentsResult struct {
	Namespace   string                 `json:"namespace"`
	Deployments []KubernetesDeployment `json:"deployments"`
	Truncated   bool                   `json:"truncated"`
}

type KubernetesDeployment struct {
	Name              string `json:"name"`
	DesiredReplicas   int32  `json:"desiredReplicas"`
	ReadyReplicas     int32  `json:"readyReplicas"`
	AvailableReplicas int32  `json:"availableReplicas"`
}
