package terran

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type TargetSpec struct {
	ID       string
	Kind     string
	Group    string
	Dest     func(p Paths, name string) (string, error)
	Mode     os.FileMode
	Validate func(data []byte) error
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
