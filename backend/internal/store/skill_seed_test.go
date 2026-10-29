package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// FR-14.7: a fresh deployment has skills to try, and running the seeder twice does not
// duplicate them or overwrite an edit somebody made.
func TestBuiltinSkillsAreInstalledOnce(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	admin := mkUser(t, st, "admin@piforge.local", contracts.RoleAdmin)

	written := map[string]int{}
	write := func(skillID string, files map[string]string) (string, string, error) {
		if _, ok := files["SKILL.md"]; !ok {
			t.Fatalf("a built-in skill came without a SKILL.md: %v", files)
		}
		written[skillID]++
		return skillID, "hash-" + skillID, nil
	}

	created, err := st.EnsureBuiltinSkills(ctx, admin.ID, write)
	if err != nil {

		t.Fatalf("EnsureBuiltinSkills: %v", err)
	}
	if created != len(BuiltinSkills()) {
		t.Fatalf("created %d, want %d", created, len(BuiltinSkills()))
	}
	list, err := st.ListSkills(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(list) != created {
		t.Fatalf("list has %d skills, created %d", len(list), created)
	}
	for _, skill := range list {
		if skill.Scope != contracts.SkillAdmin {
			t.Fatalf("%s is %q, want a shared skill", skill.Name, skill.Scope)
		}
		if skill.BundleKey == "" || skill.ContentHash == "" {
			t.Fatalf("%s has no bundle: %+v", skill.Name, skill)
		}
	}

	// Running it again changes nothing.
	again, err := st.EnsureBuiltinSkills(ctx, admin.ID, write)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again != 0 {
		t.Fatalf("the second run created %d more", again)
	}
	for id, count := range written {
		if count != 1 {
			t.Fatalf("bundle %s was written %d times", id, count)
		}
	}
}

// A seeded skill must be visible to a normal user, otherwise "shared" means nothing.
func TestBuiltinSkillsAreVisibleToUsers(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	admin := mkUser(t, st, "admin@piforge.local", contracts.RoleAdmin)
	user := mkUser(t, st, "dev@piforge.local", contracts.RoleUser)

	if _, err := st.EnsureBuiltinSkills(ctx, admin.ID, func(id string, _ map[string]string) (string, string, error) {
		return id, "hash-" + id, nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	list, err := st.ListSkills(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	names := map[string]bool{}
	for _, skill := range list {
		names[skill.Name] = true
	}
	for _, builtin := range BuiltinSkills() {
		if !names[builtin.Name] {
			t.Fatalf("%s is not visible to a normal user: %v", builtin.Name, fmt.Sprint(names))
		}
	}
}
