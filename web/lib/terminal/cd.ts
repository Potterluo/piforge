/**
 * Building the `cd` that "reveal in terminal" types for the user.
 *
 * The path comes from the file tree, so it is repository content rather than
 * user input — but a repository can contain a file named `a; rm -rf ~`. Since
 * the command is typed into a real shell over a real PTY, the only safe answer
 * is to refuse to type anything that is not obviously inert; the caller falls
 * back to showing the path.
 */

/** Characters a repository path may contain before we refuse to cd into it. */
const SAFE_PATH = /^[A-Za-z0-9._@+/-]+$/;

/**
 * Returns a shell command that changes into the file's directory, or null when
 * the path is too exotic to type safely.
 */
export function buildCdCommand(filePath: string): string | null {
  const trimmed = filePath.trim();
  if (!trimmed) return null;
  if (trimmed.startsWith("/") || /^[A-Za-z]:/.test(trimmed)) return null;
  if (trimmed.includes("..")) return null;
  if (!SAFE_PATH.test(trimmed)) return null;

  // `..` is already rejected, so the directory always stays inside the checkout.
  const slash = trimmed.lastIndexOf("/");
  const dir = slash >= 0 ? trimmed.slice(0, slash) : ".";
  if (!dir || !SAFE_PATH.test(dir)) return null;

  // Double quotes are safe on both PowerShell and POSIX shells for a path made
  // of the characters checked above, and quoting keeps `-o` from being read as
  // an option by cd.
  return dir === "." ? "cd ." : `cd "${dir}"`;
}

/** Turns a change-directory command into what a PTY expects (Enter included). */
export function asPTYInput(command: string): string {
  return `${command}\r`;
}
