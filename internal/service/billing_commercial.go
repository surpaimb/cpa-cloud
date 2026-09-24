package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"cpacloud.local/server/internal/financial"
)

const billingWebhookPurpose = "billing-redemption/v1"

func (a *App) registerBillingCommercialHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/billing/settings", a.requireAdmin(a.billingCommercialSettings, false))
	mux.HandleFunc("PUT /admin/api/v1/billing/settings", a.requireAdmin(a.billingCommercialUpdateSettings, true))
	mux.HandleFunc("POST /admin/api/v1/billing/plans", a.requireAdmin(a.billingCommercialCreatePlan, true))
	mux.HandleFunc("GET /admin/api/v1/billing/plans", a.requireAdmin(a.billingCommercialListPlans, false))
	mux.HandleFunc("PUT /admin/api/v1/billing/plans/{id}", a.requireAdmin(a.billingCommercialUpdatePlan, true))
	mux.HandleFunc("POST /admin/api/v1/billing/payment-connectors", a.requireAdmin(a.billingCommercialCreateConnector, true))
	mux.HandleFunc("GET /admin/api/v1/billing/payment-connectors", a.requireAdmin(a.billingCommercialListConnectors, false))
	mux.HandleFunc("PUT /admin/api/v1/billing/payment-connectors/{id}", a.requireAdmin(a.billingCommercialUpdateConnector, true))
	mux.HandleFunc("POST /admin/api/v1/billing/topups", a.requireAdmin(a.billingCommercialCreateTopUp, true))
	mux.HandleFunc("GET /admin/api/v1/billing/topups", a.requireAdmin(a.billingCommercialListTopUps, false))
	mux.HandleFunc("POST /admin/api/v1/billing/subscriptions", a.requireAdmin(a.billingCommercialSubscribe, true))
	mux.HandleFunc("GET /admin/api/v1/billing/subscriptions", a.requireAdmin(a.billingCommercialListSubscriptions, false))
	mux.HandleFunc("POST /admin/api/v1/billing/subscriptions/{id}/cancel", a.requireAdmin(a.billingCommercialCancelSubscription, true))
	mux.HandleFunc("POST /admin/api/v1/billing/redemption-codes", a.requireAdmin(a.billingCommercialCreateCode, true))
	mux.HandleFunc("GET /admin/api/v1/billing/redemption-codes", a.requireAdmin(a.billingCommercialListCodes, false))
	mux.HandleFunc("POST /admin/api/v1/billing/redemptions", a.requireAdmin(a.billingCommercialRedeem, true))
	mux.HandleFunc("POST /admin/api/v1/billing/refunds", a.requireAdmin(a.billingCommercialRefund, true))
	mux.HandleFunc("GET /admin/api/v1/billing/refunds", a.requireAdmin(a.billingCommercialListRefunds, false))
	mux.HandleFunc("POST /admin/api/v1/billing/payment-callbacks/{connector_id}", a.billingCommercialPaymentCallback)
}

func (a *App) billingCommercialListPlans(w http.ResponseWriter, r *http.Request, _ adminSession) {
	after, limit, ok := billingListQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	items, err := financial.NewCommercial(a.store.db).ListPlans(r.Context(), after, limit)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, billingPlanView(item))
	}
	writeJSON(w, http.StatusOK, billingListView(views, limit))
}
func (a *App) billingCommercialListConnectors(w http.ResponseWriter, r *http.Request, _ adminSession) {
	after, limit, ok := billingListQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	items, err := financial.NewCommercial(a.store.db).ListConnectors(r.Context(), after, limit)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, billingConnectorView(item))
	}
	writeJSON(w, http.StatusOK, billingListView(views, limit))
}
func (a *App) billingCommercialListTopUps(w http.ResponseWriter, r *http.Request, _ adminSession) {
	after, limit, ok := billingListQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	items, err := financial.NewCommercial(a.store.db).ListTopUps(r.Context(), after, limit)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, billingTopUpView(item))
	}
	writeJSON(w, http.StatusOK, billingListView(views, limit))
}
func (a *App) billingCommercialListSubscriptions(w http.ResponseWriter, r *http.Request, _ adminSession) {
	after, limit, ok := billingListQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	items, err := financial.NewCommercial(a.store.db).ListSubscriptions(r.Context(), after, limit)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, billingSubscriptionView(item))
	}
	writeJSON(w, http.StatusOK, billingListView(views, limit))
}
func (a *App) billingCommercialListCodes(w http.ResponseWriter, r *http.Request, _ adminSession) {
	after, limit, ok := billingListQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	items, err := financial.NewCommercial(a.store.db).ListCodes(r.Context(), after, limit)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, billingCodeView(item))
	}
	writeJSON(w, http.StatusOK, billingListView(views, limit))
}
func (a *App) billingCommercialListRefunds(w http.ResponseWriter, r *http.Request, _ adminSession) {
	after, limit, ok := billingListQuery(r)
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	items, err := financial.NewCommercial(a.store.db).ListRefunds(r.Context(), after, limit)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, map[string]any{"id": item.ID, "payment_id": item.PaymentID, "entry_id": item.EntryID, "amount_micro": strconv.FormatInt(item.AmountMicro, 10), "created_at": item.CreatedAt.Format(time.RFC3339Nano)})
	}
	writeJSON(w, http.StatusOK, billingListView(views, limit))
}

func (a *App) billingCommercialSettings(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	enabled, revision, err := financial.NewCommercial(a.store.db).Settings(r.Context())
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled, "revision": revision})
}

func (a *App) billingCommercialUpdateSettings(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "expected_revision", "enabled")
	if !ok {
		return
	}
	expected, expectedOK := parseGovernanceJSONRevision(object["expected_revision"])
	enabled, enabledOK := object["enabled"].(bool)
	if !expectedOK || !enabledOK {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	digest, _ := financial.DigestPayload(struct {
		Expected int64 `json:"expected_revision"`
		Enabled  bool  `json:"enabled"`
	}{expected, enabled})
	receipt, err := financial.NewCommercial(a.store.db).SetEnabled(r.Context(), billingWriteMeta(operationID, session.AdminID, digest), expected, enabled)
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, billingReceiptView(receipt))
}

func (a *App) billingCommercialCreatePlan(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "name", "currency", "price_micro", "credit_micro", "interval", "enabled")
	if !ok {
		return
	}
	name, nok := object["name"].(string)
	currency, cok := object["currency"].(string)
	price, pok := billingPositiveString(object["price_micro"])
	credit, gok := billingPositiveString(object["credit_micro"])
	interval, iok := object["interval"].(string)
	enabled, eok := object["enabled"].(bool)
	if !nok || !cok || !pok || !gok || !iok || !eok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	payload := struct {
		Name, Currency, Price, Credit, Interval string
		Enabled                                 bool
	}{name, currency, strconv.FormatInt(price, 10), strconv.FormatInt(credit, 10), interval, enabled}
	digest, _ := financial.DigestPayload(payload)
	plan, receipt, err := financial.NewCommercial(a.store.db).CreatePlan(r.Context(), financial.CreatePlan{Meta: billingWriteMeta(operationID, session.AdminID, digest), Name: name, Currency: currency, PriceMicro: price, CreditMicro: credit, Interval: interval, Enabled: enabled})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "plan": billingPlanView(plan)})
}

func (a *App) billingCommercialUpdatePlan(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "expected_revision", "name", "currency", "price_micro", "credit_micro", "interval", "enabled")
	if !ok {
		return
	}
	expected, xok := parseGovernanceJSONRevision(object["expected_revision"])
	name, nok := object["name"].(string)
	currency, cok := object["currency"].(string)
	price, pok := billingPositiveString(object["price_micro"])
	credit, gok := billingPositiveString(object["credit_micro"])
	interval, iok := object["interval"].(string)
	enabled, eok := object["enabled"].(bool)
	if !xok || !nok || !cok || !pok || !gok || !iok || !eok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	payload := struct {
		ID, Name, Currency, Price, Credit, Interval string
		Expected                                    int64
		Enabled                                     bool
	}{r.PathValue("id"), name, currency, strconv.FormatInt(price, 10), strconv.FormatInt(credit, 10), interval, expected, enabled}
	digest, _ := financial.DigestPayload(payload)
	plan, receipt, err := financial.NewCommercial(a.store.db).UpdatePlan(r.Context(), financial.UpdatePlan{Meta: billingWriteMeta(operationID, session.AdminID, digest), ID: r.PathValue("id"), Name: name, Currency: currency, PriceMicro: price, CreditMicro: credit, Interval: interval, Enabled: enabled, ExpectedRevision: expected})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "plan": billingPlanView(plan)})
}

func (a *App) billingCommercialCreateConnector(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "name", "webhook_secret", "enabled")
	if !ok {
		return
	}
	name, nok := object["name"].(string)
	secret, sok := object["webhook_secret"].(string)
	enabled, eok := object["enabled"].(bool)
	if !nok || !sok || !eok || len(secret) < 32 || len(secret) > 256 {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	fingerprint := sha256.Sum256([]byte(secret))
	digest, _ := financial.DigestPayload(struct {
		Name, Fingerprint string
		Enabled           bool
	}{name, hex.EncodeToString(fingerprint[:]), enabled})
	ciphertext, err := a.encryptBillingWebhookSecret(secret)
	if err != nil {
		writeBillingV1Error(w, financial.ErrUnavailable)
		return
	}
	connector, receipt, err := financial.NewCommercial(a.store.db).CreateConnector(r.Context(), financial.CreateConnector{Meta: billingWriteMeta(operationID, session.AdminID, digest), Name: name, SecretCiphertext: ciphertext, Enabled: enabled})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "connector": billingConnectorView(connector)})
}

func (a *App) billingCommercialUpdateConnector(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "expected_revision", "name", "webhook_secret", "enabled")
	if !ok {
		return
	}
	expected, xok := parseGovernanceJSONRevision(object["expected_revision"])
	name, nok := object["name"].(string)
	enabled, eok := object["enabled"].(bool)
	var secret string
	if object["webhook_secret"] != nil {
		var sok bool
		secret, sok = object["webhook_secret"].(string)
		if !sok || len(secret) < 32 || len(secret) > 256 {
			writeBillingV1Error(w, financial.ErrInvalid)
			return
		}
	}
	if !xok || !nok || !eok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	fingerprint := ""
	var ciphertext []byte
	var err error
	if secret != "" {
		sum := sha256.Sum256([]byte(secret))
		fingerprint = hex.EncodeToString(sum[:])
		ciphertext, err = a.encryptBillingWebhookSecret(secret)
		if err != nil {
			writeBillingV1Error(w, financial.ErrUnavailable)
			return
		}
	}
	payload := struct {
		ID, Name, Fingerprint string
		Expected              int64
		Enabled               bool
	}{r.PathValue("id"), name, fingerprint, expected, enabled}
	digest, _ := financial.DigestPayload(payload)
	connector, receipt, err := financial.NewCommercial(a.store.db).UpdateConnector(r.Context(), financial.UpdateConnector{Meta: billingWriteMeta(operationID, session.AdminID, digest), ID: r.PathValue("id"), Name: name, SecretCiphertext: ciphertext, Enabled: enabled, ExpectedRevision: expected})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "connector": billingConnectorView(connector)})
}

func (a *App) billingCommercialCreateTopUp(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "owner", "connector_id", "currency", "amount_micro")
	if !ok {
		return
	}
	owner, ownerOK := parseBillingOwnerObjectValue(object["owner"])
	connectorID, cok := object["connector_id"].(string)
	currency, uok := object["currency"].(string)
	amount, aok := billingPositiveString(object["amount_micro"])
	if !ownerOK || !cok || !uok || !aok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	digest, _ := financial.DigestPayload(struct {
		Owner                         financial.Owner
		ConnectorID, Currency, Amount string
	}{owner, connectorID, currency, strconv.FormatInt(amount, 10)})
	item, receipt, err := financial.NewCommercial(a.store.db).CreateTopUp(r.Context(), financial.CreateTopUp{Meta: billingWriteMeta(operationID, session.AdminID, digest), Owner: owner, ConnectorID: connectorID, Currency: currency, AmountMicro: amount})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "topup": billingTopUpView(item)})
}

func (a *App) billingCommercialSubscribe(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "owner", "plan_id")
	if !ok {
		return
	}
	owner, ownerOK := parseBillingOwnerObjectValue(object["owner"])
	planID, pok := object["plan_id"].(string)
	if !ownerOK || !pok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	digest, _ := financial.DigestPayload(struct {
		Owner  financial.Owner
		PlanID string
	}{owner, planID})
	item, receipt, err := financial.NewCommercial(a.store.db).PurchaseSubscription(r.Context(), financial.PurchaseSubscription{Meta: billingWriteMeta(operationID, session.AdminID, digest), Owner: owner, PlanID: planID})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "subscription": billingSubscriptionView(item)})
}

func (a *App) billingCommercialCancelSubscription(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "expected_revision")
	if !ok {
		return
	}
	expected, eok := parseGovernanceJSONRevision(object["expected_revision"])
	if !eok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	payload := struct {
		ID       string
		Expected int64
	}{r.PathValue("id"), expected}
	digest, _ := financial.DigestPayload(payload)
	item, receipt, err := financial.NewCommercial(a.store.db).CancelSubscription(r.Context(), financial.CancelSubscription{Meta: billingWriteMeta(operationID, session.AdminID, digest), ID: r.PathValue("id"), ExpectedRevision: expected})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "subscription": billingSubscriptionView(item)})
}

func (a *App) billingCommercialCreateCode(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "currency", "amount_micro", "max_uses", "expires_at")
	if !ok {
		return
	}
	currency, cok := object["currency"].(string)
	amount, aok := billingPositiveString(object["amount_micro"])
	maxUses, mok := parseGovernanceJSONRevision(object["max_uses"])
	expires, eok := billingOptionalTime(object["expires_at"])
	if !cok || !aok || !mok || !eok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	payload := struct {
		Currency, Amount string
		MaxUses          int64
		Expires          *time.Time
	}{currency, strconv.FormatInt(amount, 10), maxUses, expires}
	digest, _ := financial.DigestPayload(payload)
	code, err := randomBillingCode()
	if err != nil {
		writeBillingV1Error(w, financial.ErrUnavailable)
		return
	}
	codeDigest := a.secrets.digest(billingWebhookPurpose, code)
	var digestArray [32]byte
	copy(digestArray[:], codeDigest)
	item, receipt, err := financial.NewCommercial(a.store.db).CreateCode(r.Context(), financial.CreateRedemptionCode{Meta: billingWriteMeta(operationID, session.AdminID, digest), CodeDigest: digestArray, Currency: currency, AmountMicro: amount, MaxUses: maxUses, ExpiresAt: expires})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	plaintext := any(code)
	if receipt.Replay {
		plaintext = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "redemption_code": billingCodeView(item), "code": plaintext})
}

func (a *App) billingCommercialRedeem(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "owner", "code")
	if !ok {
		return
	}
	owner, ownerOK := parseBillingOwnerObjectValue(object["owner"])
	code, cok := object["code"].(string)
	if !ownerOK || !cok || len(code) < 16 || len(code) > 256 {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	codeDigest := a.secrets.digest(billingWebhookPurpose, code)
	var digestArray [32]byte
	copy(digestArray[:], codeDigest)
	payloadDigest, _ := financial.DigestPayload(struct {
		Owner      financial.Owner
		CodeDigest string
	}{owner, hex.EncodeToString(codeDigest)})
	entry, receipt, err := financial.NewCommercial(a.store.db).Redeem(r.Context(), financial.RedeemCode{Meta: billingWriteMeta(operationID, session.AdminID, payloadDigest), CodeDigest: digestArray, Owner: owner})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "entry_id": entry.ID, "amount_micro": strconv.FormatInt(entry.AmountMicro, 10), "currency": entry.Currency})
}

func (a *App) billingCommercialRefund(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, operationID, ok := decodeBillingCommercial(w, r, "payment_id", "amount_micro")
	if !ok {
		return
	}
	paymentID, pok := object["payment_id"].(string)
	amount, aok := billingPositiveString(object["amount_micro"])
	if !pok || !aok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	digest, _ := financial.DigestPayload(struct{ PaymentID, Amount string }{paymentID, strconv.FormatInt(amount, 10)})
	item, receipt, err := financial.NewCommercial(a.store.db).RefundPayment(r.Context(), financial.CreateRefund{Meta: billingWriteMeta(operationID, session.AdminID, digest), PaymentID: paymentID, AmountMicro: amount})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "refund": map[string]any{"id": item.ID, "payment_id": item.PaymentID, "entry_id": item.EntryID, "amount_micro": strconv.FormatInt(item.AmountMicro, 10), "created_at": item.CreatedAt.Format(time.RFC3339Nano)}})
}

func (a *App) billingCommercialPaymentCallback(w http.ResponseWriter, r *http.Request) {
	connectorID := r.PathValue("connector_id")
	eventID := r.Header.Get("X-Billing-Event-ID")
	timestampText := r.Header.Get("X-Billing-Timestamp")
	signature := r.Header.Get("X-Billing-Signature")
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if connectorID == "" || eventID == "" || strconv.FormatInt(timestamp, 10) != timestampText || len(signature) != 64 {
		writeAdminError(w, http.StatusUnauthorized, "invalid_signature", "Invalid payment callback signature.")
		return
	}
	signedAt := time.Unix(timestamp, 0).UTC()
	now := time.Now().UTC()
	if signedAt.Before(now.Add(-5*time.Minute)) || signedAt.After(now.Add(5*time.Minute)) {
		writeAdminError(w, http.StatusUnauthorized, "stale_callback", "Payment callback timestamp is outside the accepted window.")
		return
	}
	body, object, err := decodeBillingWebhook(w, r)
	if err != nil {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	secret, err := a.loadBillingWebhookSecret(r.Context(), connectorID)
	if err != nil {
		writeAdminError(w, http.StatusUnauthorized, "invalid_signature", "Invalid payment callback signature.")
		return
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestampText))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(eventID))
	mac.Write([]byte{'\n'})
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		writeAdminError(w, http.StatusUnauthorized, "invalid_signature", "Invalid payment callback signature.")
		return
	}
	if !exactJSONKeys(object, "status", "payment_id", "external_reference", "amount_micro", "currency") || object["status"] != "paid" {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	paymentID, pok := object["payment_id"].(string)
	external, xok := object["external_reference"].(string)
	currency, cok := object["currency"].(string)
	amount, aok := billingPositiveString(object["amount_micro"])
	if !pok || !xok || !cok || !aok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	payloadDigest := sha256.Sum256(body)
	item, err := financial.NewCommercial(a.store.db).ApplyPaid(r.Context(), financial.ApplyPayment{ConnectorID: connectorID, EventID: eventID, PaymentID: paymentID, ExternalReference: external, Currency: currency, AmountMicro: amount, PayloadDigest: payloadDigest, SignedAt: signedAt, ObservedAt: now})
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "payment_id": item.PaymentID, "status": item.Status})
}

func decodeBillingCommercial(w http.ResponseWriter, r *http.Request, fields ...string) (map[string]any, string, bool) {
	if r.URL.RawQuery != "" {
		writeBillingV1Error(w, financial.ErrInvalid)
		return nil, "", false
	}
	object, err := decodeUniqueJSONObject(w, r, billingV1MaxBody)
	if err != nil {
		writeBillingV1Error(w, financial.ErrInvalid)
		return nil, "", false
	}
	names := append([]string{"operation_id"}, fields...)
	if !exactJSONKeys(object, names...) {
		writeBillingV1Error(w, financial.ErrInvalid)
		return nil, "", false
	}
	operationID, ok := object["operation_id"].(string)
	if !ok || !validGovernanceOperationID(operationID) {
		writeBillingV1Error(w, financial.ErrInvalid)
		return nil, "", false
	}
	return object, operationID, true
}
func billingListQuery(r *http.Request) (string, int, bool) {
	query := r.URL.Query()
	for key, values := range query {
		if key != "after_id" && key != "limit" || len(values) != 1 {
			return "", 0, false
		}
	}
	after := query.Get("after_id")
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 || strconv.Itoa(parsed) != raw {
			return "", 0, false
		}
		limit = parsed
	}
	return after, limit, true
}
func billingListView(items []map[string]any, limit int) map[string]any {
	next := any(nil)
	if len(items) == limit && len(items) > 0 {
		next = items[len(items)-1]["id"]
	}
	return map[string]any{"items": items, "next_cursor": next}
}
func billingWriteMeta(operationID, actor string, digest [32]byte) financial.WriteMeta {
	return financial.WriteMeta{OperationID: operationID, ActorAdminID: actor, PayloadDigest: digest, ObservedAt: time.Now().UTC()}
}
func billingPositiveString(value any) (int64, bool) {
	text, ok := value.(string)
	if !ok || text == "" || text[0] == '0' {
		return 0, false
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	return parsed, err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == text
}
func billingOptionalTime(value any) (*time.Time, bool) {
	if value == nil {
		return nil, true
	}
	text, ok := value.(string)
	if !ok {
		return nil, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || parsed.Location() != time.UTC {
		return nil, false
	}
	return &parsed, true
}
func parseBillingOwnerObjectValue(value any) (financial.Owner, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return financial.Owner{}, false
	}
	return parseBillingOwnerObject(object)
}
func billingReceiptView(item financial.CommercialReceipt) map[string]any {
	return map[string]any{"operation_id": item.OperationID, "resource_kind": item.ResourceKind, "resource_id": item.ResourceID, "revision": item.Revision, "created_at": item.CreatedAt.Format(time.RFC3339Nano), "replay": item.Replay}
}
func billingPlanView(item financial.Plan) map[string]any {
	return map[string]any{"id": item.ID, "name": item.Name, "currency": item.Currency, "price_micro": strconv.FormatInt(item.PriceMicro, 10), "credit_micro": strconv.FormatInt(item.CreditMicro, 10), "interval": item.Interval, "enabled": item.Enabled, "revision": item.Revision, "created_at": item.CreatedAt.Format(time.RFC3339Nano), "updated_at": item.UpdatedAt.Format(time.RFC3339Nano)}
}
func billingConnectorView(item financial.Connector) map[string]any {
	return map[string]any{"id": item.ID, "name": item.Name, "enabled": item.Enabled, "revision": item.Revision, "created_at": item.CreatedAt.Format(time.RFC3339Nano), "updated_at": item.UpdatedAt.Format(time.RFC3339Nano)}
}
func billingTopUpView(item financial.TopUp) map[string]any {
	paidAt := any(nil)
	if item.PaidAt != nil {
		paidAt = item.PaidAt.Format(time.RFC3339Nano)
	}
	return map[string]any{"id": item.ID, "payment_id": item.PaymentID, "connector_id": item.ConnectorID, "external_reference": item.ExternalReference, "amount_micro": strconv.FormatInt(item.AmountMicro, 10), "currency": item.Currency, "status": item.Status, "refunded_micro": strconv.FormatInt(item.RefundedMicro, 10), "revision": item.Revision, "created_at": item.CreatedAt.Format(time.RFC3339Nano), "paid_at": paidAt}
}
func billingSubscriptionView(item financial.Subscription) map[string]any {
	return map[string]any{"id": item.ID, "plan_id": item.PlanID, "plan_revision": item.PlanRevision, "price_micro": strconv.FormatInt(item.PriceMicro, 10), "credit_micro": strconv.FormatInt(item.CreditMicro, 10), "currency": item.Currency, "interval": item.Interval, "status": item.Status, "revision": item.Revision, "started_at": item.StartedAt.Format(time.RFC3339Nano)}
}
func billingCodeView(item financial.RedemptionCode) map[string]any {
	expires := any(nil)
	if item.ExpiresAt != nil {
		expires = item.ExpiresAt.Format(time.RFC3339Nano)
	}
	return map[string]any{"id": item.ID, "amount_micro": strconv.FormatInt(item.AmountMicro, 10), "currency": item.Currency, "max_uses": item.MaxUses, "uses": item.Uses, "expires_at": expires, "enabled": item.Enabled, "created_at": item.CreatedAt.Format(time.RFC3339Nano)}
}

func (a *App) encryptBillingWebhookSecret(secret string) ([]byte, error) {
	if a == nil || a.secrets == nil || a.secrets.aead == nil {
		return nil, errors.New("missing secrets")
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := a.secrets.aead.Seal(nil, nonce, []byte(secret), []byte("cpacloud/billing-webhook-secret/v1"))
	return append(nonce, sealed...), nil
}
func (a *App) loadBillingWebhookSecret(ctx context.Context, connectorID string) (string, error) {
	var ciphertext []byte
	var enabled int
	if err := a.store.db.QueryRowContext(ctx, `SELECT secret_ciphertext,enabled FROM financial_payment_connectors WHERE id=?`, connectorID).Scan(&ciphertext, &enabled); err != nil || enabled != 1 {
		return "", errors.New("connector unavailable")
	}
	size := a.secrets.aead.NonceSize()
	if len(ciphertext) < size+a.secrets.aead.Overhead() {
		return "", errors.New("invalid ciphertext")
	}
	plain, err := a.secrets.aead.Open(nil, ciphertext[:size], ciphertext[size:], []byte("cpacloud/billing-webhook-secret/v1"))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
func randomBillingCode() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "cpa_" + base64.RawURLEncoding.EncodeToString(value), nil
}
func decodeBillingWebhook(w http.ResponseWriter, r *http.Request) ([]byte, map[string]any, error) {
	r.Body = http.MaxBytesReader(w, r.Body, billingV1MaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(body) {
		return nil, nil, errors.New("invalid body")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decodeUniqueJSONValue(decoder)
	if err != nil {
		return nil, nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("trailing JSON")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, errors.New("object required")
	}
	return body, object, nil
}
