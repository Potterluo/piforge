/**
 * A project name the user does not have to invent.
 *
 * A repository address already carries a machine-readable name: its basename.
 * Asking for a second one at creation time is how projects end up called
 * "test2" and never renamed, so an empty field derives the name instead.
 */

/** Last path segment of a repository address, `.git` removed. */
export function repoBasename(repoUrl: string): string {
  let value = repoUrl.trim();
  if (!value) return "";
  // A query or fragment is not part of the repository's name.
  const cut = value.search(/[?#]/);
  if (cut >= 0) value = value.slice(0, cut);
  value = value.replace(/\/+$/, "");
  if (value.toLowerCase().endsWith(".git")) value = value.slice(0, -4);
  // https://host/a/b, git@host:a/b, C:\repos\b and /repos/b all end at one of
  // these separators. Taking the *last* one handles each form without a parser.
  const index = Math.max(
    value.lastIndexOf("/"),
    value.lastIndexOf("\\"),
    value.lastIndexOf(":"),
  );
  const name = value.slice(index + 1).trim();
  if (!name || name === "." || name === "..") return "";
  return name;
}

/**
 * The name to create a project with: the repository's basename, or a dated
 * blank workspace. `now` is injectable so the date in a test is not today.
 *
 * The date is formatted in UTC because the server derives the same name from its
 * own clock and stores it. Formatting it locally promised 「空白工作区 · 10-30」
 * in a +08:00 browser while the API created 「空白工作区 · 10-29」 — a preview that
 * described a name the server was not going to use (QA-16 minor).
 */
export function deriveProjectName(repoUrl: string, now = new Date()): string {
  const basename = repoBasename(repoUrl);
  if (basename) return basename;
  const month = String(now.getUTCMonth() + 1).padStart(2, "0");
  const day = String(now.getUTCDate()).padStart(2, "0");
  return `空白工作区 · ${month}-${day}`;
}

/**
 * The first free name for a project, mirroring the API's allocation.
 *
 * A derived name is the product's job to make unique: two repositories both
 * called `frontend` are a normal thing to have, so the second becomes
 * `frontend-2` rather than a 409 the user cannot act on. Comparison is
 * case-insensitive exactly like `GET /v1/projects` names are compared on the
 * server; the server remains the gate, this is the preview.
 */
export function freeProjectName(base: string, taken: Iterable<string>): string {
  const used = new Set<string>();
  for (const name of taken) used.add(name.trim().toLowerCase());

  const stem = base.trim();
  if (!stem) return "";
  // The server stops at a thousand; a preview that loops forever on a corrupt
  // list would hang the dialog, so it stops at the same place.
  for (let suffix = 1; suffix <= 1000; suffix += 1) {
    const candidate = suffix === 1 ? stem : `${stem}-${suffix}`;
    if (!used.has(candidate.toLowerCase())) return candidate;
  }
  return stem;
}

/** What the name field shows as its placeholder, so the outcome is explicit. */
export const PROJECT_NAME_PLACEHOLDER = "留空则用仓库名";
