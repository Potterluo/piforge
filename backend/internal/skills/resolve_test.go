package skills

import (
	"testing"
	"time"

	"github.com/piforge/piforge/backend/internal/contracts"
)

func row(id string, scope contracts.SkillScope, name, version string, enabled bool, created time.Time) *contracts.Skill {
	return &contracts.Skill{
		ID:        id,
		Scope:     scope,
		Name:      name,
		Version:   version,
		Enabled:   enabled,
		CreatedAt: created,
	}
}

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ids(rows []*contracts.Skill) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func TestPrivateShadowsSharedWithTheSameName(t *testing.T) {
	shared := row("skl_shared", contracts.SkillAdmin, "review", "1.0.0", true, base)
	private := row("skl_private", contracts.SkillUser, "review", "0.1.0", true, base.Add(time.Hour))

	// Order must not matter: the rule is the scope, not which row was created or
	// listed first.
	for _, input := range [][]*contracts.Skill{{shared, private}, {private, shared}} {
		effective := Effective(input)
		if len(effective) != 1 {
			t.Fatalf("Effective returned %d rows, want 1 (%v)", len(effective), ids(effective))
		}
		if effective[0].ID != "skl_private" {
			t.Fatalf("effective = %q, want the private row even though its version is lower", effective[0].ID)
		}

		annotated := Annotate(input)
		for _, annotatedRow := range annotated {
			want := annotatedRow.ID == "skl_private"
			if annotatedRow.Effective != want {
				t.Fatalf("%s effective = %v, want %v", annotatedRow.ID, annotatedRow.Effective, want)
			}
		}
	}
}

func TestDisablingThePrivateRowFallsBackToTheSharedOne(t *testing.T) {
	shared := row("skl_shared", contracts.SkillAdmin, "review", "1.0.0", true, base)
	disabledPrivate := row("skl_private", contracts.SkillUser, "review", "2.0.0", false, base.Add(time.Hour))

	effective := Effective([]*contracts.Skill{shared, disabledPrivate})
	if len(effective) != 1 || effective[0].ID != "skl_shared" {
		t.Fatalf("Effective = %v, want the shared row as the fallback", ids(effective))
	}
	for _, annotatedRow := range Annotate([]*contracts.Skill{shared, disabledPrivate}) {
		want := annotatedRow.ID == "skl_shared"
		if annotatedRow.Effective != want {
			t.Fatalf("%s effective = %v, want %v", annotatedRow.ID, annotatedRow.Effective, want)
		}
	}

	// Both disabled: nothing mounts, and the rows are still listed.
	disabledShared := row("skl_shared", contracts.SkillAdmin, "review", "1.0.0", false, base)
	if got := Effective([]*contracts.Skill{disabledShared, disabledPrivate}); len(got) != 0 {
		t.Fatalf("Effective with every row disabled = %v, want none", ids(got))
	}
}

func TestTwoUsersResolveTheSameSharedRowDifferently(t *testing.T) {
	// One deployment-wide row, one user's private copy. The resolver is fed the
	// rows each user can see (the store's ListSkills already scopes them), which
	// is how provisioning calls it.
	shared := row("skl_shared", contracts.SkillAdmin, "review", "1.0.0", true, base)
	alicePrivate := row("skl_alice", contracts.SkillUser, "review", "2.0.0", true, base.Add(time.Hour))

	alice := Effective([]*contracts.Skill{shared, alicePrivate})
	if len(alice) != 1 || alice[0].ID != "skl_alice" {
		t.Fatalf("alice effective = %v, want her private row", ids(alice))
	}
	bob := Effective([]*contracts.Skill{shared})
	if len(bob) != 1 || bob[0].ID != "skl_shared" {
		t.Fatalf("bob effective = %v, want the shared row", ids(bob))
	}
}

func TestHighestVersionWinsWithinOneScope(t *testing.T) {
	old := row("skl_old", contracts.SkillUser, "review", "2.9.0", true, base)
	newer := row("skl_new", contracts.SkillUser, "review", "2.10.0", true, base.Add(-time.Hour))

	effective := Effective([]*contracts.Skill{old, newer})
	if len(effective) != 1 || effective[0].ID != "skl_new" {
		t.Fatalf("effective = %v, want 2.10.0 (numeric comparison, not lexicographic)", ids(effective))
	}
}

func TestNamesAreComparedCaseInsensitively(t *testing.T) {
	// The mounted directory is the lower-cased slug, so "Review" and "review"
	// would land in one directory; the resolver must not treat them as two.
	shared := row("skl_shared", contracts.SkillAdmin, "review", "1.0.0", true, base)
	private := row("skl_private", contracts.SkillUser, "Review", "1.0.0", true, base.Add(time.Hour))

	effective := Effective([]*contracts.Skill{shared, private})
	if len(effective) != 1 || effective[0].ID != "skl_private" {
		t.Fatalf("effective = %v, want the private row for a case-different name", ids(effective))
	}
}
