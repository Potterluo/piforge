import type { Skill } from "@/lib/types";

/**
 * The client half of the two-scope rule (FR-14.1).
 *
 * The server resolves which row of a name is in effect and sends `effective` on
 * every row. The client never re-derives the winner — that would be a second
 * rule that can drift — it only answers "may this row be mounted?" and "which
 * same-name row hides this one?".
 */

/** A row without the flag carries no shadowing information: treat it as effective. */
export function isEffective(skill: Pick<Skill, "effective">): boolean {
  return skill.effective !== false;
}

/** Enabled and effective: exactly what provisioning would mount for this user. */
export function mountableSkills(skills: Skill[]): Skill[] {
  return skills.filter((skill) => skill.enabled && isEffective(skill));
}

function sameName(a: Pick<Skill, "name">, b: Pick<Skill, "name">): boolean {
  return a.name.trim().toLowerCase() === b.name.trim().toLowerCase();
}

/**
 * The same-name row that takes this one's place, when this row is enabled but
 * not effective. Null for the effective row itself and for disabled rows: a
 * disabled row is not "shadowed", it is simply off (and may itself fall back).
 */
export function shadowedBy(skill: Skill, all: Skill[]): Skill | null {
  if (!skill.enabled || isEffective(skill)) return null;
  return (
    all.find((other) => other.id !== skill.id && other.enabled && sameName(other, skill) && isEffective(other)) ??
    null
  );
}

/** The effective same-name row a disabled row falls back to, if any. */
export function fallsBackTo(skill: Skill, all: Skill[]): Skill | null {
  if (skill.enabled) return null;
  return (
    all.find((other) => other.id !== skill.id && other.enabled && sameName(other, skill) && isEffective(other)) ??
    null
  );
}

/** True when this effective private row hides a shared row of the same name. */
export function overridesShared(skill: Skill, all: Skill[]): boolean {
  return (
    skill.scope === "user" &&
    isEffective(skill) &&
    all.some(
      (other) =>
        other.id !== skill.id && other.scope === "admin" && other.enabled && sameName(other, skill),
    )
  );
}
