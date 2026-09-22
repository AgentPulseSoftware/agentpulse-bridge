package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// deriveProject implements BR-13: the project key is the git repository
// root of cwd (found by walking up for a ".git" entry — never by shelling
// out to git), or cwd itself when it isn't inside a work tree. Only a hash
// of that key and a truncated display name ever leave the machine; the key
// itself never does.
func deriveProject(cwd string) Project {
	key := projectKey(cwd)
	sum := sha256.Sum256([]byte(key))
	return Project{
		KeyHash: hex.EncodeToString(sum[:]),
		Name:    projectName(key),
	}
}

// projectKey walks cwd's ancestors looking for a ".git" entry (a directory
// for a normal clone, or a file for a worktree/submodule — either one
// existing is enough) and returns the first directory that has one, or cwd
// unchanged if none of its ancestors do.
func projectKey(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return cwd
}

const maxProjectNameRunes = 64

// projectName returns key's base name, truncated to what the schema
// allows and falling back to "project" (BR-13) whenever the base name
// would be empty or would itself contain a path separator — filepath.Base
// of "", ".", "/", or a trailing-separator path can all produce exactly
// that, and the schema's project.name pattern forbids "/" and "\"
// specifically so this field can never itself be or contain a path.
func projectName(key string) string {
	base := filepath.Base(key)
	if base == "" || base == "." || strings.ContainsAny(base, `/\`) {
		return "project"
	}
	r := []rune(base)
	if len(r) > maxProjectNameRunes {
		return string(r[:maxProjectNameRunes])
	}
	return base
}
