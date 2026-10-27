package fsx

import (
	"mime"
	"path/filepath"
	"strings"
)

// languageByExt maps a file extension to a Shiki language id (PRD FR-7.1).
// Shiki ids are lowercase, e.g. "typescript", "go", "bash", "md".
var languageByExt = map[string]string{
	".go":         "go",
	".ts":         "typescript",
	".tsx":        "tsx",
	".mts":        "typescript",
	".cts":        "typescript",
	".js":         "javascript",
	".jsx":        "jsx",
	".mjs":        "javascript",
	".cjs":        "javascript",
	".py":         "python",
	".pyi":        "python",
	".rs":         "rust",
	".java":       "java",
	".kt":         "kotlin",
	".kts":        "kotlin",
	".scala":      "scala",
	".c":          "c",
	".h":          "c",
	".cc":         "cpp",
	".cpp":        "cpp",
	".cxx":        "cpp",
	".hpp":        "cpp",
	".hh":         "cpp",
	".m":          "objective-c",
	".mm":         "objective-cpp",
	".cs":         "csharp",
	".rb":         "ruby",
	".php":        "php",
	".swift":      "swift",
	".dart":       "dart",
	".lua":        "lua",
	".pl":         "perl",
	".r":          "r",
	".jl":         "julia",
	".ex":         "elixir",
	".exs":        "elixir",
	".erl":        "erlang",
	".hs":         "haskell",
	".clj":        "clojure",
	".zig":        "zig",
	".nim":        "nim",
	".sh":         "bash",
	".bash":       "bash",
	".zsh":        "zsh",
	".fish":       "fish",
	".ps1":        "powershell",
	".psm1":       "powershell",
	".bat":        "bat",
	".cmd":        "bat",
	".json":       "json",
	".jsonc":      "jsonc",
	".json5":      "json5",
	".yaml":       "yaml",
	".yml":        "yaml",
	".toml":       "toml",
	".ini":        "ini",
	".cfg":        "ini",
	".conf":       "ini",
	".env":        "dotenv",
	".md":         "markdown",
	".mdx":        "mdx",
	".rst":        "rst",
	".html":       "html",
	".htm":        "html",
	".vue":        "vue",
	".svelte":     "svelte",
	".astro":      "astro",
	".css":        "css",
	".scss":       "scss",
	".sass":       "sass",
	".less":       "less",
	".sql":        "sql",
	".graphql":    "graphql",
	".gql":        "graphql",
	".proto":      "proto",
	".tf":         "terraform",
	".tfvars":     "terraform",
	".hcl":        "hcl",
	".dockerfile": "dockerfile",
	".make":       "make",
	".mk":         "make",
	".cmake":      "cmake",
	".diff":       "diff",
	".patch":      "diff",
	".xml":        "xml",
	".svg":        "xml",
	".csv":        "csv",
	".tsv":        "tsv",
	".txt":        "text",
	".log":        "log",
	".lock":       "text",
	".wasm":       "wasm",
}

// languageByName handles extension-less files that still have a highlighter.
var languageByName = map[string]string{
	"dockerfile":     "dockerfile",
	"makefile":       "make",
	"gnumakefile":    "make",
	"cmakelists.txt": "cmake",
	"go.mod":         "go",
	"go.sum":         "text",
	"cargo.toml":     "toml",
	"package.json":   "json",
	"tsconfig.json":  "jsonc",
	".gitignore":     "text",
	".gitattributes": "text",
	".editorconfig":  "ini",
	".npmrc":         "ini",
	".env":           "dotenv",
	"procfile":       "text",
	"license":        "text",
	"readme":         "markdown",
}

// GuessLanguage returns a Shiki language id for the file name, or "text".
func GuessLanguage(name string) string {
	base := strings.ToLower(filepath.Base(name))
	if lang, ok := languageByName[base]; ok {
		return lang
	}
	ext := strings.ToLower(filepath.Ext(base))
	if lang, ok := languageByExt[ext]; ok {
		return lang
	}
	// Files such as "Dockerfile.dev" or "Makefile.am".
	for suffix, lang := range languageByName {
		if strings.HasPrefix(base, suffix+".") {
			return lang
		}
	}
	return "text"
}

// GuessMime returns the MIME type for the file name, using the extension table
// first and the OS registry second. Binary files default to
// application/octet-stream, text files to text/plain.
func GuessMime(name string, binary bool) string {
	ext := strings.ToLower(filepath.Ext(name))
	if t := mime.TypeByExtension(ext); t != "" {
		if i := strings.IndexByte(t, ';'); i > 0 {
			t = strings.TrimSpace(t[:i])
		}
		return t
	}
	switch ext {
	case ".go":
		return "text/x-go"
	case ".md", ".mdx", ".markdown":
		return "text/markdown"
	case ".ts", ".mts", ".cts":
		return "text/typescript"
	case ".tsx":
		return "text/typescript"
	case ".js", ".mjs", ".cjs":
		return "text/javascript"
	case ".jsx":
		return "text/javascript"
	case ".py":
		return "text/x-python"
	case ".rs":
		return "text/x-rust"
	case ".yaml", ".yml":
		return "text/yaml"
	case ".toml":
		return "text/x-toml"
	case ".sql":
		return "text/x-sql"
	case ".sh", ".bash", ".zsh":
		return "text/x-shellscript"
	case ".ps1":
		return "text/x-powershell"
	case ".diff", ".patch":
		return "text/x-diff"
	}
	if binary {
		return "application/octet-stream"
	}
	return "text/plain"
}
