// Package skills owns the two-scope rule for skills (PRD §5.8 / FR-14.1).
//
// A user sees two kinds of skill rows: their own private rows (`scope=user`,
// owner = the caller) and the deployment's shared rows (`scope=admin`). The
// same *name* can exist in both. This package decides, for one user, which row
// of a given name actually mounts.
//
// The rule, in order:
//
//  1. A disabled row never mounts and never shadows anything. "Disabled" means
//     "not configured", not "configured to be ignored by a better row".
//  2. Among enabled rows with the same name, a private (`user`) row shadows a
//     shared (`admin`) row. That is the "同名覆盖" half of FR-14.1: the user's
//     version is the one their sessions get, while every other user keeps the
//     shared one.
//  3. Within one scope (two versions of one private skill can exist), the
//     highest version wins; equal versions fall back to the newest row, then to
//     the id, so the answer never depends on map iteration order.
//  4. Names are compared after trimming and lower-casing. The mounted directory
//     is `materialize`'s lower-cased slug, so "Review" and "review" would
//     collide on disk; treating them as one name keeps the rule and the
//     filesystem in agreement.
//
// Consequence for disabling: turning the private row off makes the shared row
// effective again (fallback), rather than removing the skill. Consumers should
// report which row is effective — the API's `effective` field — instead of
// leaving the user to infer it from two identical-looking rows.
package skills

import (
	"sort"
	"strconv"
	"strings"

	"github.com/piforge/piforge/backend/internal/contracts"
)

// Resolution is one name's outcome: the row that mounts, and the enabled rows
// it hides. Effective is nil when every row with this name is disabled.
type Resolution struct {
	Name      string
	Effective *contracts.Skill
	Hidden    []*contracts.Skill
}

// Resolve groups rows by name and picks the effective row for each, in name
// order. Every input row appears exactly once as either an Effective or a
// Hidden row (unless it is nil); disabled rows are neither, because they mount
// nothing and shadow nothing.
func Resolve(all []*contracts.Skill) []Resolution {
	type group struct {
		name     string
		winner   *contracts.Skill
		hidden   []*contracts.Skill
		hasWin   bool
		disabled []*contracts.Skill
	}

	groups := map[string]*group{}
	order := make([]string, 0, len(all))
	for _, skill := range all {
		if skill == nil {
			continue
		}
		key := normalize(skill.Name)
		g, ok := groups[key]
		if !ok {
			g = &group{name: key}
			groups[key] = g
			order = append(order, key)
		}
		if !skill.Enabled {
			g.disabled = append(g.disabled, skill)
			continue
		}
		if !g.hasWin || beats(skill, g.winner) {
			if g.hasWin {
				g.hidden = append(g.hidden, g.winner)
			}
			g.winner = skill
			g.hasWin = true
		} else {
			g.hidden = append(g.hidden, skill)
		}
	}

	sort.Strings(order)
	out := make([]Resolution, 0, len(order))
	for _, key := range order {
		g := groups[key]
		// The name in the output is the winner's spelling when there is one;
		// otherwise the first disabled row's spelling is the only one available.
		name := g.name
		if g.hasWin {
			name = g.winner.Name
		} else if len(g.disabled) > 0 {
			name = g.disabled[0].Name
		}
		out = append(out, Resolution{Name: name, Effective: g.winner, Hidden: g.hidden})
	}
	return out
}

// Effective returns the rows that mount for this user, in name order. It is
// the list provisioning hands to `materialize.Mount`, so that package never has
// to guess which of two same-name rows was meant.
func Effective(all []*contracts.Skill) []*contracts.Skill {
	out := make([]*contracts.Skill, 0, len(all))
	for _, resolution := range Resolve(all) {
		if resolution.Effective != nil {
			out = append(out, resolution.Effective)
		}
	}
	return out
}

// Annotate returns copies of the rows with `Effective` set to the outcome of
// Resolve. It copies instead of mutating because the caller may be holding rows
// that also back a request-scoped cache; the wire flag must not leak into
// storage types.
func Annotate(all []*contracts.Skill) []*contracts.Skill {
	winners := map[string]string{} // normalized name -> effective id
	for _, resolution := range Resolve(all) {
		if resolution.Effective != nil {
			winners[normalize(resolution.Effective.Name)] = resolution.Effective.ID
		}
	}
	out := make([]*contracts.Skill, 0, len(all))
	for _, skill := range all {
		if skill == nil {
			continue
		}
		clone := *skill
		clone.Effective = winners[normalize(skill.Name)] == skill.ID
		out = append(out, &clone)
	}
	return out
}

// beats reports whether candidate should win over current. See the rule at the
// top of the file: user scope first, then version, then recency, then id.
func beats(candidate, current *contracts.Skill) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	candidateUser := candidate.Scope == contracts.SkillUser
	currentUser := current.Scope == contracts.SkillUser
	if candidateUser != currentUser {
		return candidateUser
	}
	if comparison := compareVersion(candidate.Version, current.Version); comparison != 0 {
		return comparison > 0
	}
	if !candidate.CreatedAt.Equal(current.CreatedAt) {
		return candidate.CreatedAt.After(current.CreatedAt)
	}
	return candidate.ID > current.ID
}

// compareVersion is a numeric-aware comparison, so 2.10.0 beats 2.9.0. It is
// deliberately not a full semver implementation: versions are free-form here
// (authors type them), and the only promise is a deterministic total order.
func compareVersion(a, b string) int {
	as := strings.Split(strings.TrimSpace(a), ".")
	bs := strings.Split(strings.TrimSpace(b), ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		ap, bp := "", ""
		if i < len(as) {
			ap = as[i]
		}
		if i < len(bs) {
			bp = bs[i]
		}
		an, aerr := strconv.Atoi(ap)
		bn, berr := strconv.Atoi(bp)
		if aerr == nil && berr == nil {
			if an != bn {
				if an > bn {
					return 1
				}
				return -1
			}
			continue
		}
		if ap != bp {
			if ap > bp {
				return 1
			}
			return -1
		}
	}
	return 0
}

func normalize(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
