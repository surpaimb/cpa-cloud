// Independently authored for docs/employee-credential-retirement-contract.md.
// No new dependencies; existing licenses/provenance remain in that contract.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"
)

const employeeRetirementMaxRevision int64 = 9_007_199_254_740_990

type employeeRetirementView struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
}

// Private request-context seams have no HTTP representation or global state.
// Production requests carry none. Tests exercise this operation's own boundary.
type employeeRetirementHooksKey struct{}
type employeeRetirementHooks struct {
	beforeLock func()
	afterStep  func(string, *sql.Tx) error
	commit     func(*sql.Tx) error
}

func (h employeeRetirementHooks) step(stage string, tx *sql.Tx) error {
	if h.afterStep != nil {
		return h.afterStep(stage, tx)
	}
	return nil
}

func readEmployeeRetirementRevision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return 0, false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil || !utf8.Valid(raw) {
		return 0, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') || !d.More() {
		return 0, false
	}
	name, err := d.Token()
	if err != nil || name != "expected_revision" {
		return 0, false
	}
	var value json.RawMessage
	if err := d.Decode(&value); err != nil || d.More() {
		return 0, false
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return 0, false
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return 0, false
	}
	revision, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil || revision < 1 || revision > employeeRetirementMaxRevision || strconv.FormatInt(revision, 10) != string(value) {
		return 0, false
	}
	return revision, true
}

func (a *App) retireEmployeeCredentials(w http.ResponseWriter, r *http.Request, admin adminSession) {
	w.Header().Set("Cache-Control", "no-store")
	revision, ok := readEmployeeRetirementRevision(w, r)
	if !ok {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return
	}
	id := r.PathValue("id")
	if !validIdentifier(id, 128) {
		writeAdminError(w, 404, "not_found", "Employee was not found.")
		return
	}
	hooks, _ := r.Context().Value(employeeRetirementHooksKey{}).(employeeRetirementHooks)
	if hooks.beforeLock != nil {
		hooks.beforeLock()
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	view, failure := retireEmployeeCredentialsTx(r.Context(), tx, id, revision, admin.AdminID, selfTime(time.Now()), hooks)
	if failure != "" {
		switch failure {
		case "not_found":
			writeAdminError(w, 404, failure, "Employee was not found.")
		case "revision_conflict":
			writeAdminError(w, 409, failure, "The object was changed by another request.")
		default:
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		}
		return
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		// This is only an advisory wake: a failed result may still have committed.
		a.notifyAccountPoolChanged()
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	a.notifyAccountPoolChanged()
	writeJSON(w, http.StatusOK, view)
}

// The caller owns the single write transaction and admission lock. This helper
// does not begin/commit another transaction or query through the parent DB.
func retireEmployeeCredentialsTx(ctx context.Context, tx *sql.Tx, id string, revision int64, actor, stamp string, hooks employeeRetirementHooks) (employeeRetirementView, string) {
	failed := func() (employeeRetirementView, string) { return employeeRetirementView{}, "storage_unavailable" }
	if !validIdentifier(actor, 128) {
		return failed()
	}
	var current int64
	var status, kind string
	err := tx.QueryRowContext(ctx, `SELECT revision,status,typeof(revision) FROM employees WHERE id=?`, id).Scan(&current, &status, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return employeeRetirementView{}, "not_found"
	}
	if err != nil || kind != "integer" || current < 1 || current > employeeRetirementMaxRevision+1 || (status != "active" && status != "disabled") {
		return failed()
	}
	if current != revision {
		return employeeRetirementView{}, "revision_conflict"
	}
	expected, err := employeeRetirementExpectedFacts(ctx, tx, id, revision, stamp)
	if err != nil {
		return failed()
	}
	result, err := tx.ExecContext(ctx, `UPDATE employees SET status='disabled',revision=revision+1 WHERE id=? AND revision=?`, id, revision)
	if err != nil || !employeeRetirementRows(result, 1) || hooks.step("employee", tx) != nil {
		return failed()
	}
	// Enumerate metadata only; even expired and dormant slot Keys participate.
	rows, err := tx.QueryContext(ctx, `SELECT id,revoked_at,typeof(revoked_at) FROM access_keys WHERE employee_id=? ORDER BY id`, id)
	if err != nil {
		return failed()
	}
	var unrevoked int64
	for rows.Next() {
		var keyID, revokedKind string
		var revoked sql.NullString
		if err := rows.Scan(&keyID, &revoked, &revokedKind); err != nil || !validIdentifier(keyID, 128) || (revokedKind != "text" && revokedKind != "null") {
			_ = rows.Close()
			return failed()
		}
		if revoked.Valid {
			if _, err := parseTime(revoked.String); err != nil {
				_ = rows.Close()
				return failed()
			}
		} else {
			unrevoked++
		}
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return failed()
	}
	result, err = tx.ExecContext(ctx, `UPDATE access_keys SET revoked_at=? WHERE employee_id=? AND revoked_at IS NULL`, stamp, id)
	if err != nil || !employeeRetirementRows(result, unrevoked) || hooks.step("keys", tx) != nil {
		return failed()
	}
	// Include reverse ownership: a foreign slot cannot reference a target Key.
	rows, err = tx.QueryContext(ctx, `SELECT s.employee_id,s.key_id,s.state,k.employee_id FROM employee_self_key_slots s LEFT JOIN access_keys k ON k.id=s.key_id WHERE s.employee_id=? OR k.employee_id=?`, id, id)
	if err != nil {
		return failed()
	}
	var slotKey, slotState string
	var slotCount int
	for rows.Next() {
		var employeeID string
		var owner sql.NullString
		if err := rows.Scan(&employeeID, &slotKey, &slotState, &owner); err != nil || employeeID != id || !owner.Valid || owner.String != id || !validIdentifier(slotKey, 128) || (slotState != "pending" && slotState != "armed" && slotState != "issued" && slotState != "cancelled") {
			_ = rows.Close()
			return failed()
		}
		slotCount++
	}
	iterationErr, closeErr = rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || slotCount > 1 {
		return failed()
	}
	if slotState == "pending" || slotState == "armed" {
		result, err = tx.ExecContext(ctx, `UPDATE employee_self_key_slots SET state='cancelled',arm_fingerprint=NULL,arm_employee_revision=NULL,arm_key_policy_revision=NULL,arm_governance_revision=NULL,arm_budget_revision=NULL,cancelled_at=? WHERE employee_id=? AND key_id=? AND state IN ('pending','armed')`, stamp, id, slotKey)
		if err != nil || !employeeRetirementRows(result, 1) {
			return failed()
		}
	}
	if hooks.step("slot", tx) != nil {
		return failed()
	}
	for _, cleanup := range []struct{ table, stage string }{
		{"employee_self_sessions", "sessions"},
		{"employee_self_credentials", "credentials"},
	} {
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+cleanup.table+` WHERE employee_id=?`, id).Scan(&count); err != nil {
			return failed()
		}
		result, err = tx.ExecContext(ctx, `DELETE FROM `+cleanup.table+` WHERE employee_id=?`, id)
		if err != nil || !employeeRetirementRows(result, count) || hooks.step(cleanup.stage, tx) != nil {
			return failed()
		}
	}
	var previousAuditRow int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM account_lifecycle_audit`).Scan(&previousAuditRow); err != nil {
		return failed()
	}
	if err := recordLifecycleAudit(ctx, tx, actor, "employee.retire_credentials", "employee", id, "succeeded"); err != nil || hooks.step("audit", tx) != nil {
		return failed()
	}
	// Existing retention may evict old facts. It must not evict this operation's
	// own new event before success (for example after a wall-clock rollback).
	var retained int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_lifecycle_audit WHERE rowid>? AND actor_id=? AND action='employee.retire_credentials' AND target_type='employee' AND target_id=? AND result='succeeded'`, previousAuditRow, actor, id).Scan(&retained); err != nil || retained != 1 {
		return failed()
	}
	actual, err := employeeRetirementReadFacts(ctx, tx)
	if err != nil || !reflect.DeepEqual(expected, actual) {
		return failed()
	}
	// Only read-only confirmation follows the last mutable hook; the caller's
	// original sole Commit and its uncertain-result advisory wake are unchanged.
	return employeeRetirementView{ID: id, Status: "disabled", Revision: revision + 1}, ""
}

func employeeRetirementRows(result sql.Result, expected int64) bool {
	if result == nil {
		return false
	}
	count, err := result.RowsAffected()
	return err == nil && count == expected
}

// Operation-private complete typed facts. No values leave this transaction or
// enter logs/responses. The set is deliberately independent of target ownership.
type employeeRetirementFactCell struct {
	kind    byte
	text    string
	integer int64
	real    uint64
}

type employeeRetirementFactTable struct {
	name    string
	columns []string
	rows    [][]employeeRetirementFactCell
}

func employeeRetirementReadFacts(ctx context.Context, tx *sql.Tx) ([]employeeRetirementFactTable, error) {
	failure := errors.New("employee retirement facts unavailable")
	var facts []employeeRetirementFactTable
	for _, name := range []string{
		"employees", "access_keys", "employee_self_key_slots",
		"employee_self_credentials", "employee_self_sessions", "employee_models",
		"access_key_policies", "access_key_policy_protocols", "access_key_policy_models",
		"access_key_policy_sources", "access_key_policy_source_cidrs",
		"access_key_policy_account_groups", "access_key_policy_account_group_members",
	} {
		rows, err := tx.QueryContext(ctx, "SELECT * FROM "+name)
		if err != nil {
			return nil, failure
		}
		columns, err := rows.Columns()
		if err != nil || len(columns) == 0 {
			if rows.Close() != nil {
				return nil, failure
			}
			return nil, failure
		}
		table := employeeRetirementFactTable{name: name, columns: append([]string{}, columns...), rows: make([][]employeeRetirementFactCell, 0)}
		for rows.Next() {
			values, targets := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if rows.Scan(targets...) != nil {
				if rows.Close() != nil {
					return nil, failure
				}
				return nil, failure
			}
			cells := make([]employeeRetirementFactCell, len(values))
			for i, value := range values {
				switch v := value.(type) {
				case nil:
					cells[i].kind = 'n'
				case string:
					cells[i] = employeeRetirementFactCell{kind: 't', text: v}
				case int64:
					cells[i] = employeeRetirementFactCell{kind: 'i', integer: v}
				case float64:
					cells[i] = employeeRetirementFactCell{kind: 'r', real: math.Float64bits(v)}
				case []byte:
					// Conversion owns a copy before Rows can reuse its BLOB buffer.
					cells[i] = employeeRetirementFactCell{kind: 'b', text: string(v)}
				default:
					if rows.Close() != nil {
						return nil, failure
					}
					return nil, failure
				}
			}
			table.rows = append(table.rows, cells)
		}
		iterationErr, closeErr := rows.Err(), rows.Close()
		if iterationErr != nil || closeErr != nil {
			return nil, failure
		}
		facts = append(facts, table)
	}
	employeeRetirementSortFacts(facts)
	return facts, nil
}

// Sort full rows by storage class and exact owned values, not SQLite affinity or
// collation. Duplicate rows remain duplicates; neither hashes nor counts stand
// in for the actual rows or column inventory.
func employeeRetirementSortFacts(facts []employeeRetirementFactTable) {
	for i := range facts {
		rows := facts[i].rows
		sort.Slice(rows, func(a, b int) bool {
			for c, left := range rows[a] {
				right := rows[b][c]
				if left.kind != right.kind {
					return left.kind < right.kind
				}
				if left.text != right.text {
					return left.text < right.text
				}
				if left.integer != right.integer {
					return left.integer < right.integer
				}
				if left.real != right.real {
					return left.real < right.real
				}
			}
			return false
		})
	}
}

func employeeRetirementFactColumns(table employeeRetirementFactTable, names ...string) ([]int, error) {
	indexes := make([]int, len(names))
	for i, name := range names {
		indexes[i] = -1
		for c, column := range table.columns {
			if column == name {
				if indexes[i] != -1 {
					return nil, errors.New("employee retirement columns unavailable")
				}
				indexes[i] = c
			}
		}
		if indexes[i] == -1 {
			return nil, errors.New("employee retirement columns unavailable")
		}
	}
	return indexes, nil
}

// Derive the only permitted transformation entirely from pre-write facts and
// the supplied stamp. No observed write result is used as the expected state.
func employeeRetirementExpectedFacts(ctx context.Context, tx *sql.Tx, id string, revision int64, stamp string) ([]employeeRetirementFactTable, error) {
	facts, err := employeeRetirementReadFacts(ctx, tx)
	if err != nil {
		return nil, err
	}
	failure := errors.New("employee retirement baseline invalid")
	text := func(value string) employeeRetirementFactCell {
		return employeeRetirementFactCell{kind: 't', text: value}
	}
	employeeColumns, err := employeeRetirementFactColumns(facts[0], "id", "status", "revision")
	if err != nil {
		return nil, err
	}
	targets := 0
	for _, row := range facts[0].rows {
		if row[employeeColumns[0]].kind == 't' && row[employeeColumns[0]].text == id {
			if row[employeeColumns[1]].kind != 't' || row[employeeColumns[2]].kind != 'i' || row[employeeColumns[2]].integer != revision {
				return nil, failure
			}
			targets++
			row[employeeColumns[1]] = text("disabled")
			row[employeeColumns[2]] = employeeRetirementFactCell{kind: 'i', integer: revision + 1}
		}
	}
	if targets != 1 {
		return nil, failure
	}
	keyColumns, err := employeeRetirementFactColumns(facts[1], "id", "employee_id", "revoked_at")
	if err != nil {
		return nil, err
	}
	owners := make(map[string]string)
	for _, row := range facts[1].rows {
		key, owner := row[keyColumns[0]], row[keyColumns[1]]
		if key.kind != 't' || owner.kind != 't' || !validIdentifier(key.text, 128) {
			return nil, failure
		}
		if _, duplicate := owners[key.text]; duplicate {
			return nil, failure
		}
		owners[key.text] = owner.text
		if owner.text != id {
			continue
		}
		revoked := row[keyColumns[2]]
		switch revoked.kind {
		case 'n':
			row[keyColumns[2]] = text(stamp)
		case 't':
			if _, err := parseTime(revoked.text); err != nil {
				return nil, failure
			}
		default:
			return nil, failure
		}
	}
	slotColumns, err := employeeRetirementFactColumns(facts[2], "employee_id", "key_id", "state", "arm_fingerprint", "arm_employee_revision", "arm_key_policy_revision", "arm_governance_revision", "arm_budget_revision", "cancelled_at")
	if err != nil {
		return nil, err
	}
	related := 0
	for _, row := range facts[2].rows {
		owner, key := row[slotColumns[0]], row[slotColumns[1]]
		if owner.kind != 't' || key.kind != 't' {
			return nil, failure
		}
		keyOwner, exists := owners[key.text]
		if owner.text != id && keyOwner != id {
			continue
		}
		related++
		if related > 1 || owner.text != id || !exists || keyOwner != id {
			return nil, failure
		}
		state := row[slotColumns[2]]
		if state.kind != 't' {
			return nil, failure
		}
		switch state.text {
		case "pending", "armed":
			row[slotColumns[2]] = text("cancelled")
			for _, column := range slotColumns[3:8] {
				row[column] = employeeRetirementFactCell{kind: 'n'}
			}
			row[slotColumns[8]] = text(stamp)
		case "issued", "cancelled":
			// Lifetime tombstones, including history, remain byte-for-byte exact.
		default:
			return nil, failure
		}
	}
	for _, i := range []int{3, 4} {
		columns, err := employeeRetirementFactColumns(facts[i], "employee_id")
		if err != nil {
			return nil, err
		}
		retained := facts[i].rows[:0]
		for _, row := range facts[i].rows {
			if row[columns[0]].kind != 't' {
				return nil, failure
			}
			if row[columns[0]].text != id {
				retained = append(retained, row)
			}
		}
		facts[i].rows = retained
	}
	employeeRetirementSortFacts(facts)
	return facts, nil
}
