// Package prompt renders the system and user prompts sent to providers.
//
// Templates are named and versioned so the benchmark can compare them; the
// CLI uses Default unless config or --template picks another. A template
// only sees what Context carries, and Context only carries what the user
// allowed (see Fields): the cwd and tool list are opt-in because they leave
// the machine when the provider is a cloud model.
package prompt

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/ericandrechek/shelp/internal/provider"
)

// Context describes the environment the command will run in.
type Context struct {
	OS       string   // "macOS 27.0", "Ubuntu 24.04", "Linux"
	Userland string   // "BSD" (macOS), "GNU", "busybox"
	Shell    string   // "zsh", "bash", "fish", "sh", "pwsh"
	Cwd      string   // only when the "cwd" field is enabled
	Tools    []string // notable optional tools on PATH; only with "tools"
}

// Context fields a user can enable. DefaultContext is used when config and
// flags are silent.
var (
	AllFields      = []string{"os", "shell", "cwd", "tools"}
	DefaultContext = []string{"os", "shell"}
)

// Template renders prompts for a mode.
type Template struct {
	Name        string
	Description string
	System      func(provider.Mode, Context) string
	User        func(provider.Mode, string, Context) string
}

var templates = map[string]Template{}

// Register adds a template; the benchmark's experimental variants live next
// to the default one in templates.go.
func Register(t Template) { templates[t.Name] = t }

// Get returns a template by name ("" means Default).
func Get(name string) (Template, error) {
	if name == "" {
		name = Default
	}
	t, ok := templates[name]
	if !ok {
		return Template{}, fmt.Errorf("unknown template %q (have: %s)", name, strings.Join(Names(), ", "))
	}
	return t, nil
}

// Names lists registered templates.
func Names() []string {
	var ns []string
	for n := range templates {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return ns
}

// Render builds the provider request for input under template t.
func Render(t Template, mode provider.Mode, input string, c Context) provider.Request {
	return provider.Request{Mode: mode, System: t.System(mode, c), Prompt: t.User(mode, input, c)}
}

// Detect fills a Context with the requested fields. shell is the target
// shell as known by the caller (shell integration passes it); empty falls
// back to $SHELP_SHELL, then $SHELL.
func Detect(fields []string, shell string) Context {
	want := map[string]bool{}
	for _, f := range fields {
		want[f] = true
	}
	var c Context
	if want["os"] {
		c.OS, c.Userland = detectOS()
	}
	if want["shell"] {
		c.Shell = detectShell(shell)
	}
	if want["cwd"] {
		c.Cwd, _ = os.Getwd()
	}
	if want["tools"] {
		c.Tools = detectTools()
	}
	return c
}

func detectShell(shell string) string {
	for _, s := range []string{shell, os.Getenv("SHELP_SHELL"), os.Getenv("SHELL")} {
		if s = strings.TrimSpace(s); s != "" {
			s = s[strings.LastIndex(s, "/")+1:]
			return strings.TrimPrefix(s, "-")
		}
	}
	return "sh"
}

var plistVersion = regexp.MustCompile(`<key>ProductVersion</key>\s*<string>([^<]+)</string>`)

func detectOS() (name, userland string) {
	switch runtime.GOOS {
	case "darwin":
		// Reading the plist is ~100x faster than exec'ing sw_vers.
		if b, err := os.ReadFile("/System/Library/CoreServices/SystemVersion.plist"); err == nil {
			if m := plistVersion.FindSubmatch(b); m != nil {
				return "macOS " + string(m[1]), "BSD"
			}
		}
		return "macOS", "BSD"
	case "linux":
		name := "Linux"
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
					name = strings.Trim(v, `"`)
				}
			}
		}
		userland := "GNU"
		if t, err := os.Readlink("/bin/sh"); err == nil && strings.Contains(t, "busybox") {
			userland = "busybox"
		}
		return name, userland
	default:
		return runtime.GOOS, ""
	}
}

// notableTools are optional tools worth telling the model about because
// they change the best answer (GNU variants on macOS, modern replacements).
var notableTools = []string{
	"brew", "apt", "dnf", "pacman", "nix",
	"gsed", "gawk", "gfind", "ggrep", "gdate", "gstat", "gtimeout",
	"rg", "fd", "fzf", "jq", "yq", "bat", "eza", "sd", "dust", "duf", "btop", "htop",
	"git", "gh", "docker", "podman", "kubectl", "ffmpeg", "imagemagick", "magick",
	"uv", "pnpm", "npm", "bun", "cargo", "go", "python3", "node",
	"tailscale", "aws", "gcloud", "terraform",
}

func detectTools() []string {
	var have []string
	for _, t := range notableTools {
		if _, ok := provider.LookPath(t); ok {
			have = append(have, t)
		}
	}
	return have
}
