// Independently authored KEY-02 account-pool group policy tests.
package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeAccountGroupsSortsDetachesAndEnforcesModes(t *testing.T) {
	input := allPolicyReplacement()
	input.AccountGroupMode = ModeSelected
	input.AccountGroupIDs = []string{"grp:z", "grp-a", "grp/A", "grp.0", "grp_1"}
	normalized, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grp-a", "grp.0", "grp/A", "grp:z", "grp_1"}
	if !slices.Equal(normalized.AccountGroupIDs, want) {
		t.Fatalf("account group IDs=%v want=%v", normalized.AccountGroupIDs, want)
	}
	input.AccountGroupIDs[0] = "changed"
	if !slices.Equal(normalized.AccountGroupIDs, want) {
		t.Fatalf("normalized account group IDs retained caller storage: %v", normalized.AccountGroupIDs)
	}

	all := allPolicyReplacement()
	if normalized, err := Normalize(all); err != nil || normalized.AccountGroupIDs == nil || len(normalized.AccountGroupIDs) != 0 {
		t.Fatalf("all normalization=%#v error=%v", normalized, err)
	}
	emptySelected := allPolicyReplacement()
	emptySelected.AccountGroupMode = ModeSelected
	if normalized, err := Normalize(emptySelected); err != nil || normalized.AccountGroupIDs == nil || len(normalized.AccountGroupIDs) != 0 {
		t.Fatalf("selected empty normalization=%#v error=%v", normalized, err)
	}
}

func TestNormalizeAccountGroupsRejectsInvalidLimitsAndDuplicates(t *testing.T) {
	valid := allPolicyReplacement()
	valid.AccountGroupMode = ModeSelected
	cases := []struct {
		name   string
		mode   Mode
		values []string
	}{
		{"missing mode", "", []string{}},
		{"nil values", ModeSelected, nil},
		{"all with member", ModeAll, []string{"grp-a"}},
		{"empty member", ModeSelected, []string{""}},
		{"duplicate", ModeSelected, []string{"grp-a", "grp-a"}},
		{"leading whitespace", ModeSelected, []string{" grp-a"}},
		{"unicode", ModeSelected, []string{"分组"}},
		{"unsupported punctuation", ModeSelected, []string{"grp@a"}},
		{"member too long", ModeSelected, []string{strings.Repeat("a", MaxAccountGroupIDBytes+1)}},
		{"too many members", ModeSelected, accountGroupIDs(MaxAccountGroupIDs+1, 16)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.AccountGroupMode = test.mode
			input.AccountGroupIDs = test.values
			if _, err := Normalize(input); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	maximum := allPolicyReplacement()
	maximum.AccountGroupMode = ModeSelected
	maximum.AccountGroupIDs = accountGroupIDs(MaxAccountGroupIDs, MaxAccountGroupIDBytes)
	if _, err := Normalize(maximum); err != nil {
		t.Fatalf("exact account group limits rejected: %v", err)
	}
}

func TestAllowsAccountGroupAllSelectedAndInvalidStoredPolicy(t *testing.T) {
	all := Policy{
		Revision: 1, ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{}, AccountGroupMode: ModeAll, AccountGroupIDs: []string{},
	}
	if !AllowsAccountGroup(all, "") || !AllowsAccountGroup(all, "grp-any") {
		t.Fatal("all account group policy did not preserve candidate checks")
	}
	selected := all
	selected.AccountGroupMode = ModeSelected
	selected.AccountGroupIDs = []string{"grp-a", "grp-b"}
	if !AllowsAccountGroup(selected, "grp-a") || AllowsAccountGroup(selected, "") || AllowsAccountGroup(selected, "grp-c") {
		t.Fatalf("selected account group decision was incorrect: %#v", selected)
	}
	selected.AccountGroupIDs = []string{}
	if AllowsAccountGroup(selected, "grp-a") {
		t.Fatal("selected empty account group policy did not deny all")
	}
	selected.AccountGroupIDs = nil
	if AllowsAccountGroup(selected, "grp-a") {
		t.Fatal("invalid stored account group policy did not fail closed")
	}
}

func TestAccountGroupPolicyCreateLoadReplaceCASAndRollback(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	insertKey(t, db, "key-groups", "employee-one")

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, groupID := range []string{"grp-a", "grp-b"} {
		if _, err := tx.Exec(`INSERT INTO account_groups(id) VALUES(?)`, groupID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	createdInput := allPolicyReplacement()
	createdInput.AccountGroupMode = ModeSelected
	createdInput.AccountGroupIDs = []string{"grp-b", "grp-a"}
	created, err := CreateTx(context.Background(), tx, "key-groups", createdInput, testTime)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || !slices.Equal(created.AccountGroupIDs, []string{"grp-a", "grp-b"}) || !AllowsAccountGroup(created, "grp-a") {
		t.Fatalf("created policy=%#v", created)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadTx(context.Background(), tx, "key-groups")
	_ = tx.Rollback()
	if err != nil || !slices.Equal(loaded.AccountGroupIDs, created.AccountGroupIDs) {
		t.Fatalf("loaded policy=%#v error=%v", loaded, err)
	}

	if _, err := db.Exec(`CREATE TRIGGER reject_group_b BEFORE INSERT ON access_key_policy_account_group_members WHEN NEW.account_group_id='grp-b' BEGIN SELECT RAISE(ABORT,'synthetic member failure'); END`); err != nil {
		t.Fatal(err)
	}
	failing := allPolicyReplacement()
	failing.AccountGroupMode = ModeSelected
	failing.AccountGroupIDs = []string{"grp-b"}
	if _, err := Replace(context.Background(), db, "key-groups", 1, failing, testTime.Add(1)); err == nil {
		t.Fatal("synthetic account group member failure unexpectedly committed")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_group_b`); err != nil {
		t.Fatal(err)
	}
	assertStoredAccountGroups(t, db, "key-groups", 1, ModeSelected, []string{"grp-a", "grp-b"})

	unknown := allPolicyReplacement()
	unknown.AccountGroupMode = ModeSelected
	unknown.AccountGroupIDs = []string{"grp-missing"}
	if _, err := Replace(context.Background(), db, "key-groups", 1, unknown, testTime.Add(2)); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("unknown account group error=%v", err)
	}
	assertStoredAccountGroups(t, db, "key-groups", 1, ModeSelected, []string{"grp-a", "grp-b"})

	denyAll := allPolicyReplacement()
	denyAll.AccountGroupMode = ModeSelected
	updated, err := Replace(context.Background(), db, "key-groups", 1, denyAll, testTime.Add(3))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || AllowsAccountGroup(updated, "grp-a") {
		t.Fatalf("deny-all update=%#v", updated)
	}
	if _, err := Replace(context.Background(), db, "key-groups", 1, allPolicyReplacement(), testTime.Add(4)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale account group replacement error=%v", err)
	}
	reset, err := Replace(context.Background(), db, "key-groups", 2, allPolicyReplacement(), testTime.Add(5))
	if err != nil {
		t.Fatal(err)
	}
	if reset.Revision != 3 || !AllowsAccountGroup(reset, "") || len(reset.AccountGroupIDs) != 0 {
		t.Fatalf("all reset=%#v", reset)
	}
}

func TestAccountGroupPolicyDefaultCreationUsesAll(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	insertKey(t, db, "key-default-groups", "employee-one")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := CreateDefaultTx(context.Background(), tx, "key-default-groups", testTime); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertStoredAccountGroups(t, db, "key-default-groups", 1, ModeAll, []string{})
}

func TestAccountGroupPolicyRejectsUnknownGroupBeforeCreateWrites(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	insertKey(t, db, "key-unknown-group", "employee-one")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	input := allPolicyReplacement()
	input.AccountGroupMode = ModeSelected
	input.AccountGroupIDs = []string{"grp-missing"}
	if _, err := CreateTx(context.Background(), tx, "key-unknown-group", input, testTime); !errors.Is(err, ErrInvalidPolicy) {
		_ = tx.Rollback()
		t.Fatalf("unknown account group create error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_key_policies WHERE key_id='key-unknown-group'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unknown group create left policy count=%d error=%v", count, err)
	}
}

func allPolicyReplacement() Replacement {
	return Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{}, AccountGroupMode: ModeAll, AccountGroupIDs: []string{},
	}
}

func accountGroupIDs(count, size int) []string {
	values := make([]string, count)
	for index := range values {
		prefix := fmt.Sprintf("grp-%04d-", index)
		values[index] = prefix + strings.Repeat("a", size-len(prefix))
	}
	return values
}

func assertStoredAccountGroups(t *testing.T, db queryRower, keyID string, revision int64, mode Mode, members []string) {
	t.Helper()
	var gotRevision int64
	var gotMode Mode
	if err := db.QueryRow(`SELECT p.revision,g.account_group_mode FROM access_key_policies p JOIN access_key_policy_account_groups g ON g.key_id=p.key_id WHERE p.key_id=?`, keyID).Scan(&gotRevision, &gotMode); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT account_group_id FROM access_key_policy_account_group_members WHERE key_id=? ORDER BY account_group_id`, keyID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	gotMembers := make([]string, 0)
	for rows.Next() {
		var groupID string
		if err := rows.Scan(&groupID); err != nil {
			t.Fatal(err)
		}
		gotMembers = append(gotMembers, groupID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if gotRevision != revision || gotMode != mode || !slices.Equal(gotMembers, members) {
		t.Fatalf("stored revision=%d mode=%q members=%v want revision=%d mode=%q members=%v", gotRevision, gotMode, gotMembers, revision, mode, members)
	}
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}
