package terran

import (
	"errors"
	"os"
	"sort"
	"strings"
)

// CaptureItem is one entry on this machine that Terran does not manage. ID is
// the item id it would carry in a catalog. Values are never included.
type CaptureItem struct {
	ID     string `json:"id"`   // ItemID for the entry as it would be named in a catalog
	Kind   string `json:"kind"` // "unmanaged_entry" | "unowned_key"
	Target string `json:"target"`
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

type CaptureResult struct {
	SchemaVersion int           `json:"schema_version"`
	Items         []CaptureItem `json:"items"`
}

// Capture lists unmanaged entries in fixed skill and file directories, unowned
// whole-file targets, and unowned top-level keys of shared JSON settings
// files. It is read-only and reads contents only of json-keys files, to list
// key names.
func Capture(target string) (CaptureResult, error) {
	if err := validateTarget(target); err != nil {
		return CaptureResult{}, err
	}
	paths, err := ResolvePaths()
	if err != nil {
		return CaptureResult{}, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return CaptureResult{}, err
	}
	receipt, err := LoadReceipt(paths, enrollment)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return CaptureResult{}, err
	}
	skip := make(map[string]bool, len(enrollment.Holds))
	for _, id := range enrollment.Holds {
		skip[id] = true
	}
	for _, p := range receipt.Projections {
		skip[ItemID("skill", p.Target, p.Skill)] = true
	}
	for _, m := range receipt.Managed {
		skip[ItemID(m.Kind, m.Target, m.Name)] = true
	}
	for _, k := range receipt.JSONKeys {
		skip[ItemID("json-keys", k.Target, k.Key)] = true
	}
	result := CaptureResult{SchemaVersion: SchemaVersion, Items: []CaptureItem{}}
	add := func(spec TargetSpec, kind, name, reason string) {
		id := ItemID(spec.Kind, spec.ID, name)
		if !skip[id] {
			result.Items = append(result.Items, CaptureItem{ID: id, Kind: kind, Target: spec.ID, Name: name, Reason: reason})
		}
	}
	for _, spec := range targetSpecs {
		if target != "all" && spec.Group != target {
			continue
		}
		// An empty name yields the skill root, file directory, or whole file.
		destination, err := spec.Dest(paths, "")
		if err != nil {
			return CaptureResult{}, err
		}
		switch spec.Kind {
		case "skill", "file":
			for _, name := range captureNames(destination, spec) {
				add(spec, "unmanaged_entry", name, "")
			}
		case "instruction", "config":
			if _, err := os.Lstat(destination); err == nil {
				add(spec, "unmanaged_entry", "", "")
			}
		case "json-keys":
			data, _, err := readJSONSettings(destination)
			if data == nil && err == nil {
				continue
			}
			var object map[string]any
			if err == nil {
				object, err = decodeJSONObject(data)
			}
			if err != nil {
				add(spec, "unmanaged_entry", "", "unreadable json")
				continue
			}
			for key := range object {
				// Keys that cannot form an item id cannot be catalogued.
				if _, _, name, err := ParseItemID(ItemID("json-keys", spec.ID, key)); err == nil && name == key {
					add(spec, "unowned_key", key, "")
				}
			}
		}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].ID < result.Items[j].ID })
	return result, nil
}

// captureNames lists the direct entries of dir that are candidates for
// management by spec: valid names, not hidden, not Naru's, and for file
// targets regular files or symlinks with an allowed extension. A root that is
// missing or not a real directory yields nothing.
func captureNames(dir string, spec TargetSpec) []string {
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "naru-") {
			continue
		}
		if spec.Kind == "skill" {
			if !skillNamePattern.MatchString(name) {
				continue
			}
		} else if entry.IsDir() || validateFileName(spec, name) != nil {
			continue
		}
		names = append(names, name)
	}
	return names
}
