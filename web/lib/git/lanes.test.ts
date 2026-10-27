import { describe, expect, it } from "vitest";

import { assignLanes, hasAncestor } from "@/lib/git/lanes";
import type { GitCommit } from "@/lib/types";

/** Minimal commit factory: the layout only reads `hash` and `parents`. */
function commit(hash: string, ...parents: string[]): GitCommit {
  return {
    hash,
    short: hash.slice(0, 7),
    parents,
    author: "tester",
    ts: "2025-01-01T00:00:00Z",
    subject: hash,
  };
}

function lanesOf(layout: ReturnType<typeof assignLanes>): number[] {
  return layout.nodes.map((laneNode) => laneNode.lane);
}

describe("assignLanes", () => {
  it("returns an empty layout for an empty history", () => {
    const layout = assignLanes([]);

    expect(layout.nodes).toEqual([]);
    expect(layout.laneCount).toBe(0);
  });

  it("keeps a linear history in lane 0", () => {
    const layout = assignLanes([commit("a", "b"), commit("b", "c"), commit("c")]);

    expect(lanesOf(layout)).toEqual([0, 0, 0]);
    expect(layout.laneCount).toBe(1);
    expect(layout.nodes.map((node) => node.row)).toEqual([0, 1, 2]);
  });

  it("gives a merge's second parent its own lane and rejoins afterwards", () => {
    // main: a -> x -> root ; feature: b -> root, merged by m
    const layout = assignLanes([
      commit("m", "x", "b"),
      commit("x", "a"),
      commit("b", "a"),
      commit("a"),
    ]);

    expect(lanesOf(layout)).toEqual([0, 0, 1, 0]);
    expect(layout.laneCount).toBe(2);
    expect(layout.nodes[0].parents).toEqual([
      { hash: "x", lane: 0 },
      { hash: "b", lane: 1 },
    ]);
  });

  it("keeps two parallel branches on two lanes until they meet", () => {
    const layout = assignLanes([
      commit("h1", "p1"),
      commit("h2", "p2"),
      commit("p1", "r"),
      commit("p2", "r"),
      commit("r"),
    ]);

    expect(lanesOf(layout)).toEqual([0, 1, 0, 1, 0]);
    expect(layout.laneCount).toBe(2);

    // The shared parent is claimed by the leftmost child's lane.
    expect(layout.nodes[4].lane).toBe(0);
    expect(layout.nodes[3].parents).toEqual([{ hash: "r", lane: 1 }]);
  });

  it("opens one lane per extra parent for an octopus merge", () => {
    const layout = assignLanes([
      commit("m", "a", "b", "c"),
      commit("a"),
      commit("b"),
      commit("c"),
    ]);

    expect(lanesOf(layout)).toEqual([0, 0, 1, 2]);
    expect(layout.laneCount).toBe(3);
    expect(layout.nodes[0].parents).toEqual([
      { hash: "a", lane: 0 },
      { hash: "b", lane: 1 },
      { hash: "c", lane: 2 },
    ]);
  });

  it("puts a commit that is the parent of two children on the first child's lane", () => {
    const layout = assignLanes([
      commit("c", "a"),
      commit("d", "a"),
      commit("a", "b"),
      commit("b"),
    ]);

    expect(lanesOf(layout)).toEqual([0, 1, 0, 0]);
    expect(layout.laneCount).toBe(2);
  });

  it("reuses a lane as soon as the hash it waited for was reached", () => {
    const layout = assignLanes([
      commit("a", "b"),
      commit("c", "d"),
      commit("b"),
      commit("e", "f"),
    ]);

    // After `b`, lane 0 is free again, so `e` reuses it instead of opening lane 2.
    expect(lanesOf(layout)).toEqual([0, 1, 0, 0]);
    expect(layout.laneCount).toBe(2);
  });

  it("records lane occupancy around each row", () => {
    const layout = assignLanes([commit("m", "x", "b"), commit("x", "a"), commit("b", "a"), commit("a")]);

    expect(layout.nodes[0].incoming).toEqual([]);
    expect(layout.nodes[0].outgoing).toEqual(["x", "b"]);
    // The second parent's lane stays reserved while the first branch is walked.
    expect(layout.nodes[1].incoming).toEqual(["x", "b"]);
    expect(layout.nodes[1].outgoing).toEqual(["a", "b"]);
    // The shared parent is reached: lane 0 is released, lane 1 still waits.
    expect(layout.nodes[3].outgoing).toEqual([null, "a"]);
  });

  it("ignores parents that are outside the loaded window", () => {
    const layout = assignLanes([commit("a", "missing"), commit("b")]);

    expect(layout.nodes[0].parents).toEqual([{ hash: "missing", lane: 0 }]);
    expect(lanesOf(layout)).toEqual([0, 1]);
  });
});

describe("hasAncestor", () => {
  const layout = assignLanes([
    commit("m", "x", "b"),
    commit("x", "a"),
    commit("b", "a"),
    commit("a"),
  ]);

  it("walks towards parents", () => {
    expect(hasAncestor(layout, "m", "a")).toBe(true);
    expect(hasAncestor(layout, "m", "b")).toBe(true);
    expect(hasAncestor(layout, "x", "m")).toBe(false);
    expect(hasAncestor(layout, "a", "m")).toBe(false);
  });
});
