package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/litemlflow/litemlflow/internal/model"
	"github.com/litemlflow/litemlflow/internal/store"
)

// TestDefaultWorkspaceExistsAfterMigrate verifies that the 'default' workspace
// is seeded by migration 005 and is immediately usable.
func TestDefaultWorkspaceExistsAfterMigrate(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	ws, err := s.GetWorkspace(ctx, "default")
	if err != nil {
		t.Fatalf("default workspace not found: %v", err)
	}
	if ws.ID != "default" {
		t.Fatalf("expected id 'default', got %q", ws.ID)
	}
	if ws.Name != "Default" {
		t.Fatalf("expected name 'Default', got %q", ws.Name)
	}
}

// TestWorkspaceCRUD exercises full create / get / list / update / delete.
func TestWorkspaceCRUD(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	// Create
	ws := &model.Workspace{ID: "team-alpha", Name: "Team Alpha", Description: "alpha team"}
	if err := s.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Get
	got, err := s.GetWorkspace(ctx, "team-alpha")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Team Alpha" || got.Description != "alpha team" {
		t.Fatalf("unexpected fields: %+v", got)
	}
	if got.CreationTime == 0 || got.LastUpdateTime == 0 {
		t.Fatal("timestamps should be set")
	}

	// List — should include 'default' + 'team-alpha'
	all, err := s.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(all))
	}

	// Update name
	newName := "Team Alpha Renamed"
	newDesc := "new description"
	if err := s.UpdateWorkspace(ctx, "team-alpha", &newName, &newDesc); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := s.GetWorkspace(ctx, "team-alpha")
	if got2.Name != newName {
		t.Fatalf("name not updated, got %q", got2.Name)
	}
	if got2.Description != newDesc {
		t.Fatalf("description not updated, got %q", got2.Description)
	}

	// Duplicate id should fail
	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "team-alpha", Name: "dup"}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("want ErrAlreadyExists, got %v", err)
	}

	// Delete
	if err := s.DeleteWorkspace(ctx, "team-alpha"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetWorkspace(ctx, "team-alpha"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete, got %v", err)
	}

	// Cannot delete default
	if err := s.DeleteWorkspace(ctx, "default"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("want ErrConflict for deleting default, got %v", err)
	}
}

// TestDeleteWorkspaceWithExperiments verifies that a workspace with live
// experiments cannot be deleted.
func TestDeleteWorkspaceWithExperiments(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws-nonempty", Name: "Non Empty"}); err != nil {
		t.Fatal(err)
	}
	// Create an experiment in that workspace.
	if _, err := s.CreateExperiment(ctx, &model.Experiment{Name: "exp1", WorkspaceID: "ws-nonempty"}); err != nil {
		t.Fatalf("create experiment: %v", err)
	}

	if err := s.DeleteWorkspace(ctx, "ws-nonempty"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("want ErrConflict when workspace has experiments, got %v", err)
	}
}

// TestWorkspaceMemberManagement covers AddMember, RemoveMember, ListMembers,
// GetMemberRole.
func TestWorkspaceMemberManagement(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws-members", Name: "Members WS"}); err != nil {
		t.Fatal(err)
	}

	// Add two members
	if err := s.AddMember(ctx, "ws-members", "alice", "admin"); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	if err := s.AddMember(ctx, "ws-members", "bob", "viewer"); err != nil {
		t.Fatalf("add bob: %v", err)
	}

	// List
	members, err := s.ListMembers(ctx, "ws-members")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}

	// GetMemberRole
	role, err := s.GetMemberRole(ctx, "ws-members", "alice")
	if err != nil {
		t.Fatalf("get role: %v", err)
	}
	if role != "admin" {
		t.Fatalf("want admin, got %q", role)
	}

	// Update role (upsert)
	if err := s.AddMember(ctx, "ws-members", "alice", "editor"); err != nil {
		t.Fatalf("update role: %v", err)
	}
	role2, _ := s.GetMemberRole(ctx, "ws-members", "alice")
	if role2 != "editor" {
		t.Fatalf("role should have changed to editor, got %q", role2)
	}

	// Invalid role
	if err := s.AddMember(ctx, "ws-members", "carol", "owner"); err == nil {
		t.Fatal("expected error for invalid role 'owner'")
	}

	// Remove
	if err := s.RemoveMember(ctx, "ws-members", "bob"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	members2, _ := s.ListMembers(ctx, "ws-members")
	if len(members2) != 1 {
		t.Fatalf("expected 1 member after remove, got %d", len(members2))
	}

	// Remove non-existent
	if err := s.RemoveMember(ctx, "ws-members", "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}

	// GetMemberRole for non-member
	if _, err := s.GetMemberRole(ctx, "ws-members", "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestWorkspaceIsolation verifies that experiments in workspace A are not
// visible when querying workspace B.
func TestWorkspaceIsolation(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws1", Name: "WS1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws2", Name: "WS2"}); err != nil {
		t.Fatal(err)
	}

	// Create experiments in different workspaces
	if _, err := s.CreateExperiment(ctx, &model.Experiment{Name: "exp-in-ws1", WorkspaceID: "ws1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateExperiment(ctx, &model.Experiment{Name: "exp-in-ws2", WorkspaceID: "ws2"}); err != nil {
		t.Fatal(err)
	}

	// Search from ws1 should NOT see ws2's experiment
	res1, err := s.SearchExperiments(ctx, store.SearchOptions{WorkspaceID: "ws1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res1.Items {
		if e.WorkspaceID != "ws1" {
			t.Fatalf("isolation breach: experiment %q with workspace_id=%q appeared in ws1 results", e.Name, e.WorkspaceID)
		}
	}

	// Search from ws2 should NOT see ws1's experiment
	res2, err := s.SearchExperiments(ctx, store.SearchOptions{WorkspaceID: "ws2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res2.Items {
		if e.WorkspaceID != "ws2" {
			t.Fatalf("isolation breach: experiment %q with workspace_id=%q appeared in ws2 results", e.Name, e.WorkspaceID)
		}
	}

	// GetExperimentByNameInWorkspace cross-workspace: should not find ws1's exp in ws2
	if _, err := s.GetExperimentByNameInWorkspace(ctx, "ws2", "exp-in-ws1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound for cross-workspace lookup, got %v", err)
	}
}

// TestMigrationColumnDefault verifies that the workspace_id column defaults
// to 'default' for pre-existing experiments. Here we simulate this by
// checking that experiments created without an explicit WorkspaceID end up
// in the default workspace.
func TestMigrationColumnDefault(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	// Create an experiment without explicitly setting WorkspaceID.
	id, err := s.CreateExperiment(ctx, &model.Experiment{Name: "legacy-exp"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetExperiment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "default" {
		t.Fatalf("expected workspace_id 'default', got %q", got.WorkspaceID)
	}
}

// TestWorkspaceGetByNameInWorkspace verifies that GetExperimentByName
// (backward-compat shim) scopes to default.
func TestWorkspaceGetByNameInWorkspace(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()

	if err := s.CreateWorkspace(ctx, &model.Workspace{ID: "ws-name", Name: "WS Name"}); err != nil {
		t.Fatal(err)
	}
	// Experiment in ws-name
	if _, err := s.CreateExperiment(ctx, &model.Experiment{Name: "shared-name", WorkspaceID: "ws-name"}); err != nil {
		t.Fatal(err)
	}
	// Same name in default
	if _, err := s.CreateExperiment(ctx, &model.Experiment{Name: "shared-name", WorkspaceID: "default"}); err != nil {
		t.Fatal(err)
	}

	// GetExperimentByName (backward-compat) should return the default one
	e, err := s.GetExperimentByName(ctx, "shared-name")
	if err != nil {
		t.Fatal(err)
	}
	if e.WorkspaceID != "default" {
		t.Fatalf("GetExperimentByName should scope to default, got workspace %q", e.WorkspaceID)
	}

	// GetExperimentByNameInWorkspace should return the ws-name one
	e2, err := s.GetExperimentByNameInWorkspace(ctx, "ws-name", "shared-name")
	if err != nil {
		t.Fatal(err)
	}
	if e2.WorkspaceID != "ws-name" {
		t.Fatalf("expected ws-name, got %q", e2.WorkspaceID)
	}
}
