package service

// Independently authored for docs/subscription-one-shot-renewal-contract.md.

import (
	"net/http"
	"time"

	"cpacloud.local/server/internal/financial"
)

func (a *App) billingOneShotGet(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" || r.ContentLength != 0 || r.PathValue("id") == "" {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	item, err := financial.NewCommercial(a.store.db).OneShotRenewal(r.Context(), r.PathValue("id"))
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, billingOneShotView(item))
}

func (a *App) billingOneShotArm(w http.ResponseWriter, r *http.Request, session adminSession) {
	a.billingOneShotWrite(w, r, session, false)
}

func (a *App) billingOneShotDisarm(w http.ResponseWriter, r *http.Request, session adminSession) {
	a.billingOneShotWrite(w, r, session, true)
}

func (a *App) billingOneShotWrite(w http.ResponseWriter, r *http.Request, session adminSession, disarm bool) {
	object, operationID, ok := decodeBillingCommercial(w, r, "expected_revision")
	if !ok {
		return
	}
	expected, ok := parseGovernanceJSONRevision(object["expected_revision"])
	if !ok {
		writeBillingV1Error(w, financial.ErrInvalid)
		return
	}
	id := r.PathValue("id")
	digest, _ := financial.DigestPayload(struct {
		ID       string
		Expected int64
	}{id, expected})
	meta := billingWriteMeta(operationID, session.AdminID, digest)
	commercial := financial.NewCommercial(a.store.db)
	var item financial.OneShotRenewalStatus
	var receipt financial.CommercialReceipt
	var err error
	if disarm {
		item, receipt, err = commercial.DisarmOneShotRenewal(r.Context(), financial.DisarmOneShotRenewal{Meta: meta, PredecessorID: id, ExpectedRevision: expected})
	} else {
		item, receipt, err = commercial.ArmOneShotRenewal(r.Context(), financial.ArmOneShotRenewal{Meta: meta, PredecessorID: id, ExpectedRevision: expected})
	}
	if err != nil {
		writeBillingV1Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"receipt": billingReceiptView(receipt), "one_shot_renewal": billingOneShotView(item)})
}

func billingOneShotView(item financial.OneShotRenewalStatus) map[string]any {
	var due, terminal any
	if item.DueAt != nil {
		due = item.DueAt.UTC().Format(time.RFC3339Nano)
	}
	if item.TerminalAt != nil {
		terminal = item.TerminalAt.UTC().Format(time.RFC3339Nano)
	}
	var reason, successor any
	if item.Reason != "" {
		reason = item.Reason
	}
	if item.SuccessorID != "" {
		successor = item.SuccessorID
	}
	return map[string]any{"predecessor_id": item.PredecessorID, "state": item.State, "revision": item.Revision, "due_at": due, "reason": reason, "successor_id": successor, "terminal_at": terminal}
}
