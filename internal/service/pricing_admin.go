package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"cpacloud.local/server/internal/accounting"
)

type priceRateView struct {
	Currency                  string `json:"currency"`
	InputPerMillionMicro      string `json:"input_per_million_micro"`
	OutputPerMillionMicro     string `json:"output_per_million_micro"`
	CacheReadPerMillionMicro  string `json:"cache_read_per_million_micro"`
	CacheWritePerMillionMicro string `json:"cache_write_per_million_micro"`
}

type priceVersionView struct {
	UpstreamID    string         `json:"upstream_id"`
	UpstreamModel string         `json:"upstream_model"`
	Version       string         `json:"version"`
	Revision      int64          `json:"revision"`
	CreatedAt     string         `json:"created_at"`
	Price         *priceRateView `json:"price"`
}

// registerPricingHandlers is separate so App startup can migrate the catalog
// before exposing either management or model routes.
func (a *App) registerPricingHandlers(mux *http.ServeMux) {
	// The final wildcard keeps the existing literal codex-oauth-sessions route
	// strictly more specific under Go 1.22 ServeMux precedence rules.
	mux.HandleFunc("GET /admin/api/v1/upstreams/{id}/{price_action}", a.requireAdmin(a.listUpstreamPrices, false))
	mux.HandleFunc("POST /admin/api/v1/upstreams/{id}/prices", a.requireAdmin(a.saveUpstreamPrice, true))
}

func (a *App) listUpstreamPrices(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.PathValue("price_action") != "prices" {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		writePricingInvalid(w)
		return
	}
	catalog := accounting.NewPriceCatalog(a.store.db)
	items, err := catalog.List(r.Context(), r.PathValue("id"))
	if err != nil {
		writePricingError(w, err)
		return
	}
	views := make([]priceVersionView, 0, len(items))
	for _, item := range items {
		views = append(views, priceVersionResponse(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

func (a *App) saveUpstreamPrice(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" {
		writePricingInvalid(w)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil {
		writePricingInvalid(w)
		return
	}
	if !exactJSONKeys(object, "operation_id", "expected_revision", "upstream_model", "price") {
		writePricingInvalid(w)
		return
	}
	operationID, ok := object["operation_id"].(string)
	if !ok {
		writePricingInvalid(w)
		return
	}
	actualModel, ok := object["upstream_model"].(string)
	if !ok {
		writePricingInvalid(w)
		return
	}
	expectedRevision, ok := parseSafeJSONInteger(object["expected_revision"])
	if !ok {
		writePricingInvalid(w)
		return
	}
	price, ok := parsePriceRequest(object["price"])
	if !ok {
		writePricingInvalid(w)
		return
	}
	catalog := accounting.NewPriceCatalog(a.store.db)
	item, err := catalog.Save(r.Context(), accounting.PriceSave{
		AccountID: r.PathValue("id"), ActualModel: actualModel, OperationID: operationID,
		ExpectedRevision: expectedRevision, Price: price,
	})
	if err != nil {
		writePricingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, priceVersionResponse(item))
}

func parsePriceRequest(value any) (*accounting.PriceSnapshot, bool) {
	if value == nil {
		return nil, true
	}
	object, ok := value.(map[string]any)
	if !ok || !exactJSONKeys(object, "currency", "input_per_million_micro", "output_per_million_micro", "cache_read_per_million_micro", "cache_write_per_million_micro") {
		return nil, false
	}
	currency, ok := object["currency"].(string)
	if !ok {
		return nil, false
	}
	values := make([]int64, 4)
	for index, name := range []string{"input_per_million_micro", "output_per_million_micro", "cache_read_per_million_micro", "cache_write_per_million_micro"} {
		text, ok := object[name].(string)
		if !ok {
			return nil, false
		}
		values[index], ok = parseCanonicalPriceRate(text)
		if !ok {
			return nil, false
		}
	}
	return &accounting.PriceSnapshot{Currency: currency, InputPerMillionMicro: values[0], OutputPerMillionMicro: values[1], CacheReadPerMillionMicro: values[2], CacheWritePerMillionMicro: values[3]}, true
}

func parseCanonicalPriceRate(value string) (int64, bool) {
	if value == "" || value != "0" && value[0] == '0' {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil && parsed <= accounting.MaxPriceRate
}

func parseSafeJSONInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	text := number.String()
	if text == "" || text != "0" && text[0] == '0' {
		return 0, false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	return parsed, err == nil && parsed <= accounting.MaxPriceRate
}

func priceVersionResponse(item accounting.PriceVersion) priceVersionView {
	view := priceVersionView{UpstreamID: item.AccountID, UpstreamModel: item.ActualModel, Version: item.Version,
		Revision: item.Revision, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano)}
	if item.Price != nil {
		view.Price = &priceRateView{Currency: item.Price.Currency,
			InputPerMillionMicro:      strconv.FormatInt(item.Price.InputPerMillionMicro, 10),
			OutputPerMillionMicro:     strconv.FormatInt(item.Price.OutputPerMillionMicro, 10),
			CacheReadPerMillionMicro:  strconv.FormatInt(item.Price.CacheReadPerMillionMicro, 10),
			CacheWritePerMillionMicro: strconv.FormatInt(item.Price.CacheWritePerMillionMicro, 10)}
	}
	return view
}

func writePricingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounting.ErrInvalid):
		writePricingInvalid(w)
	case errors.Is(err, accounting.ErrNotFound):
		writeAdminError(w, http.StatusNotFound, "not_found", "Upstream was not found.")
	case errors.Is(err, accounting.ErrPriceRevisionConflict):
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The price configuration was changed by another request.")
	case errors.Is(err, accounting.ErrPriceOperationConflict):
		writeAdminError(w, http.StatusConflict, "operation_conflict", "The operation was already used with different input.")
	case errors.Is(err, accounting.ErrPriceListLimit):
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Too many price configurations to return.")
	default:
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
	}
}

func writePricingInvalid(w http.ResponseWriter) {
	writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid price request.")
}

func exactJSONKeys(object map[string]any, names ...string) bool {
	if len(object) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}

func decodeUniqueJSONObject(w http.ResponseWriter, r *http.Request, max int64) (map[string]any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	body, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(body) {
		return nil, errors.New("invalid JSON encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decodeUniqueJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("JSON object required")
	}
	return object, nil
}

func decodeUniqueJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("invalid JSON object key")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("duplicate JSON key")
			}
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errors.New("invalid JSON object")
		}
		return object, nil
	case '[':
		values := make([]any, 0)
		for decoder.More() {
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errors.New("invalid JSON array")
		}
		return values, nil
	default:
		return nil, errors.New("invalid JSON delimiter")
	}
}
