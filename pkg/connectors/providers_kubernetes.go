package connectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

const (
	maxDeployments = 100
	maxK8sResponse = 1 << 20
)

func (p *Providers) listDeployments(ctx context.Context, def Definition) (any, error) {
	config := def.Config
	base, host, err := httpEndpoint(config.BaseURL)
	if err != nil {
		return nil, err
	}
	if !dnsLabel.MatchString(config.Namespace) {
		return nil, ErrInput
	}
	clientTLS, err := p.tlsConfig(host, config)
	if err != nil {
		return nil, err
	}
	base.Path += "/apis/apps/v1/namespaces/" + config.Namespace + "/deployments"
	base.RawQuery = "limit=100"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, ErrInput
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+def.Credentials.Secret)
	client := &http.Client{
		Timeout: providerDeadline,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext:            p.policy.DialContext,
			TLSClientConfig:        clientTLS,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 16 << 10,
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return nil, ErrDenied
	case http.StatusOK:
	default:
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxK8sResponse+1))
	if err != nil || len(raw) > maxK8sResponse {
		return nil, ErrUnavailable
	}
	var list struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Replicas int32 `json:"replicas"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas     int32 `json:"readyReplicas"`
				AvailableReplicas int32 `json:"availableReplicas"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || list.APIVersion != "apps/v1" || list.Kind != "DeploymentList" ||
		list.Items == nil || len(list.Items) > maxDeployments {
		return nil, ErrUnavailable
	}
	deployments := make([]KubernetesDeployment, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.Namespace != config.Namespace || !dnsName(item.Metadata.Name) ||
			item.Spec.Replicas < 0 || item.Status.ReadyReplicas < 0 || item.Status.AvailableReplicas < 0 {
			return nil, ErrUnavailable
		}
		deployments = append(deployments, KubernetesDeployment{
			Name: item.Metadata.Name, DesiredReplicas: item.Spec.Replicas,
			ReadyReplicas: item.Status.ReadyReplicas, AvailableReplicas: item.Status.AvailableReplicas,
		})
	}
	return ListDeploymentsResult{
		Namespace: config.Namespace, Deployments: deployments, Truncated: list.Metadata.Continue != "",
	}, nil
}
