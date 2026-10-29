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
 */
export function deriveProjectName(repoUrl: string, now = new Date()): string {
  const basename = repoBasename(repoUrl);
  if (basename) return basename;
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `空白工作区 · ${month}-${day}`;
}

/** What the name field shows as its placeholder, so the outcome is explicit. */
export const PROJECT_NAME_PLACEHOLDER = "留空则用仓库名";
