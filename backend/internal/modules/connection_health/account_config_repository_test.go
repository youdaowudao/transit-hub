package connection_health

import (
	"context"
	"testing"
)

func TestAccountIntelligenceWeightRepositorySchemaEnforcesRangeAndIsolation(t *testing.T) {
	pool := openQuestionAnswerPostgresPool(t)
	repository := NewRepository(pool)
	ctx := context.Background()
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema first run: %v", err)
	}
	if err := repository.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema second run must remain idempotent: %v", err)
	}

	var tableName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('connection_health_account_configs')::text`).Scan(&tableName); err != nil {
		t.Fatalf("find account config table: %v", err)
	}
	if tableName == nil || *tableName != "connection_health_account_configs" {
		t.Fatalf("account config table = %v, want connection_health_account_configs", tableName)
	}

	for _, fixture := range []struct {
		userID         string
		adminAccountID string
		targetID       string
		weight         *int
	}{
		{userID: "user-a", adminAccountID: "workspace-a", targetID: "sub2api:workspace-a:null", weight: nil},
		{userID: "user-a", adminAccountID: "workspace-a", targetID: "sub2api:workspace-a:zero", weight: intPointer(0)},
		{userID: "user-a", adminAccountID: "workspace-a", targetID: "sub2api:workspace-a:one", weight: intPointer(1)},
		{userID: "user-a", adminAccountID: "workspace-a", targetID: "sub2api:workspace-a:hundred", weight: intPointer(100)},
		{userID: "user-b", adminAccountID: "workspace-a", targetID: "sub2api:workspace-a:zero", weight: intPointer(80)},
		{userID: "user-a", adminAccountID: "workspace-b", targetID: "sub2api:workspace-b:zero", weight: intPointer(60)},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO connection_health_account_configs (
				user_id, admin_account_id, target_id, intelligence_weight
			) VALUES ($1, $2, $3, $4)
		`, fixture.userID, fixture.adminAccountID, fixture.targetID, fixture.weight); err != nil {
			t.Fatalf("insert valid account config %+v: %v", fixture, err)
		}
	}

	for _, invalid := range []int{-1, 101} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO connection_health_account_configs (
				user_id, admin_account_id, target_id, intelligence_weight
			) VALUES ('user-a', 'workspace-a', $1, $2)
		`, "sub2api:workspace-a:invalid", invalid); err == nil {
			t.Fatalf("out-of-range intelligence weight %d must violate the database constraint", invalid)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO connection_health_account_configs (
			user_id, admin_account_id, target_id, intelligence_weight
		) VALUES ('user-a', 'workspace-a', 'sub2api:workspace-a:zero', 50)
	`); err == nil {
		t.Fatal("duplicate user/workspace/target config must violate the primary key")
	}

	var isolatedRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM connection_health_account_configs
		WHERE target_id = 'sub2api:workspace-a:zero'
	`).Scan(&isolatedRows); err != nil {
		t.Fatalf("count isolated account configs: %v", err)
	}
	if isolatedRows != 2 {
		t.Fatalf("same target across users must keep isolated rows, got %d want 2", isolatedRows)
	}
}

func intPointer(value int) *int { return &value }
