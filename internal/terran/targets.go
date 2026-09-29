package terran

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type TargetSpec struct {
	ID       string
	Kind     string
	Group    string
	Dest     func(p Paths, name string) (string, error)
	Mode     os.FileMode
	Validate func(data []byte) error
	Exts     []string // allowed name extensions for file targets; nil allows any
}

var targetSpecs = []TargetSpec{
	{
		ID:    "agents",
		Kind:  "skill",
		Group: "agents",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.Home, ".agents", "skills", name), nil
		},
	},
	{
		ID:    "claude",
		Kind:  "skill",
		Group: "claude",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.Home, ".claude", "skills", name), nil
		},
	},
	{
		ID:    "claude-global",
		Kind:  "instruction",
		Group: "claude",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.Home, ".claude", "CLAUDE.md"), nil
		},
		Mode: 0o644,
	},
	{
		ID:    "opencode-global",
		Kind:  "instruction",
		Group: "opencode",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.ConfigBase, "opencode", "AGENTS.md"), nil
		},
		Mode: 0o644,
	},
	{
		ID:    "codex-global",
		Kind:  "instruction",
		Group: "codex",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.CodexHome, "AGENTS.md"), nil
		},
		Mode: 0o644,
	},
	{
		ID:    "opencode-config",
		Kind:  "config",
		Group: "opencode",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.ConfigBase, "opencode", "opencode.json"), nil
		},
		Mode:     0o600,
		Validate: validateOpenCodeConfig,
	},
	{
		ID:    "naru-runtime",
		Kind:  "config",
		Group: "opencode",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.ConfigBase, "opencode", "naru-runtime.json"), nil
		},
		Mode:     0o600,
		Validate: validateOpenCodeConfig,
	},
	{
		ID:    "mise-config",
		Kind:  "config",
		Group: "mise",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.ConfigBase, "mise", "config.toml"), nil
		},
		Mode:     0o644,
		Validate: validateTextConfig,
	},
	{
		ID:    "mise-lock",
		Kind:  "config",
		Group: "mise",
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(p.ConfigBase, "mise", "mise.lock"), nil
		},
		Mode:     0o644,
		Validate: validateTextConfig,
	},
	fileTarget("claude-agent", "claude", 0o644, []string{".md"}, func(p Paths) string { return filepath.Join(p.Home, ".claude", "agents") }),
	fileTarget("claude-command", "claude", 0o644, []string{".md"}, func(p Paths) string { return filepath.Join(p.Home, ".claude", "commands") }),
	fileTarget("claude-hook", "claude", 0o755, nil, func(p Paths) string { return filepath.Join(p.Home, ".claude", "hooks") }),
	fileTarget("opencode-plugin", "opencode", 0o644, []string{".js", ".ts"}, func(p Paths) string { return filepath.Join(p.ConfigBase, "opencode", "plugins") }),
	fileTarget("opencode-tool", "opencode", 0o644, []string{".ts"}, func(p Paths) string { return filepath.Join(p.ConfigBase, "opencode", "tool") }),
	fileTarget("opencode-command", "opencode", 0o644, []string{".md"}, func(p Paths) string { return filepath.Join(p.ConfigBase, "opencode", "command") }),
}

// fileTarget declares a named-file target: its destination joins a fixed
// directory with a name validated by validateFileName.
func fileTarget(id, group string, mode os.FileMode, exts []string, dir func(Paths) string) TargetSpec {
	return TargetSpec{
		ID:    id,
		Kind:  "file",
		Group: group,
		Dest: func(p Paths, name string) (string, error) {
			return filepath.Join(dir(p), name), nil
		},
		Mode: mode,
		Exts: exts,
	}
}

var fileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func validateFileName(spec TargetSpec, name string) error {
	if !fileNamePattern.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid %s file name %q", spec.ID, name)
	}
	if spec.Exts == nil {
		return nil
	}
	for _, ext := range spec.Exts {
		if strings.HasSuffix(name, ext) {
			return nil
		}
	}
	return fmt.Errorf("%s file name %q must end in %s", spec.ID, name, strings.Join(spec.Exts, " or "))
}

func lookupTarget(kind, id string) (TargetSpec, bool) {
	for _, spec := range targetSpecs {
		if spec.Kind == kind && spec.ID == id {
			return spec, true
		}
	}
	return TargetSpec{}, false
}

func targetsInGroup(group string) []TargetSpec {
	var result []TargetSpec
	for _, spec := range targetSpecs {
		if spec.Group == group {
			result = append(result, spec)
		}
	}
	return result
}

func targetGroups() []string {
	var groups []string
	seen := make(map[string]bool)
	for _, spec := range targetSpecs {
		if !seen[spec.Group] {
			groups = append(groups, spec.Group)
			seen[spec.Group] = true
		}
	}
	return groups
}

func TargetGroups() []string {
	return append([]string(nil), targetGroups()...)
}

func ValidateTarget(target string) error {
	if target != "all" && len(targetsInGroup(target)) == 0 {
		groups := targetGroups()
		groupText := strings.Join(groups, ", ")
		if len(groups) > 1 {
			groupText = strings.Join(groups[:len(groups)-1], ", ") + ", or " + groups[len(groups)-1]
		}
		return fmt.Errorf("target must be all, %s", groupText)
	}
	return nil
}
