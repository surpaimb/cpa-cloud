package service

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cpacloud.local/server/internal/financial"
)

const billingV1MaxBody = 64 << 10

func (a *App) registerBillingV1Handlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/billing/balances", a.requireAdmin(a.billingV1Balance, false))
	mux.HandleFunc("GET /admin/api/v1/billing/entries", a.requireAdmin(a.billingV1Entries, false))
	mux.HandleFunc("POST /admin/api/v1/billing/adjustments", a.requireAdmin(a.billingV1Adjustment, true))
	a.registerBillingCommercialHandlers(mux)
}

func (a *App) billingV1Balance(w http.ResponseWriter, r *http.Request, _ adminSession) {
	owner, currency, ok := parseBillingOwnerQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	balance, err := financial.NewLedger(a.store.db).Balance(r.Context(), owner, currency)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, billingBalanceView(balance))
}

func (a *App) billingV1Entries(w http.ResponseWriter, r *http.Request, _ adminSession) {
	filter, ok := parseBillingEntriesQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	page, err := financial.NewLedger(a.store.db).ListEntries(r.Context(), filter)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, billingEntryView(item))
	}
	var next any
	if page.NextCursor != "" {
		next = page.NextCursor
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (a *App) billingV1Adjustment(w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, billingV1MaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "owner", "currency", "amount_micro") {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	currency, currencyOK := object["currency"].(string)
	amountText, amountOK := object["amount_micro"].(string)
	ownerObject, ownerOK := object["owner"].(map[string]any)
	owner, ownerValid := parseBillingOwnerObject(ownerObject)
	amount, parseErr := strconv.ParseInt(amountText, 10, 64)
	if !operationOK || !validGovernanceOperationID(operationID) || !currencyOK || !amountOK || !ownerOK || !ownerValid || parseErr != nil || amount == 0 || strconv.FormatInt(amount, 10) != amountText {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	kind := financial.EntryAdjustmentCredit
	if amount < 0 {
		kind = financial.EntryAdjustmentDebit
	}
	posted, err := financial.NewLedger(a.store.db).Post(r.Context(), financial.Post{
		OperationID: operationID, Action: "adjustment", ActorAdminID: session.AdminID,
		ResourceKind: "adjustment", ResourceID: operationID, ObservedAt: time.Now().UTC(), RequireNonNegative: true,
		Entries: []financial.EntryInput{{Owner: owner, Currency: currency, Kind: kind, AmountMicro: amount, ResourceKind: "adjustment", ResourceID: operationID}},
	})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	if len(posted) != 1 {
		writeBillingV1Error(w, financial.ErrUnavailable)
		return
	}
	balance, err := financial.NewLedger(a.store.db).Balance(r.Context(), owner, currency)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operation_id": operationID, "entry_id": posted[0].ID, "owner": billingOwnerView(owner),
		"currency": currency, "amount_micro": amountText, "balance_micro": strconv.FormatInt(balance.AmountMicro, 10),
	})
}

func parseBillingOwnerQuery(r *http.Request) (financial.Owner, string, bool) {
	query := r.URL.Query()
	allowed := map[string]bool{"owner_kind": true, "employee_id": true, "key_id": true, "resource_kind": true, "resource_id": true, "currency": true}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 {
			return financial.Owner{}, "", false
		}
	}
	owner := financial.Owner{Kind: financial.OwnerKind(query.Get("owner_kind")), EmployeeID: query.Get("employee_id"), KeyID: query.Get("key_id"), ResourceKind: query.Get("resource_kind"), ResourceID: query.Get("resource_id")}
	currency := query.Get("currency")
	return owner, currency, owner.Kind != "" && owner.EmployeeID != "" && currency != ""
}

func parseBillingEntriesQuery(r *http.Request) (financial.EntryFilter, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return financial.EntryFilter{}, false
	}
	allowed := map[string]bool{
		"owner_kind": true, "employee_id": true, "key_id": true,
		"resource_kind": true, "resource_id": true, "account_id": true,
		"currency": true, "after_id": true, "limit": true,
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			return financial.EntryFilter{}, false
		}
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 || strconv.Itoa(parsed) != raw {
			return financial.EntryFilter{}, false
		}
		limit = parsed
	}
	filter := financial.EntryFilter{
		OwnerKind: financial.OwnerKind(query.Get("owner_kind")), EmployeeID: query.Get("employee_id"),
		KeyID: query.Get("key_id"), ResourceKind: query.Get("resource_kind"), ResourceID: query.Get("resource_id"),
		AccountID: query.Get("account_id"), Currency: query.Get("currency"), AfterID: query.Get("after_id"), Limit: limit,
	}
	return filter, true
}

func parseBillingOwnerObject(object map[string]any) (financial.Owner, bool) {
	if object == nil {
		return financial.Owner{}, false
	}
	allowed := map[string]bool{"kind": true, "employee_id": true, "key_id": true, "resource_kind": true, "resource_id": true}
	for key := range object {
		if !allowed[key] {
			return financial.Owner{}, false
		}
	}
	kind, kindOK := object["kind"].(string)
	employeeID, employeeOK := object["employee_id"].(string)
	if !kindOK || !employeeOK {
		return financial.Owner{}, false
	}
	owner := financial.Owner{Kind: financial.OwnerKind(kind), EmployeeID: employeeID}
	for key, destination := range map[string]*string{"key_id": &owner.KeyID, "resource_kind": &owner.ResourceKind, "resource_id": &owner.ResourceID} {
		if raw, present := object[key]; present {
			value, ok := raw.(string)
			if !ok {
				return financial.Owner{}, false
			}
			*destination = value
		}
	}
	return owner, true
}

func billingBalanceView(balance financial.Balance) map[string]any {
	accountID := any(nil)
	if balance.AccountID != "" {
		accountID = balance.AccountID
	}
	return map[string]any{"account_id": accountID, "owner": billingOwnerView(balance.Owner), "currency": balance.Currency, "balance_micro": strconv.FormatInt(balance.AmountMicro, 10)}
}

func billingOwnerView(owner financial.Owner) map[string]any {
	keyID, resourceKind, resourceID := any(nil), any(nil), any(nil)
	if owner.KeyID != "" {
		keyID = owner.KeyID
	}
	if owner.ResourceKind != "" {
		resourceKind = owner.ResourceKind
	}
	if owner.ResourceID != "" {
		resourceID = owner.ResourceID
	}
	return map[string]any{"kind": owner.Kind, "employee_id": owner.EmployeeID, "key_id": keyID, "resource_kind": resourceKind, "resource_id": resourceID}
}

func billingEntryView(item financial.Entry) map[string]any {
	original := any(nil)
	if item.OriginalEntryID != "" {
		original = item.OriginalEntryID
	}
	return map[string]any{
		"id": item.ID, "operation_id": item.OperationID, "account_id": item.AccountID,
		"owner": billingOwnerView(item.Owner), "currency": item.Currency, "kind": item.Kind,
		"amount_micro": strconv.FormatInt(item.AmountMicro, 10), "original_entry_id": original,
		"resource_kind": item.ResourceKind, "resource_id": item.ResourceID,
		"created_at": item.CreatedAt.Format(time.RFC3339Nano),
	}
}

func writeBillingV1Error(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, financial.ErrInvalid):
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid billing request.")
	case errors.Is(err, financial.ErrNotFound):
		writeAdminError(w, http.StatusNotFound, "not_found", "The billing owner or entry was not found.")
	case errors.Is(err, financial.ErrConflict):
		writeAdminError(w, http.StatusConflict, "operation_conflict", "The operation identifier or billing ownership conflicts with existing data.")
	case errors.Is(err, financial.ErrInsufficient):
		writeAdminError(w, http.StatusConflict, "insufficient_balance", "The operation would make the balance negative.")
	default:
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Billing data is temporarily unavailable.")
	}
}
