package service

// Independently authored from outbound-proxy-admin-contract.md. DTOs expose
// neither encrypted authentication nor its operation fingerprint.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type outboundProxyDTO struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Scheme             string    `json:"scheme"`
	Host               string    `json:"host"`
	Port               int       `json:"port"`
	AddressScope       string    `json:"address_scope"`
	Enabled            bool      `json:"enabled"`
	Revision           int64     `json:"revision"`
	ConnectionRevision int64     `json:"connection_revision"`
	HasCredentials     bool      `json:"has_credentials"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func proxyDTO(value outboundProxyView) outboundProxyDTO {
	return outboundProxyDTO{value.ID, value.Name, value.Scheme, value.Host, value.Port, value.AddressScope, value.Enabled, value.Revision, value.ConnectionRevision, value.HasCredentials, value.CreatedAt, value.UpdatedAt}
}

type proxyCredentialRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func proxyCredentialInput(value *proxyCredentialRequest) *outboundProxyCredential {
	if value == nil {
		return nil
	}
	return &outboundProxyCredential{username: value.Username, password: value.Password}
}

type upstreamProxyDTO struct {
	UpstreamID       string           `json:"upstream_id"`
	UpstreamRevision int64            `json:"upstream_revision"`
	Binding          *proxyBindingDTO `json:"binding"`
}
type proxyBindingDTO struct {
	ProxyID            string `json:"proxy_id"`
	ProxyRevision      int64  `json:"proxy_revision"`
	ConnectionRevision int64  `json:"connection_revision"`
	Enabled            bool   `json:"enabled"`
	Name               string `json:"name"`
}

func (a *App) registerOutboundProxyHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/outbound-proxies", a.requireAdmin(a.listOutboundProxies, false))
	mux.HandleFunc("POST /admin/api/v1/outbound-proxies", a.requireAdmin(a.createOutboundProxy, true))
	mux.HandleFunc("GET /admin/api/v1/outbound-proxies/{id}", a.requireAdmin(a.getOutboundProxy, false))
	mux.HandleFunc("PATCH /admin/api/v1/outbound-proxies/{id}", a.requireAdmin(a.updateOutboundProxy, true))
	// A generic final segment leaves the existing OAuth session prefix more
	// specific; two crossing wildcards would panic in Go ServeMux.
	mux.HandleFunc("GET /admin/api/v1/upstreams/{id}/{resource}", a.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		if r.PathValue("resource") == "prices" {
			r.SetPathValue("price_action", "prices")
			a.listUpstreamPrices(w, r, session)
			return
		}
		if r.PathValue("resource") != "proxy" {
			writeAdminError(w, 404, "not_found", "The requested object was not found.")
			return
		}
		a.getUpstreamProxy(w, r, session)
	}, false))
	mux.HandleFunc("PUT /admin/api/v1/upstreams/{id}/proxy", a.requireAdmin(a.setUpstreamProxy, true))
}

func (a *App) listOutboundProxies(w http.ResponseWriter, r *http.Request, _ adminSession) {
	values, err := parseProxyListQuery(r)
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	items, err := a.outboundProxies.List(r.Context(), values.after, values.limit)
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	result := make([]outboundProxyDTO, 0, len(items))
	for _, item := range items {
		result = append(result, proxyDTO(item))
	}
	var next *string
	if len(items) == values.limit {
		last := items[len(items)-1].ID
		next = &last
	}
	writeJSON(w, 200, map[string]any{"items": result, "next_cursor": next})
}

type proxyListQuery struct {
	after string
	limit int
}

func parseProxyListQuery(r *http.Request) (proxyListQuery, error) {
	result := proxyListQuery{limit: 50}
	if len(r.URL.RawQuery) > 2048 {
		return result, errOutboundProxyInvalid
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return result, errOutboundProxyInvalid
	}
	for key, v := range values {
		if len(v) != 1 || key != "after_id" && key != "limit" {
			return result, errOutboundProxyInvalid
		}
		if key == "after_id" {
			result.after = v[0]
			if result.after != "" && !validIdentifier(result.after, 128) {
				return result, errOutboundProxyInvalid
			}
		}
		if key == "limit" {
			parsed, err := strconv.Atoi(v[0])
			if err != nil || parsed < 1 || parsed > 100 {
				return result, errOutboundProxyInvalid
			}
			result.limit = parsed
		}
	}
	return result, nil
}

func (a *App) getOutboundProxy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	item, err := a.outboundProxies.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	writeJSON(w, 200, proxyDTO(item))
}

func (a *App) createOutboundProxy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var body struct {
		OperationID  string                  `json:"operation_id"`
		Name         string                  `json:"name"`
		Scheme       string                  `json:"scheme"`
		Host         string                  `json:"host"`
		Port         int                     `json:"port"`
		AddressScope string                  `json:"address_scope"`
		Enabled      *bool                   `json:"enabled"`
		Credentials  *proxyCredentialRequest `json:"credentials"`
	}
	if !decodeJSON(w, r, 16<<10, &body) {
		return
	}
	if body.Enabled == nil {
		writeProxyAdminError(w, errOutboundProxyInvalid)
		return
	}
	item, err := a.outboundProxies.Create(r.Context(), outboundProxyCreateInput{OperationID: body.OperationID, Name: body.Name, Scheme: body.Scheme, Host: body.Host, Port: body.Port, AddressScope: body.AddressScope, Enabled: *body.Enabled, Credential: proxyCredentialInput(body.Credentials)})
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	writeJSON(w, 200, proxyDTO(item))
}

func (a *App) updateOutboundProxy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var body struct {
		ExpectedRevision int64                   `json:"expected_revision"`
		Name             string                  `json:"name"`
		Scheme           string                  `json:"scheme"`
		Host             string                  `json:"host"`
		Port             int                     `json:"port"`
		AddressScope     string                  `json:"address_scope"`
		Enabled          *bool                   `json:"enabled"`
		CredentialMode   string                  `json:"credential_mode"`
		Credentials      *proxyCredentialRequest `json:"credentials"`
	}
	if !decodeJSON(w, r, 16<<10, &body) {
		return
	}
	if body.Enabled == nil {
		writeProxyAdminError(w, errOutboundProxyInvalid)
		return
	}
	a.admission.Lock()
	result, err := a.outboundProxies.Update(r.Context(), outboundProxyUpdateInput{ID: r.PathValue("id"), ExpectedRevision: body.ExpectedRevision, Name: body.Name, Scheme: body.Scheme, Host: body.Host, Port: body.Port, AddressScope: body.AddressScope, Enabled: *body.Enabled, CredentialMode: body.CredentialMode, Credential: proxyCredentialInput(body.Credentials)})
	if err != nil || result.ConnectionChanged {
		a.proxyClients.Invalidate(r.PathValue("id"))
	}
	a.admission.Unlock()
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	if result.ConnectionChanged {
		a.notifyAccountPoolChanged()
	}
	writeJSON(w, 200, proxyDTO(result.View))
}

func (a *App) upstreamProxyView(ctx context.Context, id string) (upstreamProxyDTO, error) {
	result := upstreamProxyDTO{UpstreamID: id}
	if !validIdentifier(id, 128) {
		return result, errOutboundProxyInvalid
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM upstreams WHERE id=?`, id).Scan(&result.UpstreamRevision); errors.Is(err, sql.ErrNoRows) {
		return result, errOutboundProxyNotFound
	} else if err != nil {
		return result, err
	}
	var bound proxyBindingDTO
	var enabled int
	err = tx.QueryRowContext(ctx, `SELECT b.proxy_id,p.revision,p.connection_revision,p.enabled,p.name FROM upstream_proxy_bindings b JOIN outbound_proxies p ON p.id=b.proxy_id WHERE b.upstream_id=?`, id).Scan(&bound.ProxyID, &bound.ProxyRevision, &bound.ConnectionRevision, &enabled, &bound.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	bound.Enabled = enabled == 1
	result.Binding = &bound
	return result, nil
}

func (a *App) getUpstreamProxy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	result, err := a.upstreamProxyView(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func (a *App) setUpstreamProxy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var body struct {
		ExpectedUpstreamRevision int64  `json:"expected_upstream_revision"`
		ProxyID                  string `json:"proxy_id"`
		ExpectedProxyRevision    int64  `json:"expected_proxy_revision"`
		Bind                     *bool  `json:"bind"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if body.Bind == nil || body.ExpectedUpstreamRevision < 1 || body.ExpectedUpstreamRevision > outboundProxyMaxRevision || body.ExpectedProxyRevision < 1 || body.ExpectedProxyRevision > outboundProxyMaxRevision {
		writeProxyAdminError(w, errOutboundProxyInvalid)
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	var provider, endpoint string
	err := a.store.db.QueryRowContext(r.Context(), `SELECT provider_kind,endpoint FROM upstreams WHERE id=?`, r.PathValue("id")).Scan(&provider, &endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		writeProxyAdminError(w, errOutboundProxyNotFound)
		return
	}
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	if !validProxyBindingProvider(provider) || !validHTTPSUpstreamEndpoint(endpoint) {
		writeAdminError(w, 400, "unsupported_proxy_binding", "This upstream cannot use an outbound proxy.")
		return
	}
	_, err = a.outboundProxies.SetBinding(r.Context(), upstreamProxyBindingInput{UpstreamID: r.PathValue("id"), ExpectedUpstreamRevision: body.ExpectedUpstreamRevision, ProxyID: body.ProxyID, ExpectedProxyRevision: body.ExpectedProxyRevision, Bind: *body.Bind})
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	a.notifyAccountPoolChanged()
	result, err := a.upstreamProxyView(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProxyAdminError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func writeProxyAdminError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errOutboundProxyInvalid):
		writeAdminError(w, 400, "invalid_proxy", "Invalid outbound proxy configuration.")
	case errors.Is(err, errOutboundProxyNotFound):
		writeAdminError(w, 404, "not_found", "The requested object was not found.")
	case errors.Is(err, errOutboundProxyConflict), errors.Is(err, errOutboundProxyRevisionOverflow):
		writeAdminError(w, 409, "revision_conflict", "The configuration changed; reload before editing.")
	case errors.Is(err, errOutboundProxyOperationConflict):
		writeAdminError(w, 409, "operation_conflict", "The operation identifier already belongs to different input.")
	case errors.Is(err, errUpstreamProxyBindingConflict):
		writeAdminError(w, 409, "binding_conflict", "The upstream binding changed; reload before editing.")
	case errors.Is(err, errOutboundProxyUnavailable):
		writeAdminError(w, 503, "proxy_unavailable", "The configured outbound proxy is unavailable.")
	default:
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
	}
}
