// Independently authored for docs/admin-audit-export-contract.md using Go's
// standard CSV/HTTP APIs and this repository's existing audit projections.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	adminAuditExportPath     = "/admin/api/v1/audit/events/export.csv"
	adminAuditExportMaxRows  = 1000
	adminAuditExportMaxBytes = 2 * 1024 * 1024
	adminAuditExportFilename = `attachment; filename="cpa-cloud-admin-audit.csv"`
)

var errAdminAuditExportTooLarge = errors.New("audit export exceeds bound")

var adminAuditCSVHeader = []string{
	"source", "event_id", "actor_kind", "actor_id", "action", "target_type", "target_id", "result", "revision", "occurred_at",
}

func (a *App) getAdminAuditExport(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if a == nil || a.store == nil || a.store.db == nil {
		writeAdminAuditStorageError(w)
		return
	}
	if r == nil || r.URL == nil {
		writeAdminAuditInvalidError(w)
		return
	}
	if !validOptionalOrigin(r, a.cfg.TLSCert != "") {
		writeAdminError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.")
		return
	}
	parameters := r.URL.Query()
	if _, exists := parameters["cursor"]; exists {
		writeAdminAuditInvalidError(w)
		return
	}
	if _, exists := parameters["limit"]; exists {
		writeAdminAuditInvalidError(w)
		return
	}
	query, cursor, err := parseAdminAuditRequest(r, time.Now().UTC())
	if err != nil || cursor != "" {
		writeAdminAuditInvalidError(w)
		return
	}
	query.Limit = adminAuditExportMaxRows
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	payload, err := a.queryAdminAuditExport(ctx, query)
	if ctx.Err() != nil {
		writeAdminAuditStorageError(w)
		return
	}
	if errors.Is(err, errAdminAuditExportTooLarge) {
		writeAdminError(w, http.StatusRequestEntityTooLarge, "export_too_large", "Audit export is too large; narrow the time window or filters.")
		return
	}
	if err != nil {
		writeAdminAuditStorageError(w)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", adminAuditExportFilename)
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) queryAdminAuditExport(ctx context.Context, query adminAuditQuery) ([]byte, error) {
	validation := query
	validation.Limit = adminAuditMaxLimit
	if query.Limit != adminAuditExportMaxRows || validateAdminAuditQuery(validation) != nil {
		return nil, errors.New("invalid internal audit export query")
	}
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := validateAdminAuditSources(ctx, tx); err != nil {
		return nil, err
	}
	watermarks := make([]int64, len(adminAuditSources))
	for index, source := range adminAuditSources {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM `+source.table).Scan(&watermarks[index]); err != nil || watermarks[index] < 0 {
			return nil, errors.New("load audit export watermark")
		}
	}
	selected := make(map[string]bool, len(query.Sources))
	for _, source := range query.Sources {
		selected[source] = true
	}
	items := make([]adminAuditEventView, 0, adminAuditExportMaxRows+1)
	for index, source := range adminAuditSources {
		if !selected[source.token] {
			continue
		}
		page, err := queryAdminAuditSource(ctx, tx, source, watermarks[index], query, nil)
		if err != nil {
			return nil, err
		}
		items = append(items, page...)
		if len(items) > adminAuditExportMaxRows {
			return nil, errAdminAuditExportTooLarge
		}
	}
	sortAdminAuditItems(items)
	payload, err := encodeAdminAuditCSV(ctx, items)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return payload, nil
}

type boundedAdminAuditCSV struct{ bytes.Buffer }

func (buffer *boundedAdminAuditCSV) Write(value []byte) (int, error) {
	if len(value) > adminAuditExportMaxBytes-buffer.Len() {
		return 0, errAdminAuditExportTooLarge
	}
	return buffer.Buffer.Write(value)
}

func encodeAdminAuditCSV(ctx context.Context, items []adminAuditEventView) ([]byte, error) {
	buffer := &boundedAdminAuditCSV{}
	writer := csv.NewWriter(buffer)
	writer.UseCRLF = true
	if err := writer.Write(adminAuditCSVHeader); err != nil {
		return nil, err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		occurred, err := time.Parse(time.RFC3339Nano, item.OccurredAt)
		if err != nil || item.OccurredAt != occurred.UTC().Format(time.RFC3339Nano) || adminAuditSourceRank(item.Source) < 0 || item.Result != "succeeded" ||
			(item.ActorID == nil) != (item.ActorKind == "legacy_unknown") ||
			(item.Source != "financial_commercial" && item.ActorKind != "admin") ||
			(item.Source == "financial_commercial" && item.ActorKind != "admin" && item.ActorKind != "employee" && item.ActorKind != "system" && item.ActorKind != "legacy_unknown") {
			return nil, errors.New("invalid audit CSV event")
		}
		actor, revision := "", ""
		if item.ActorID != nil {
			if *item.ActorID == "" {
				return nil, errors.New("invalid audit CSV actor")
			}
			actor = *item.ActorID
		}
		if item.Revision != nil {
			if *item.Revision < 1 || *item.Revision > 9_007_199_254_740_991 {
				return nil, errors.New("invalid audit CSV revision")
			}
			revision = strconv.FormatInt(*item.Revision, 10)
		}
		fields := []string{item.Source, item.EventID, item.ActorKind, actor, item.Action, item.TargetType, item.TargetID, item.Result, revision, item.OccurredAt}
		for index, value := range fields {
			if value == "" && ((index == 3 && item.ActorID == nil) || (index == 8 && item.Revision == nil)) {
				continue
			}
			encoded, err := safeAdminAuditCSVCell(value)
			if err != nil {
				return nil, err
			}
			fields[index] = encoded
		}
		if err := writer.Write(fields); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func safeAdminAuditCSVCell(value string) (string, error) {
	if !validAdminAuditMetadata(value, 256) || !utf8.ValidString(value) || strings.IndexFunc(value, func(character rune) bool {
		return unicode.Is(unicode.Cf, character)
	}) >= 0 {
		return "", errors.New("unsafe audit CSV metadata")
	}
	if strings.ContainsRune("=+-@", rune(value[0])) {
		return "'" + value, nil
	}
	return value, nil
}
