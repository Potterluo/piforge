/**
 * Lane assignment for the commit DAG.
 *
 * This is the pure half of the git graph: no React, no DOM, no SVG. The backend
 * already returns commits in `--date-order` (children before parents), so the
 * layout is a single greedy pass over that list.
 *
 * Rules (PRD §10.6):
 *  1. A commit takes the lane its first child reserved for it.
 *  2. Otherwise it takes the lowest free lane.
 *  3. The first parent inherits the commit's lane, every extra parent (a merge)
 *     opens a new lane.
 *  4. A lane is freed as soon as the hash it expected is reached.
 *
 * The PRD sketch used `used.findIndex(...) ?? used.length`, which is wrong:
 * `Array#findIndex` returns -1, never undefined, so the fallback never fires.
 * The version below branches on -1 explicitly and is covered by exact-lane
 * tests in lanes.test.ts.
 */
import type { GitCommit } from "@/lib/types";

export interface LaneNode {
  hash: string;
  lane: number;
  row: number;
  /** Parents with the lane each one is rendered in. */
  parents: { hash: string; lane: number }[];
  /**
   * Lane occupancy above this row (`incoming`) and below it (`outgoing`): a
   * non-null entry is the hash that lane is still waiting for. The renderer
   * uses these snapshots to draw vertical segments without re-running the
   * algorithm, which is what keeps the virtualised graph cheap.
   *
   * Both arrays are index-aligned to lane numbers and may differ in length.
   */
  incoming?: (string | null)[];
  outgoing?: (string | null)[];
}

export interface LaneLayout {
  nodes: LaneNode[];
  laneCount: number;
}

/**
 * Assigns a lane to every commit in the list.
 *
 * @param commits commits in date order, children before their parents.
 */
export function assignLanes(commits: GitCommit[]): LaneLayout {
  /** lane index -> hash that lane is waiting for, or null when free. */
  const expected: (string | null)[] = [];
  const nodes: LaneNode[] = [];
  let laneCount = 0;

  for (let row = 0; row < commits.length; row++) {
    const commit = commits[row];
    const incoming = expected.slice();

    // 1) A child already reserved a lane for this commit: claim the leftmost.
    let lane = expected.indexOf(commit.hash);

    // 2) No reservation: take the lowest free lane, or append one.
    if (lane === -1) {
      lane = expected.indexOf(null);
      if (lane === -1) {
        lane = expected.length;
      }
    }

    // Grow sparsely so intermediate indexes stay null (= free).
    while (expected.length <= lane) {
      expected.push(null);
    }

    // 3) The first parent inherits this lane; no parents frees it.
    //
    // `parents` is defensive: the wire contract says "always an array", but a
    // null here used to crash the whole graph, so the layout tolerates it.
    const commitParents = commit.parents ?? [];
    const parents: { hash: string; lane: number }[] = [];
    const firstParent = commitParents[0];
    expected[lane] = firstParent ?? null;
    if (firstParent) {
      parents.push({ hash: firstParent, lane });
    }

    // 4) Every additional parent opens its own lane.
    for (const parent of commitParents.slice(1)) {
      let parentLane = expected.indexOf(null);
      if (parentLane === -1) {
        parentLane = expected.length;
        expected.push(parent);
      } else {
        expected[parentLane] = parent;
      }
      parents.push({ hash: parent, lane: parentLane });
    }

    laneCount = Math.max(laneCount, lane + 1);
    for (const parent of parents) {
      laneCount = Math.max(laneCount, parent.lane + 1);
    }

    nodes.push({
      hash: commit.hash,
      lane,
      row,
      parents,
      incoming,
      outgoing: expected.slice(),
    });
  }

  return { nodes, laneCount };
}

/**
 * True when `hash` is an ancestor of `start` according to the lane layout.
 * Used by the graph to decide whether a ref badge belongs to the visible tip.
 */
export function hasAncestor(
  layout: LaneLayout,
  start: string,
  ancestor: string,
  limit = 5000,
): boolean {
  const byHash = new Map(layout.nodes.map((node) => [node.hash, node]));
  const queue = [start];
  const seen = new Set<string>();
  let budget = limit;

  while (queue.length > 0 && budget-- > 0) {
    const hash = queue.shift();
    if (hash === undefined || seen.has(hash)) continue;
    seen.add(hash);
    if (hash === ancestor) return true;
    const node = byHash.get(hash);
    if (!node) continue;
    for (const parent of node.parents) queue.push(parent.hash);
  }

  return false;
}
