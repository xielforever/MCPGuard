package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const nacosMetadataTimeout = 8 * time.Second
const nacosResponseLimit = 64 << 10

func loadMetadataDSN(ctx context.Context) (string, error) {
	dsn := os.Getenv("MCPGUARD_METADATA_DSN")
	switch os.Getenv("ZHIQI_METADATA_SOURCE") {
	case "", "env":
		if dsn == "" {
			return "", errors.New("MCPGUARD_METADATA_DSN is required")
		}
		return dsn, nil
	case "nacos":
		if dsn != "" {
			return "", errors.New("unset MCPGUARD_METADATA_DSN when ZHIQI_METADATA_SOURCE=nacos")
		}
		return loadNacosMetadataDSN(ctx)
	default:
		return "", errors.New("ZHIQI_METADATA_SOURCE must be empty, env or nacos")
	}
}

func loadNacosMetadataDSN(ctx context.Context) (string, error) {
	endpoint, err := url.Parse(os.Getenv("ZHIQI_NACOS_URL"))
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return "", errors.New("ZHIQI_NACOS_URL must be an HTTP(S) address without credentials, query or fragment")
	}
	if endpoint.Path == "" {
		endpoint.Path = "/nacos"
	}
	namespace := strings.TrimSpace(os.Getenv("ZHIQI_NACOS_NAMESPACE_ID"))
	group := strings.TrimSpace(os.Getenv("ZHIQI_NACOS_GROUP"))
	dataID := strings.TrimSpace(os.Getenv("ZHIQI_NACOS_DATA_ID"))
	nacosUser := strings.TrimSpace(os.Getenv("ZHIQI_NACOS_USERNAME"))
	nacosPassword := os.Getenv("ZHIQI_NACOS_PASSWORD")
	database := strings.TrimSpace(os.Getenv("ZHIQI_METADATA_DATABASE"))
	caFile := strings.TrimSpace(os.Getenv("ZHIQI_METADATA_CA_FILE"))
	if namespace == "" || group == "" || dataID == "" || nacosUser == "" || nacosPassword == "" || database == "" || caFile == "" {
		return "", errors.New("Nacos namespace, group, data ID, login, metadata database and database CA file are required")
	}
	port := os.Getenv("ZHIQI_METADATA_PORT")
	if port == "" {
		port = "5432"
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return "", errors.New("ZHIQI_METADATA_PORT must be a valid TCP port")
	}

	ctx, cancel := context.WithTimeout(ctx, nacosMetadataTimeout)
	defer cancel()
	// Configuration is fetched directly from the chosen Nacos service. In
	// particular, a redirect must not forward the login or token elsewhere.
	client := &http.Client{
		Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	form := url.Values{"username": {nacosUser}, "password": {nacosPassword}}
	loginURL := endpoint.JoinPath("v3/auth/user/login")
	loginRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("invalid Nacos metadata login request")
	}
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var login struct {
		AccessToken string `json:"accessToken"`
		GlobalAdmin bool   `json:"globalAdmin"`
	}
	if !readNacosJSON(client, loginRequest, &login) || strings.TrimSpace(login.AccessToken) == "" {
		return "", errors.New("Nacos metadata login failed")
	}
	if login.GlobalAdmin {
		return "", errors.New("Nacos metadata login requires a configuration reader")
	}

	configURL := endpoint.JoinPath("v3/client/cs/config")
	configURL.RawQuery = url.Values{"namespaceId": {namespace}, "groupName": {group}, "dataId": {dataID}}.Encode()
	configRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, configURL.String(), nil)
	if err != nil {
		return "", errors.New("invalid Nacos metadata configuration request")
	}
	configRequest.Header.Set("accessToken", login.AccessToken)
	var response struct {
		Code *int `json:"code"`
		Data struct {
			Content string `json:"content"`
		} `json:"data"`
	}
	if !readNacosJSON(client, configRequest, &response) || response.Code == nil || *response.Code != 0 {
		return "", errors.New("Nacos metadata configuration could not be read")
	}

	var config struct {
		Host     string `json:"host"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(response.Data.Content), &config); err != nil {
		return "", errors.New("Nacos metadata configuration must be a JSON object")
	}
	address, err := netip.ParseAddr(config.Host)
	if err != nil || address.Zone() != "" || strings.TrimSpace(config.Username) == "" || config.Password == "" {
		return "", errors.New("Nacos metadata configuration requires host IP, username and password")
	}
	query := url.Values{"sslmode": {"verify-ca"}, "sslrootcert": {caFile}}
	postgresURL := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(config.Username, config.Password),
		Host:     net.JoinHostPort(address.String(), port),
		Path:     "/" + database,
		RawPath:  "/" + url.PathEscape(database),
		RawQuery: query.Encode(),
	}
	return postgresURL.String(), nil
}

func readNacosJSON(client *http.Client, request *http.Request, out any) bool {
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, nacosResponseLimit+1))
	return err == nil && len(body) <= nacosResponseLimit && json.Unmarshal(body, out) == nil
}
