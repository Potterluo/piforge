import { describe, expect, it } from "vitest";

import {
  fallsBackTo,
  isEffective,
  mountableSkills,
  overridesShared,
  shadowedBy,
} from "@/lib/skills";
import type { Skill } from "@/lib/types";

function skill(overrides: Partial<Skill> & Pick<Skill, "id" | "scope" | "name">): Skill {
  return {
    version: "1.0.0",
    description: "",
    content_hash: "",
    enabled: true,
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const shared = skill({ id: "skl_shared", scope: "admin", name: "review", effective: true });
const mine = skill({ id: "skl_mine", scope: "user", name: "review", version: "2.0.0" });

describe("the two-scope rule on the client", () => {
  it("treats a row without the flag as effective", () => {
    expect(isEffective({})).toBe(true);
    expect(isEffective({ effective: false })).toBe(false);
  });

  it("offers only enabled, effective rows to the composer", () => {
    expect(mountableSkills([shared, mine]).map((row) => row.id)).toEqual([
      "skl_shared",
      "skl_mine",
    ]);
    expect(mountableSkills([{ ...shared, effective: false }, mine]).map((row) => row.id)).toEqual([
      "skl_mine",
    ]);
    expect(mountableSkills([{ ...shared, enabled: false }])).toEqual([]);
  });

  it("names the effective row that hides a shadowed one", () => {
    expect(shadowedBy(shared, [shared, mine])).toBeNull(); // this is the winner
    const hidden = { ...shared, effective: false };
    expect(shadowedBy(hidden, [hidden, mine])?.id).toBe("skl_mine");
    expect(shadowedBy({ ...hidden, enabled: false }, [hidden, mine])).toBeNull();
  });

  it("finds the fallback after the private row is disabled", () => {
    const off = { ...mine, enabled: false };
    // The server re-marks the shared row effective when the private one is off;
    // the UI still has to explain where the skill went.
    expect(fallsBackTo(off, [shared, off])?.id).toBe("skl_shared");
    expect(fallsBackTo(off, [off])).toBeNull();
    expect(fallsBackTo(mine, [shared, mine])).toBeNull();
  });

  it("marks an effective private row that overrides a shared one", () => {
    expect(overridesShared(mine, [shared, mine])).toBe(true);
    expect(overridesShared(shared, [shared, mine])).toBe(false);
    expect(overridesShared(skill({ id: "skl_other", scope: "user", name: "other" }), [shared])).toBe(
      false,
    );
  });
});
