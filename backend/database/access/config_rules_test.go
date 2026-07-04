package access_test

import (
	"testing"

	"github.com/asdine/storm/v3"
)

func TestSetRule_DeclarativeLifecycle(t *testing.T) {
	setupTestSources()
	s, userStore := createTestStorage(t)
	createTestUser(t, userStore, "alice")
	createTestUser(t, userStore, "bob")
	err := s.LoadFromDB()
	if err != nil && err != storm.ErrNotFound {
		t.Errorf("unexpected error loading from DB: %v", err)
	}

	// Create: references a group nobody has logged in with yet — it must be
	// auto-created so the rule is valid before the first member's login.
	if err := s.SetRule("mnt/storage", "/coaches", true, nil, []string{"coaches"}, nil, nil); err != nil {
		t.Fatalf("SetRule create failed: %v", err)
	}
	if s.Permitted("mnt/storage", "/coaches", "alice") {
		t.Error("alice should be denied (denyAll, not in coaches)")
	}
	_ = s.AddUserToGroup("coaches", "bob")
	if !s.Permitted("mnt/storage", "/coaches", "bob") {
		t.Error("bob should be permitted (member of allowed group)")
	}

	// Replace: the declared rule authoritatively swaps principals — the old
	// group grant must be gone, not merged.
	if err := s.SetRule("mnt/storage", "/coaches", true, []string{"alice"}, nil, nil, nil); err != nil {
		t.Fatalf("SetRule replace failed: %v", err)
	}
	if s.Permitted("mnt/storage", "/coaches", "bob") {
		t.Error("bob should no longer be permitted after replace")
	}
	if !s.Permitted("mnt/storage", "/coaches", "alice") {
		t.Error("alice should be permitted after replace")
	}

	// Clear: an empty declared rule removes the path's rule entirely.
	if err := s.SetRule("mnt/storage", "/coaches", false, nil, nil, nil, nil); err != nil {
		t.Fatalf("SetRule clear failed: %v", err)
	}
	rules, err := s.GetAllRules("mnt/storage")
	if err != nil {
		t.Fatalf("GetAllRules failed: %v", err)
	}
	if _, exists := rules["/coaches/"]; exists {
		t.Error("rule should be removed after clearing")
	}
	if !s.Permitted("mnt/storage", "/coaches", "bob") {
		t.Error("bob should be permitted again (no rule, allow-by-default source)")
	}
}
