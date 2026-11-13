package gateway

import (
	"context"
	v02 "github.com/xielforever/MCPGuard/internal/contracts/v02"
	"github.com/xielforever/MCPGuard/internal/resources"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (g *Gateway) AttachContexts(s *resources.ContextService) { g.Contexts = s }
func privateContextPath(path string) bool {
	return path == "/api/v2/credential-contexts" || strings.HasPrefix(path, "/api/v2/credential-contexts/") || strings.HasPrefix(path, "/api/v2/credentials/") && strings.HasSuffix(path, "/renewals")
}
func contextQueueInput(raw string) (v02.ListDownloadsInput, error) {
	var in v02.ListDownloadsInput
	query, e := url.ParseQuery(raw)
	if e != nil {
		return in, resources.ErrInput
	}
	for key, values := range query {
		if len(values) != 1 {
			return in, resources.ErrInput
		}
		switch key {
		case "after":
			in.After = &values[0]
		case "limit":
			n, e := strconv.Atoi(values[0])
			if e != nil {
				return in, resources.ErrInput
			}
			in.Limit = &n
		default:
			return in, resources.ErrInput
		}
	}
	return in, nil
}
func (g *Gateway) contextHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	p, ok := requestPrincipal(r)
	if !ok {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" || len(r.Header.Values("Authorization")) != 1 {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	call, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	stop := context.AfterFunc(g.Root, cancel)
	defer stop()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		writeResourceHTTP(w, nil, resources.ErrUnavailable)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/api/v2/credential-contexts" {
		if r.URL.RawQuery != "" || len(r.Header.Values("X-MCPGuard-Delivery-Token")) != 1 || len(r.Header.Values(resources.ContextHeader)) > 0 {
			writeResourceHTTP(w, nil, resources.ErrDenied)
			return
		}
		var in v02.ContextCreateInput
		if e := accessBody(w, r, "ContextCreateInput", &in); e != nil {
			writeResourceHTTP(w, nil, e)
			return
		}
		out, e := g.Contexts.Create(call, p, r.Header.Get("X-MCPGuard-Delivery-Token"), in)
		writeResourceHTTP(w, out, e)
		return
	}
	if len(r.Header.Values(resources.ContextHeader)) != 1 {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	proof, e := resources.ParseContextProof(r.Header.Get(resources.ContextHeader))
	if e != nil {
		writeResourceHTTP(w, nil, e)
		return
	}
	if token := r.Header.Get("X-MCPGuard-Delivery-Token"); token != "" {
		if len(r.Header.Values("X-MCPGuard-Delivery-Token")) != 1 {
			writeResourceHTTP(w, nil, resources.ErrDenied)
			return
		}
		if e = g.Contexts.VerifyDelivery(call, p, proof, token); e != nil {
			writeResourceHTTP(w, nil, e)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/api/v2/credential-contexts/") && r.PathValue("id") != proof.ContextID() {
		writeResourceHTTP(w, nil, resources.ErrDenied)
		return
	}
	var out any
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/credential-downloads":
		var in v02.ListDownloadsInput
		in, e = contextQueueInput(r.URL.RawQuery)
		if e == nil {
			out, e = g.Contexts.Queue(call, p, proof, in)
		}
	case r.URL.RawQuery != "":
		e = resources.ErrInput
	case r.Method == http.MethodGet:
		if r.ContentLength != 0 {
			e = resources.ErrInput
		} else {
			out, e = g.Contexts.Status(call, p, proof)
		}
	case strings.HasSuffix(r.URL.Path, "/activations"):
		var in v02.ActivationInput
		e = accessBody(w, r, "ActivationInput", &in)
		if e == nil {
			out, e = g.Contexts.Activate(call, p, proof, in)
		}
	case strings.HasSuffix(r.URL.Path, "/deactivations"):
		var in v02.DeactivateInput
		e = accessBody(w, r, "DeactivateInput", &in)
		if e == nil {
			out, e = g.Contexts.Deactivate(call, p, proof, in)
		}
	case strings.HasSuffix(r.URL.Path, "/replacements"):
		var in v02.ReplaceDownloadInput
		e = accessBody(w, r, "ReplaceDownloadInput", &in)
		if e == nil {
			out, e = g.Contexts.Replace(call, p, proof, r.PathValue("id"), in)
		}
	case strings.HasPrefix(r.URL.Path, "/api/v2/credentials/"):
		var in v02.RenewCredentialInput
		e = accessBody(w, r, "RenewCredentialInput", &in)
		if e == nil && in.CredentialID != r.PathValue("id") {
			e = resources.ErrInput
		}
		if e == nil {
			out, e = g.Contexts.RenewCredential(call, p, proof, in)
		}
	default:
		var in v02.ContextRenewInput
		e = accessBody(w, r, "ContextRenewInput", &in)
		if e == nil {
			out, e = g.Contexts.Renew(call, p, proof, in, r.Method == http.MethodDelete)
		}
	}
	writeResourceHTTP(w, out, e)
}
