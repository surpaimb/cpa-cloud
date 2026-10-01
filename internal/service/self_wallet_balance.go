// Independently authored for docs/employee-self-wallet-balance-contract.md.
package service

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"cpacloud.local/server/internal/financial"
)

const selfWalletBalanceTimeout = 5 * time.Second

func selfBalanceCurrency(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > 64 {
		return "", false
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) != 1 || len(values["currency"]) != 1 {
		return "", false
	}
	currency := values["currency"][0]
	if len(currency) != 3 {
		return "", false
	}
	for i := 0; i < len(currency); i++ {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return "", false
		}
	}
	return currency, true
}

func (a *App) selfWalletBalance(w http.ResponseWriter, r *http.Request, session selfSession) {
	currency, ok := selfBalanceCurrency(r.URL.RawQuery)
	if !ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfWalletBalanceTimeout)
	defer cancel()
	result, err := financial.NewLedger(a.store.db).ReadEmployeeBalance(ctx, session.EmployeeID, currency)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	var amount *string
	if result.HasAccount {
		value := decimal(result.AmountMicro)
		amount = &value
	}
	writeJSON(w, http.StatusOK, map[string]any{"currency": currency, "has_account": result.HasAccount, "amount_micro": amount})
}
