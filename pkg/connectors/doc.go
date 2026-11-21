// Package connectors runs bounded reads against configured PostgreSQL, Redis,
// Jenkins, and Kubernetes backends. Applications manage connector registration,
// credentials, authorization, and auditing.
//
// Construct an EgressPolicy with NewEgressPolicy, then use Providers.Validate
// to normalize a new configuration. Pass the stored configuration and credentials
// to Providers.Probe or Providers.Read after authorizing the operation. Every
// connection enforces the egress policy at dial time.
package connectors
