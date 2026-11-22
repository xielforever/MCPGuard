package connectors

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xielforever/MCPGuard/internal/jenkins"
)

const providerDeadline = 5 * time.Second

var (
	dnsLabel    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	bearerToken = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)
)

// Providers applies endpoint and egress policy to stored connector scopes.
type Providers struct {
	policy *EgressPolicy
}

func NewProviders(policy *EgressPolicy) *Providers {
	return &Providers{policy: policy}
}

func validText(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && utf8.ValidString(value) &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func dnsName(value string) bool {
	if len(value) > 253 || value == "" {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func normalizeHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if address, err := netip.ParseAddr(host); err == nil && address.Zone() == "" {
		return address.Unmap().String(), nil
	}
	if !dnsName(host) {
		return "", ErrInput
	}
	return host, nil
}

func loopbackHost(host string) bool {
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func httpEndpoint(value string) (*url.URL, string, error) {
	base, err := url.Parse(strings.TrimSpace(value))
	if err != nil || base.Opaque != "" || base.User != nil || base.RawQuery != "" || base.ForceQuery ||
		base.Fragment != "" || base.RawPath != "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, "", ErrInput
	}
	host, err := normalizeHost(base.Hostname())
	if err != nil || (base.Scheme == "http" && !loopbackHost(host)) {
		return nil, "", ErrInput
	}
	port := base.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, "", ErrInput
		}
		base.Host = net.JoinHostPort(host, strconv.Itoa(number))
	} else if strings.Contains(host, ":") {
		base.Host = "[" + host + "]"
	} else {
		base.Host = host
	}
	base.Path = strings.TrimSuffix(base.Path, "/")
	if base.Path != "" && !jenkins.ValidFolder(strings.TrimPrefix(base.Path, "/")) {
		return nil, "", ErrInput
	}
	return base, host, nil
}

// Validate normalizes the public fields and validates egress without contacting
// a backend or using any credential.
func (p *Providers) Validate(ctx context.Context, kind Kind, config Config, credentials Credentials) (Config, error) {
	if p == nil || p.policy == nil {
		return Config{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, providerDeadline)
	defer cancel()
	if !validText(credentials.Secret, 8192) {
		return Config{}, ErrInput
	}
	if kind != Kubernetes && !validText(credentials.Username, 256) {
		return Config{}, ErrInput
	}
	if config.AllowBuilds && kind != Jenkins {
		return Config{}, ErrInput
	}
	var host string
	switch kind {
	case PostgreSQL, MySQL, Redis:
		if config.BaseURL != "" || config.Folder != "" || config.Namespace != "" {
			return Config{}, ErrInput
		}
		var err error
		host, err = normalizeHost(config.Host)
		if err != nil || (!config.TLS && !loopbackHost(host)) || config.Port < 0 || config.Port > 65535 {
			return Config{}, ErrInput
		}
		config.Host = host
		switch kind {
		case PostgreSQL:
			if !validText(config.Database, 63) || !validText(config.Schema, 63) ||
				config.KeyPrefix != "" || config.ProbeKey != "" {
				return Config{}, ErrInput
			}
			if config.Port == 0 {
				config.Port = 5432
			}
		case MySQL:
			if !validMySQLTableName(config.Database) || config.Schema != "" || config.KeyPrefix != "" || config.ProbeKey != "" {
				return Config{}, ErrInput
			}
			if config.Port == 0 {
				config.Port = 3306
			}
		case Redis:
			if !validText(config.KeyPrefix, 256) || !validText(config.ProbeKey, MaxRedisKeyBytes) ||
				!strings.HasPrefix(config.ProbeKey, config.KeyPrefix) || config.Database != "" || config.Schema != "" {
				return Config{}, ErrInput
			}
			if config.Port == 0 {
				config.Port = 6379
			}
		}
	case Jenkins, Kubernetes:
		if config.Host != "" || config.Port != 0 || config.Database != "" || config.Schema != "" ||
			config.KeyPrefix != "" || config.ProbeKey != "" {
			return Config{}, ErrInput
		}
		base, endpointHost, err := httpEndpoint(config.BaseURL)
		if err != nil {
			return Config{}, err
		}
		host = endpointHost
		config.BaseURL = base.String()
		config.TLS = base.Scheme == "https"
		if kind == Jenkins {
			if config.Namespace != "" || (config.Folder != "/" && !jenkins.ValidFolder(config.Folder)) ||
				strings.Contains(credentials.Username, ":") || len(credentials.Secret) > 1024 {
				return Config{}, ErrInput
			}
		} else if config.Folder != "" || !dnsLabel.MatchString(config.Namespace) ||
			credentials.Username != "" || !bearerToken.MatchString(credentials.Secret) {
			return Config{}, ErrInput
		}
	default:
		return Config{}, ErrInput
	}
	if _, err := p.tlsConfig(host, config); err != nil {
		return Config{}, err
	}
	if _, err := p.policy.Resolve(ctx, host); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (p *Providers) tlsConfig(host string, config Config) (*tls.Config, error) {
	if config.TLS {
		return p.policy.TLSConfig(host, config.CACertificate)
	}
	if !loopbackHost(host) || config.CACertificate != "" {
		return nil, ErrInput
	}
	return nil, nil
}

// Probe checks the backend by performing its scoped read.
func (p *Providers) Probe(ctx context.Context, def Definition) error {
	var operation string
	arguments := ReadInput{}
	switch def.Type {
	case PostgreSQL, MySQL:
		operation = ListTables
	case Redis:
		operation, arguments.Key = GetKey, def.Config.ProbeKey
	case Jenkins:
		operation = ListJobs
	case Kubernetes:
		operation = ListDeployments
	default:
		return ErrInput
	}
	_, err := p.Read(ctx, def, operation, arguments)
	return err
}

// Read returns the result type corresponding to the selected read operation.
func (p *Providers) Read(ctx context.Context, def Definition, operation string, arguments ReadInput) (any, error) {
	if p == nil || p.policy == nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, providerDeadline)
	defer cancel()
	switch def.Type {
	case PostgreSQL:
		if operation == ListTables && arguments == (ReadInput{}) {
			return p.listTables(ctx, def)
		}
	case MySQL:
		if operation == ListTables && arguments == (ReadInput{}) {
			return p.readMySQLMetadata(ctx, def, "")
		}
		if operation == ListColumns && arguments.Key == "" && arguments.JobRef == "" && validMySQLTableName(arguments.Table) {
			return p.readMySQLMetadata(ctx, def, arguments.Table)
		}
	case Redis:
		if operation == GetKey && arguments.JobRef == "" && arguments.Table == "" && validText(arguments.Key, MaxRedisKeyBytes) {
			if !strings.HasPrefix(arguments.Key, def.Config.KeyPrefix) || def.Config.KeyPrefix == "" {
				return nil, ErrDenied
			}
			return p.getKey(ctx, def, arguments.Key)
		}
	case Jenkins:
		if (operation == ListJobs && arguments == (ReadInput{})) ||
			(operation == DescribeJob && arguments.Key == "" && arguments.Table == "" && validText(arguments.JobRef, 768)) {
			return p.readJenkins(ctx, def, operation, arguments.JobRef)
		}
	case Kubernetes:
		if operation == ListDeployments && arguments == (ReadInput{}) {
			return p.listDeployments(ctx, def)
		}
	}
	return nil, ErrInput
}

// JenkinsForBuild gives the controlled executor access to a build-enabled
// connector. The caller still has to authorize and approve the action.
func (p *Providers) JenkinsForBuild(def Definition) (*jenkins.Client, error) {
	if def.Type != Jenkins || !def.Config.AllowBuilds {
		return nil, ErrDenied
	}
	return p.jenkinsClient(def)
}

func (p *Providers) jenkinsClient(def Definition) (*jenkins.Client, error) {
	if p == nil || p.policy == nil {
		return nil, ErrUnavailable
	}
	base, host, err := httpEndpoint(def.Config.BaseURL)
	if err != nil {
		return nil, err
	}
	clientTLS, err := p.tlsConfig(host, def.Config)
	if err != nil {
		return nil, err
	}
	if def.Config.Folder == "" {
		return nil, ErrInput
	}
	folder := def.Config.Folder
	if folder == "/" {
		folder = ""
	}
	client, err := jenkins.New(jenkins.Config{
		ControllerID: def.ID, OrgID: def.OrgID, BaseURL: base.String(),
		Username: def.Credentials.Username, APIToken: def.Credentials.Secret, Folders: []string{folder},
	}, p.policy.DialContext, clientTLS)
	if err != nil {
		return nil, ErrInput
	}
	return client, nil
}

func (p *Providers) readJenkins(ctx context.Context, def Definition, operation, jobRef string) (any, error) {
	client, err := p.jenkinsClient(def)
	if err != nil {
		return nil, err
	}
	if operation == ListJobs {
		jobs, err := client.ListJobs(ctx)
		if err != nil {
			return nil, jenkinsReadError(err)
		}
		result := make([]JenkinsJob, len(jobs))
		for i, job := range jobs {
			result[i] = JenkinsJob(job)
		}
		return ListJobsResult{Jobs: result}, nil
	}
	job, err := client.DescribeJob(ctx, jobRef)
	if err != nil {
		return nil, jenkinsReadError(err)
	}
	result := DescribeJobResult{
		JenkinsJob: JenkinsJob(job.Job), SchemaRevision: job.SchemaRevision,
		CanSubmit: def.Config.AllowBuilds && job.CanSubmit,
	}
	if job.Parameters != nil {
		result.Parameters = make([]JenkinsParameter, len(job.Parameters))
		for i, parameter := range job.Parameters {
			result.Parameters[i] = JenkinsParameter(parameter)
		}
	}
	return result, nil
}

func jenkinsReadError(err error) error {
	switch {
	case errors.Is(err, jenkins.ErrInput):
		return ErrInput
	case errors.Is(err, jenkins.ErrDenied):
		return ErrDenied
	default:
		return ErrUnavailable
	}
}
