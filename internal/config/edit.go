package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Starter is the commented file `shelp config edit` creates when none
// exists. Everything in it is a comment, so it changes nothing.
const Starter = `# shelp config. Every key is optional; see "shelp help config".

# Fallback chain, tried in order; the first success wins.
# Change it interactively with "shelp config chain".
# providers = ["shortcuts:apple-pro", "fm"]

# template = "v4"
# context = ["os", "shell"]
# cooldown = "30m"
`

// ProvidersLine renders ids as a top-level providers assignment.
func ProvidersLine(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = fmt.Sprintf("%q", id)
	}
	return "providers = [" + strings.Join(q, ", ") + "]"
}

// SetProviders returns src with its top-level providers assignment
// replaced by ids (a multi-line array included), or, if there is none, one
// inserted above the first [table] header (below any comment block
// attached to it), or appended when there are no tables. Every other byte
// of src is left alone.
func SetProviders(src string, ids []string) string {
	newLine := ProvidersLine(ids)
	lines := strings.SplitAfter(src, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	depth := 0
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if depth == 0 {
			if strings.HasPrefix(t, "[") {
				return insertBefore(lines, i, newLine)
			}
			if k, ok := strings.CutPrefix(t, "providers"); ok && strings.HasPrefix(strings.TrimSpace(k), "=") {
				end := i
				d := scanDepth(lines[i][strings.Index(lines[i], "=")+1:], 0)
				for d > 0 && end+1 < len(lines) {
					end++
					d = scanDepth(lines[end], d)
				}
				nl := newLine + "\n"
				if !strings.HasSuffix(lines[end], "\n") {
					nl = newLine
				}
				return strings.Join(lines[:i], "") + nl + strings.Join(lines[end+1:], "")
			}
		}
		depth = scanDepth(lines[i], depth)
	}
	out := strings.Join(lines, "")
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + newLine + "\n"
}

func insertBefore(lines []string, i int, newLine string) string {
	// Keep a comment block attached to the header with the header.
	at := i
	for at > 0 && strings.HasPrefix(strings.TrimSpace(lines[at-1]), "#") {
		at--
	}
	ins := newLine + "\n"
	if at > 0 && strings.TrimSpace(lines[at-1]) != "" {
		ins = newLine + "\n\n"
	} else {
		ins += "\n"
	}
	return strings.Join(lines[:at], "") + ins + strings.Join(lines[at:], "")
}

// scanDepth returns the bracket depth after s, starting at depth, ignoring
// brackets inside strings and comments.
func scanDepth(s string, depth int) int {
	var quote rune
	esc := false
	for _, r := range s {
		switch {
		case quote != 0:
			switch {
			case esc:
				esc = false
			case r == '\\' && quote == '"':
				esc = true
			case r == quote:
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#':
			return depth
		case r == '[' || r == '{':
			depth++
		case r == ']' || r == '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth
}

// SaveProviders edits the file at path with SetProviders and writes it
// atomically (temp file + rename), creating parent directories. A missing
// file is created. The result must parse, or nothing is written.
func SaveProviders(path string, ids []string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out := SetProviders(string(data), ids)
	if _, err := Parse(out); err != nil {
		return fmt.Errorf("not writing %s: %w", path, err)
	}
	return WriteAtomic(path, []byte(out))
}

// WriteAtomic writes data to path via a temp file in the same directory
// and a rename, keeping an existing file's mode.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	f, err := os.CreateTemp(dir, ".shelp-config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
