// Independently authored from docs/admin-audit-overview-contract.md using
// Go's public HTTP APIs and SQLite behavior already used by this repository.
package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cpacloud.local/server/internal/financial"
)

const (
	adminAuditPath          = "/admin/api/v1/audit/events"
	adminAuditDefaultLimit  = 50
	adminAuditMaxLimit      = 100
	adminAuditMaxWindow     = 31 * 24 * time.Hour
	adminAuditCursorLimit   = 4096
	adminAuditCursorVersion = 2
	adminAuditCursorPurpose = "admin-audit-cursor/v2"
	adminAuditTimeKeyLayout = "2006-01-02T15:04:05.000000000Z"
)

type adminAuditSourceSpec struct {
	token          string
	table          string
	idExpression   string
	actorExpr      string
	actionExpr     string
	targetTypeExpr string
	targetIDExpr   string
	resultExpr     string
	revisionExpr   string
	timeColumn     string
	rank           int
	actorNullable  bool
}

var adminAuditSources = []adminAuditSourceSpec{
	{"account_pool", "account_pool_audit", "id", "actor_id", "action", "target_type", "target_id", "result", "NULL", "occurred_at", 0, false},
	{"account_lifecycle", "account_lifecycle_audit", "id", "actor_id", "action", "target_type", "target_id", "result", "NULL", "occurred_at", 1, false},
	{"governance_management", "governance_management_audit", "operation_id", "actor_id", "action", "resource_kind", "resource_id", "'succeeded'", "revision", "created_at", 2, false},
	{"governance_general_budget", "governance_general_budget_audit", "operation_id", "actor_id", "action", "'budget'", "policy_id", "'succeeded'", "revision", "created_at", 3, false},
	{"financial_commercial", "financial_commercial_operations", "operation_id", "actor_admin_id", "action", "resource_kind", "resource_id", "'succeeded'", "revision", "created_at", 4, true},
}

type adminAuditQuery struct {
	From       time.Time
	To         time.Time
	Sources    []string
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	Result     string
	Limit      int
}

type adminAuditCursor struct {
	Version    int      `json:"v"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	SnapshotAt string   `json:"snapshot_at"`
	Sources    []string `json:"sources"`
	ActorID    string   `json:"actor_id,omitempty"`
	Action     string   `json:"action,omitempty"`
	TargetType string   `json:"target_type,omitempty"`
	TargetID   string   `json:"target_id,omitempty"`
	Result     string   `json:"result,omitempty"`
	Limit      int      `json:"limit"`
	Watermarks []int64  `json:"watermarks"`
	LastTime   string   `json:"last_time"`
	LastSource string   `json:"last_source"`
	LastID     string   `json:"last_id"`
}

type adminAuditEventView struct {
	Source     string  `json:"source"`
	EventID    string  `json:"event_id"`
	ActorID    *string `json:"actor_id"`
	Action     string  `json:"action"`
	TargetType string  `json:"target_type"`
	TargetID   string  `json:"target_id"`
	Result     string  `json:"result"`
	Revision   *int64  `json:"revision"`
	OccurredAt string  `json:"occurred_at"`

	occurred time.Time
	rank     int
}

type adminAuditPageView struct {
	From       string                `json:"from"`
	To         string                `json:"to"`
	SnapshotAt string                `json:"snapshot_at"`
	Sources    []string              `json:"sources"`
	Items      []adminAuditEventView `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

func (a *App) registerAdminAuditHandlers(mux *http.ServeMux) {
	if a == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET "+adminAuditPath, a.requireAdmin(a.getAdminAuditEvents, false))
	mux.HandleFunc("GET "+adminAuditExportPath, a.requireAdmin(a.getAdminAuditExport, false))
}

func (a *App) getAdminAuditEvents(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if a == nil || a.store == nil || a.store.db == nil || a.secrets == nil {
		writeAdminAuditStorageError(w)
		return
	}
	if !validOptionalOrigin(r, a.cfg.TLSCert != "") {
		writeAdminError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.")
		return
	}
	query, encodedCursor, err := parseAdminAuditRequest(r, time.Now().UTC())
	if err != nil {
		writeAdminAuditInvalidError(w)
		return
	}
	var cursor *adminAuditCursor
	if encodedCursor != "" {
		decoded, decodeErr := a.decodeAdminAuditCursor(encodedCursor)
		if decodeErr != nil {
			writeAdminAuditInvalidError(w)
			return
		}
		cursor = &decoded
		query = decoded.query()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	page, pageErr := a.queryAdminAudit(ctx, query, cursor)
	if pageErr != nil {
		writeAdminAuditStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func parseAdminAuditRequest(r *http.Request, now time.Time) (adminAuditQuery, string, error) {
	invalid := func() (adminAuditQuery, string, error) {
		return adminAuditQuery{}, "", errors.New("invalid audit query")
	}
	if r == nil || r.URL == nil {
		return invalid()
	}
	if raw := r.URL.RawQuery; raw != "" {
		for _, part := range strings.Split(raw, "&") {
			if part == "" {
				return invalid()
			}
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	if cursorValues, ok := values["cursor"]; ok {
		if len(values) != 1 || len(cursorValues) != 1 || cursorValues[0] == "" || len(cursorValues[0]) > adminAuditCursorLimit {
			return invalid()
		}
		return adminAuditQuery{}, cursorValues[0], nil
	}
	allowed := map[string]bool{"from": true, "to": true, "sources": true, "actor_id": true, "action": true, "target_type": true, "target_id": true, "result": true, "limit": true}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 || entries[0] == "" {
			return invalid()
		}
	}
	query := adminAuditQuery{Limit: adminAuditDefaultLimit, Sources: allAdminAuditSourceTokens()}
	fromValues, hasFrom := values["from"]
	toValues, hasTo := values["to"]
	if hasFrom != hasTo {
		return invalid()
	}
	if hasFrom {
		if len(fromValues[0]) > 64 || len(toValues[0]) > 64 {
			return invalid()
		}
		query.From, err = time.Parse(time.RFC3339Nano, fromValues[0])
		if err != nil {
			return invalid()
		}
		query.To, err = time.Parse(time.RFC3339Nano, toValues[0])
		if err != nil {
			return invalid()
		}
		query.From, query.To = query.From.UTC(), query.To.UTC()
	} else {
		query.To = now.UTC()
		query.From = query.To.Add(-24 * time.Hour)
	}
	if entries, ok := values["sources"]; ok {
		seen := make(map[string]bool)
		for _, token := range strings.Split(entries[0], ",") {
			if token == "" || seen[token] || adminAuditSourceRank(token) < 0 {
				return invalid()
			}
			seen[token] = true
		}
		query.Sources = query.Sources[:0]
		for _, spec := range adminAuditSources {
			if seen[spec.token] {
				query.Sources = append(query.Sources, spec.token)
			}
		}
	}
	for key, destination := range map[string]*string{"actor_id": &query.ActorID, "action": &query.Action, "target_type": &query.TargetType, "target_id": &query.TargetID, "result": &query.Result} {
		if entries, ok := values[key]; ok {
			*destination = entries[0]
			if !validAdminAuditMetadata(*destination, 256) {
				return invalid()
			}
		}
	}
	if entries, ok := values["limit"]; ok {
		if !validAdminAuditUnsignedDecimal(entries[0]) {
			return invalid()
		}
		query.Limit, err = strconv.Atoi(entries[0])
		if err != nil || query.Limit < 1 || query.Limit > adminAuditMaxLimit {
			return invalid()
		}
	}
	if err := validateAdminAuditQuery(query); err != nil {
		return invalid()
	}
	return query, "", nil
}

func validateAdminAuditQuery(query adminAuditQuery) error {
	if query.From.IsZero() || query.To.IsZero() || !query.From.Before(query.To) || query.To.Sub(query.From) > adminAuditMaxWindow || query.Limit < 1 || query.Limit > adminAuditMaxLimit {
		return errors.New("invalid audit query")
	}
	if len(query.Sources) < 1 || len(query.Sources) > len(adminAuditSources) {
		return errors.New("invalid audit sources")
	}
	canonical := make([]string, 0, len(query.Sources))
	selected := make(map[string]bool)
	for _, source := range query.Sources {
		if adminAuditSourceRank(source) < 0 || selected[source] {
			return errors.New("invalid audit sources")
		}
		selected[source] = true
	}
	for _, spec := range adminAuditSources {
		if selected[spec.token] {
			canonical = append(canonical, spec.token)
		}
	}
	if !slices.Equal(query.Sources, canonical) {
		return errors.New("non-canonical audit sources")
	}
	for _, value := range []string{query.ActorID, query.Action, query.TargetType, query.TargetID, query.Result} {
		if value != "" && !validAdminAuditMetadata(value, 256) {
			return errors.New("invalid audit metadata")
		}
	}
	return nil
}

func validAdminAuditUnsignedDecimal(value string) bool {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func validAdminAuditMetadata(value string, maximum int) bool {
	if value == "" || len([]byte(value)) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func allAdminAuditSourceTokens() []string {
	tokens := make([]string, 0, len(adminAuditSources))
	for _, source := range adminAuditSources {
		tokens = append(tokens, source.token)
	}
	return tokens
}

func adminAuditSourceRank(token string) int {
	for _, source := range adminAuditSources {
		if source.token == token {
			return source.rank
		}
	}
	return -1
}

func (cursor adminAuditCursor) query() adminAuditQuery {
	from, _ := time.Parse(time.RFC3339Nano, cursor.From)
	to, _ := time.Parse(time.RFC3339Nano, cursor.To)
	return adminAuditQuery{From: from.UTC(), To: to.UTC(), Sources: slices.Clone(cursor.Sources), ActorID: cursor.ActorID, Action: cursor.Action, TargetType: cursor.TargetType, TargetID: cursor.TargetID, Result: cursor.Result, Limit: cursor.Limit}
}

func (a *App) encodeAdminAuditCursor(cursor adminAuditCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := a.secrets.digest(adminAuditCursorPurpose, string(payload))
	encoded := base64.RawURLEncoding.EncodeToString(append(payload, mac...))
	if len(encoded) > adminAuditCursorLimit {
		return "", errors.New("audit cursor exceeds limit")
	}
	return encoded, nil
}

func (a *App) decodeAdminAuditCursor(encoded string) (adminAuditCursor, error) {
	invalid := func() (adminAuditCursor, error) { return adminAuditCursor{}, errors.New("invalid audit cursor") }
	if a == nil || a.secrets == nil || encoded == "" || len(encoded) > adminAuditCursorLimit {
		return invalid()
	}
	decodedLength := base64.RawURLEncoding.DecodedLen(len(encoded))
	if decodedLength <= 32 {
		return invalid()
	}
	raw := make([]byte, decodedLength)
	written, err := base64.RawURLEncoding.Strict().Decode(raw, []byte(encoded))
	if err != nil || written != decodedLength {
		return invalid()
	}
	raw = raw[:written]
	payload, provided := raw[:len(raw)-32], raw[len(raw)-32:]
	expected := a.secrets.digest(adminAuditCursorPurpose, string(payload))
	if subtle.ConstantTimeCompare(expected, provided) != 1 {
		return invalid()
	}
	var cursor adminAuditCursor
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return invalid()
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return invalid()
	}
	if cursor.Version != adminAuditCursorVersion || len(cursor.Watermarks) != len(adminAuditSources) || cursor.LastTime == "" || cursor.LastSource == "" || !validAdminAuditMetadata(cursor.LastID, 256) {
		return invalid()
	}
	for _, watermark := range cursor.Watermarks {
		if watermark < 0 {
			return invalid()
		}
	}
	if _, err := time.Parse(adminAuditTimeKeyLayout, cursor.LastTime); err != nil || adminAuditSourceRank(cursor.LastSource) < 0 {
		return invalid()
	}
	snapshot, err := time.Parse(time.RFC3339Nano, cursor.SnapshotAt)
	if err != nil || snapshot.Location() == nil {
		return invalid()
	}
	query := cursor.query()
	if err := validateAdminAuditQuery(query); err != nil || cursor.From != query.From.Format(time.RFC3339Nano) || cursor.To != query.To.Format(time.RFC3339Nano) || cursor.SnapshotAt != snapshot.UTC().Format(time.RFC3339Nano) {
		return invalid()
	}
	return cursor, nil
}

func (a *App) queryAdminAudit(ctx context.Context, query adminAuditQuery, cursor *adminAuditCursor) (adminAuditPageView, error) {
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return adminAuditPageView{}, err
	}
	defer tx.Rollback()
	if err := validateAdminAuditSources(ctx, tx); err != nil {
		return adminAuditPageView{}, err
	}
	watermarks := make([]int64, len(adminAuditSources))
	snapshotAt := time.Now().UTC()
	if cursor == nil {
		for index, source := range adminAuditSources {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM `+source.table).Scan(&watermarks[index]); err != nil || watermarks[index] < 0 {
				return adminAuditPageView{}, errors.New("load audit watermark")
			}
		}
	} else {
		copy(watermarks, cursor.Watermarks)
		var parseErr error
		snapshotAt, parseErr = time.Parse(time.RFC3339Nano, cursor.SnapshotAt)
		if parseErr != nil {
			return adminAuditPageView{}, parseErr
		}
	}
	selected := make(map[string]bool, len(query.Sources))
	for _, source := range query.Sources {
		selected[source] = true
	}
	items := make([]adminAuditEventView, 0, query.Limit+len(query.Sources))
	for index, source := range adminAuditSources {
		if !selected[source.token] {
			continue
		}
		page, err := queryAdminAuditSource(ctx, tx, source, watermarks[index], query, cursor)
		if err != nil {
			return adminAuditPageView{}, err
		}
		items = append(items, page...)
	}
	sortAdminAuditItems(items)
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	view := adminAuditPageView{
		From: query.From.Format(time.RFC3339Nano), To: query.To.Format(time.RFC3339Nano), SnapshotAt: snapshotAt.UTC().Format(time.RFC3339Nano),
		Sources: slices.Clone(query.Sources), Items: items,
	}
	if hasMore {
		last := items[len(items)-1]
		next, err := a.encodeAdminAuditCursor(adminAuditCursor{
			Version: adminAuditCursorVersion, From: view.From, To: view.To, SnapshotAt: view.SnapshotAt, Sources: slices.Clone(query.Sources),
			ActorID: query.ActorID, Action: query.Action, TargetType: query.TargetType, TargetID: query.TargetID, Result: query.Result, Limit: query.Limit,
			Watermarks: slices.Clone(watermarks), LastTime: adminAuditTimeKey(last.occurred), LastSource: last.Source, LastID: last.EventID,
		})
		if err != nil {
			return adminAuditPageView{}, err
		}
		view.NextCursor = &next
	}
	if err := tx.Commit(); err != nil {
		return adminAuditPageView{}, err
	}
	return view, nil
}

func sortAdminAuditItems(items []adminAuditEventView) {
	sort.Slice(items, func(left, right int) bool {
		if !items[left].occurred.Equal(items[right].occurred) {
			return items[left].occurred.After(items[right].occurred)
		}
		if items[left].rank != items[right].rank {
			return items[left].rank < items[right].rank
		}
		return items[left].EventID > items[right].EventID
	})
}

func queryAdminAuditSource(ctx context.Context, tx *sql.Tx, source adminAuditSourceSpec, watermark int64, query adminAuditQuery, cursor *adminAuditCursor) ([]adminAuditEventView, error) {
	timeKey := adminAuditSQLiteTimeKey(source.timeColumn)
	clauses := []string{"rowid<=?", timeKey + ">=?", timeKey + "<?"}
	arguments := []any{watermark, adminAuditTimeKey(query.From), adminAuditTimeKey(query.To)}
	for _, filter := range []struct{ value, expression string }{
		{query.ActorID, source.actorExpr}, {query.Action, source.actionExpr}, {query.TargetType, source.targetTypeExpr},
		{query.TargetID, source.targetIDExpr}, {query.Result, source.resultExpr},
	} {
		if filter.value != "" {
			clauses = append(clauses, filter.expression+"=?")
			arguments = append(arguments, filter.value)
		}
	}
	if cursor != nil {
		lastRank := adminAuditSourceRank(cursor.LastSource)
		switch {
		case source.rank < lastRank:
			clauses = append(clauses, timeKey+"<?")
			arguments = append(arguments, cursor.LastTime)
		case source.rank > lastRank:
			clauses = append(clauses, timeKey+"<=?")
			arguments = append(arguments, cursor.LastTime)
		default:
			clauses = append(clauses, "("+timeKey+"<? OR ("+timeKey+"=? AND "+source.idExpression+"<?))")
			arguments = append(arguments, cursor.LastTime, cursor.LastTime, cursor.LastID)
		}
	}
	arguments = append(arguments, query.Limit+1)
	statement := fmt.Sprintf(`SELECT %s,%s,%s,%s,%s,%s,%s,%s,rowid FROM %s WHERE %s ORDER BY %s DESC,%s DESC LIMIT ?`,
		source.idExpression, source.actorExpr, source.actionExpr, source.targetTypeExpr, source.targetIDExpr, source.resultExpr, source.revisionExpr, source.timeColumn,
		source.table, strings.Join(clauses, " AND "), timeKey, source.idExpression)
	rows, err := tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]adminAuditEventView, 0, query.Limit+1)
	for rows.Next() {
		var item adminAuditEventView
		var actor sql.NullString
		var revision sql.NullInt64
		var timestamp string
		var rowID int64
		if err := rows.Scan(&item.EventID, &actor, &item.Action, &item.TargetType, &item.TargetID, &item.Result, &revision, &timestamp, &rowID); err != nil {
			return nil, err
		}
		if rowID < 1 || !validAdminAuditMetadata(item.EventID, 256) || (!actor.Valid && !source.actorNullable) || (actor.Valid && !validAdminAuditMetadata(actor.String, 256)) || !validAdminAuditMetadata(item.Action, 256) || !validAdminAuditMetadata(item.TargetType, 256) || !validAdminAuditMetadata(item.TargetID, 256) || item.Result != "succeeded" {
			return nil, errors.New("invalid audit metadata")
		}
		if actor.Valid {
			value := actor.String
			item.ActorID = &value
		}
		occurred, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, errors.New("invalid audit timestamp")
		}
		_, offset := occurred.Zone()
		if offset != 0 || occurred.Before(query.From) || !occurred.Before(query.To) {
			return nil, errors.New("invalid audit timestamp range")
		}
		if revision.Valid {
			if revision.Int64 < 1 || revision.Int64 > 9_007_199_254_740_991 {
				return nil, errors.New("invalid audit revision")
			}
			value := revision.Int64
			item.Revision = &value
		}
		item.Source, item.OccurredAt, item.occurred, item.rank = source.token, occurred.UTC().Format(time.RFC3339Nano), occurred.UTC(), source.rank
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, rows.Close()
}

func adminAuditTimeKey(value time.Time) string { return value.UTC().Format(adminAuditTimeKeyLayout) }

func adminAuditSQLiteTimeKey(column string) string {
	fraction := "substr(" + column + ",21,length(" + column + ")-21)"
	return "(CASE WHEN length(" + column + ")=20 AND substr(" + column + ",20,1)='Z' THEN substr(" + column + ",1,19)||'.000000000Z' " +
		"WHEN length(" + column + ") BETWEEN 22 AND 30 AND substr(" + column + ",20,1)='.' AND substr(" + column + ",-1,1)='Z' AND " + fraction + "<>'' AND " + fraction + " NOT GLOB '*[^0-9]*' " +
		"THEN substr(" + column + ",1,20)||substr(" + fraction + "||'000000000',1,9)||'Z' ELSE '' END)"
}

func validateAdminAuditSources(ctx context.Context, tx *sql.Tx) error {
	metadataColumns := map[string]schemaColumn{
		"id": {kind: "TEXT", primaryKey: 1}, "actor_id": {kind: "TEXT", notNull: true}, "action": {kind: "TEXT", notNull: true},
		"target_type": {kind: "TEXT", notNull: true}, "target_id": {kind: "TEXT", notNull: true}, "result": {kind: "TEXT", notNull: true}, "occurred_at": {kind: "TEXT", notNull: true},
	}
	if err := validateAdminAuditColumns(ctx, tx, "account_pool_audit", metadataColumns); err != nil {
		return err
	}
	if err := validateAdminAuditColumns(ctx, tx, "account_lifecycle_audit", metadataColumns); err != nil {
		return err
	}
	var poolDDL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='account_pool_audit'`).Scan(&poolDDL); err != nil {
		return err
	}
	normalizedPool := strings.ToLower(strings.Join(strings.Fields(poolDDL), " "))
	for _, fragment := range []string{"actor_id text not null references admins(id)", "check(result = 'succeeded')"} {
		if !strings.Contains(normalizedPool, fragment) {
			return errors.New("account pool audit schema mismatch")
		}
	}
	for name, expected := range map[string]string{
		"account_pool_audit_time_idx":      "create index account_pool_audit_time_idx on account_pool_audit(occurred_at,id)",
		"account_lifecycle_audit_time_idx": "create index account_lifecycle_audit_time_idx on account_lifecycle_audit(occurred_at,id)",
	} {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "index" || normalizeAdminAuditSQL(actual) != normalizeAdminAuditSQL(expected) {
			return errors.New("audit time index mismatch")
		}
	}
	for table, expected := range map[string]string{"governance_management_audit": governanceAuditDDL, "governance_general_budget_audit": generalBudgetAuditDDL} {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, table).Scan(&kind, &actual); err != nil || kind != "table" || normalizeGovernanceDDL(actual) != normalizeGovernanceDDL(storedGovernanceDDL(expected)) {
			return errors.New("governance audit schema mismatch")
		}
	}
	return financial.ValidateCommercialAuditSource(ctx, tx)
}

func validateAdminAuditColumns(ctx context.Context, tx *sql.Tx, table string, expected map[string]schemaColumn) error {
	columns, err := schemaColumns(ctx, tx, table)
	if err != nil || len(columns) != len(expected) {
		return errors.New("audit table schema mismatch")
	}
	for name, wanted := range expected {
		actual, ok := columns[name]
		if !ok || actual.kind != wanted.kind || actual.notNull != wanted.notNull || actual.primaryKey != wanted.primaryKey || actual.defaultSQL.Valid {
			return errors.New("audit table column mismatch")
		}
	}
	return nil
}

func normalizeAdminAuditSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), ""))
}

func writeAdminAuditInvalidError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid audit request.")
}

func writeAdminAuditStorageError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Audit events are temporarily unavailable.")
}
