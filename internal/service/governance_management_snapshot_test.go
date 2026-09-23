package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"cpacloud.local/server/internal/governance"
)

func TestGovernanceManagementUpdateRejectsMissingScope(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	group, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(801), "temporary", nil)
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(2)
	input := governancePolicyInput{ScopeKind: governance.ScopeGroup, ScopeID: group.ResourceID, Enabled: true, Hard: governanceHardLimits{RPM: &limit}}
	policy, err := f.manager.createPolicy(ctx, "admin-one", governanceOperationID(802), input)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate corrupt/dangling storage without waiting for restart validation.
	if _, err := f.base.db.Exec(`DELETE FROM governance_groups WHERE id=?`, group.ResourceID); err != nil {
		t.Fatal(err)
	}
	input.ScopeID, input.ScopeKind = "", ""
	_, err = f.manager.updatePolicy(ctx, "admin-one", governanceOperationID(803), policy.ResourceID, 1, input)
	if !errors.Is(err, errGovernanceManagementNotFound) {
		t.Fatalf("missing scope update: %v", err)
	}
	stored, err := loadGovernancePolicy(ctx, f.base.db, policy.ResourceID)
	if err != nil || stored.Revision != 1 {
		t.Fatalf("modified policy: revision=%d err=%v", stored.Revision, err)
	}
	assertGovernanceCount(t, f.base.db, "governance_management_operations", 2)
	assertGovernanceCount(t, f.base.db, "governance_management_audit", 2)
}

func TestGovernanceManagementGroupReadSnapshot(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	group, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(900), "members", []string{"employee-one"})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		for revision := int64(2); revision <= 60; revision++ {
			member := "employee-one"
			if revision%2 == 0 {
				member = "employee-two"
			}
			if _, err := f.manager.updateGroup(ctx, "admin-one", governanceOperationID(900+int(revision)), group.ResourceID, revision-1, "members", []string{member}); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	var readErr error
	for i := 0; i < 80; i++ {
		item, err := f.manager.group(ctx, group.ResourceID)
		if err != nil {
			readErr = err
			break
		}
		items, _, err := f.manager.listGroups(ctx, 10, "")
		if err != nil {
			readErr = err
			break
		}
		for _, snapshot := range append(items, item) {
			member := "employee-one"
			if snapshot.Revision%2 == 0 {
				member = "employee-two"
			}
			if len(snapshot.EmployeeIDs) != 1 || snapshot.EmployeeIDs[0] != member {
				readErr = fmt.Errorf("torn group snapshot revision=%d members=%v", snapshot.Revision, snapshot.EmployeeIDs)
			}
		}
		if readErr != nil {
			break
		}
	}
	writeErr := <-finished
	if readErr != nil || writeErr != nil {
		t.Fatalf("read=%v write=%v", readErr, writeErr)
	}
}
