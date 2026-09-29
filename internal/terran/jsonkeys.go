package terran

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Shared JSON settings files (json-keys targets) are owned per top-level key:
// Terran sets or deletes only the keys it owns and preserves every other key.

const settingsLimit = 4 << 20

func jsonKeysDestination(paths Paths, target string) (string, error) {
	spec, ok := lookupTarget("json-keys", target)
	if !ok {
		return "", fmt.Errorf("unsupported json-keys target %q", target)
	}
	return spec.Dest(paths, "")
}

// canonicalJSON encodes a decoded value with sorted object keys, no
// insignificant whitespace, and no HTML escaping.
func canonicalJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func canonicalValues(object map[string]any) (map[string]json.RawMessage, error) {
	values := make(map[string]json.RawMessage, len(object))
	for key, value := range object {
		canonical, err := canonicalJSON(value)
		if err != nil {
			return nil, err
		}
		values[key] = canonical
	}
	return values, nil
}

// jsonKeyValuesFromSource validates a json-keys source: a strict, public JSON
// object holding only the top-level keys Terran owns. Keys must form valid item
// ids so each one can be held.
func jsonKeyValuesFromSource(target string, data []byte) (map[string]json.RawMessage, error) {
	spec, _ := lookupTarget("json-keys", target)
	if err := spec.Validate(data); err != nil {
		return nil, err
	}
	object, err := decodeJSONObject(data)
	if err != nil {
		return nil, err
	}
	for key := range object {
		if _, _, name, err := ParseItemID(ItemID("json-keys", target, key)); err != nil || name != key {
			return nil, fmt.Errorf("key %q cannot be used in an item id", key)
		}
	}
	return canonicalValues(object)
}

// jsonKeysCatalog returns the catalog declaring a json-keys target, if any.
func jsonKeysCatalog(catalogs Catalogs, target string) (LoadedManifest, bool) {
	for _, loaded := range catalogs.list() {
		if _, ok := loaded.JSONKeySources[target]; ok {
			return loaded, true
		}
	}
	return LoadedManifest{}, false
}

// jsonSettings is a settings file as observed by the plan.
type jsonSettings struct {
	exists  bool
	hash    string                     // sha256 of the whole file; "" when missing
	values  map[string]json.RawMessage // canonical value of each top-level key
	blocked string                     // file-level collision reason, if any
}

func inspectJSONSettings(destination string) jsonSettings {
	if _, err := os.Lstat(filepath.Dir(destination)); errors.Is(err, os.ErrNotExist) {
		return jsonSettings{blocked: "parent directory missing"}
	}
	if err := validateInstructionParent(destination); err != nil {
		return jsonSettings{blocked: err.Error()}
	}
	data, _, err := readJSONSettings(destination)
	if err != nil {
		return jsonSettings{blocked: err.Error()}
	}
	if data == nil {
		return jsonSettings{}
	}
	file := jsonSettings{exists: true, hash: hashBytes(data)}
	object, err := decodeJSONObject(data)
	if err != nil {
		file.blocked = "settings file " + err.Error()
		return file
	}
	if file.values, err = canonicalValues(object); err != nil {
		file.blocked = err.Error()
	}
	return file
}

// readJSONSettings reads a settings file without following symlinks; a
// missing file returns nil data.
func readJSONSettings(destination string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("settings file %s is a symlink", destination)
	}
	return readTrustedFile(destination, "settings file", settingsLimit, 0)
}

// classifyJSONKey plans one top-level key. desired is nil when the key is
// owned but no longer in the catalog.
func classifyJSONKey(file jsonSettings, key string, desired json.RawMessage, prior ReceiptJSONKey, owned bool) (string, string) {
	if file.blocked != "" {
		return "blocked_collision", file.blocked
	}
	current, present := file.values[key]
	if owned {
		if !present {
			return "blocked_drift", "receipt-owned key is missing"
		}
		if hashBytes(current) != prior.AppliedHash {
			return "blocked_drift", "receipt-owned key changed"
		}
		switch {
		case desired == nil && prior.Origin == "created":
			return "remove", "created key is no longer in the catalog; delete it"
		case desired == nil:
			return "release", "adopted key is no longer in the catalog; drop ownership and keep its value"
		case hashBytes(desired) == prior.AppliedHash:
			return "noop", ""
		}
		return "update", "catalog value changed"
	}
	if !present {
		if !file.exists {
			return "create", "settings file will be created"
		}
		return "create", ""
	}
	if bytes.Equal(current, desired) {
		return "adopt", "existing key already has the catalog value"
	}
	return "blocked_collision", "key exists with a different value"
}

// jsonTargetsToVerify lists json-keys targets whose plan depends on the
// observed file: any action other than noop, held or excluded.
func jsonTargetsToVerify(plan PlanResult) []string {
	var targets []string
	seen := map[string]bool{}
	for _, action := range plan.Actions {
		if action.Kind == "json-keys" && action.Action != "noop" && !inert(action) && !seen[action.Target] {
			seen[action.Target] = true
			targets = append(targets, action.Target)
		}
	}
	return targets
}

func planChanged(destination string) error {
	return Coded(CodePlanChanged, "run terran plan again", fmt.Errorf("%s changed since it was planned", destination))
}

// verifyJSONSettings re-reads a settings file and requires the exact bytes the
// plan observed.
func verifyJSONSettings(paths Paths, catalogs Catalogs, plan PlanResult, target string) ([]byte, os.FileMode, error) {
	destination, err := jsonKeysDestination(paths, target)
	if err != nil {
		return nil, 0, err
	}
	if loaded, ok := jsonKeysCatalog(catalogs, target); ok {
		if _, err := readVerifiedManagedSource(loaded.Repository, "json-keys", target, loaded.JSONKeySources[target], loaded.JSONKeyHashes[target]); err != nil {
			return nil, 0, fmt.Errorf("json-keys source changed during apply: %w", err)
		}
	}
	data, mode, err := readJSONSettings(destination)
	planned, inspected := plan.jsonFiles[target]
	if err != nil || !inspected || (data == nil) != (planned == "") || (data != nil && hashBytes(data) != planned) {
		return nil, 0, planChanged(destination)
	}
	return data, mode, nil
}

// mutateJSONKeys rewrites one settings file with every planned key change for
// its target. The returned rollback restores the previous bytes.
func mutateJSONKeys(paths Paths, catalogs Catalogs, plan PlanResult, target string) (instructionRollback, error) {
	destination, err := jsonKeysDestination(paths, target)
	if err != nil {
		return instructionRollback{}, err
	}
	rollback := instructionRollback{destination: destination}
	data, mode, err := verifyJSONSettings(paths, catalogs, plan, target)
	if err != nil {
		return rollback, err
	}
	if err := validateInstructionParent(destination); err != nil {
		return rollback, err
	}
	raw := map[string]json.RawMessage{}
	if data != nil {
		rollback.before, rollback.mode, rollback.existed = data, mode, true
		if _, err := decodeJSONObject(data); err != nil {
			return rollback, planChanged(destination)
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return rollback, err
		}
	}
	loaded, _ := jsonKeysCatalog(catalogs, target)
	changed := false
	for _, action := range plan.Actions {
		if action.Kind != "json-keys" || action.Target != target {
			continue
		}
		switch action.Action {
		case "create", "update":
			value, ok := loaded.JSONKeyValues[target][action.Name]
			if !ok {
				return rollback, fmt.Errorf("json key %s is no longer in the catalog", action.ID)
			}
			raw[action.Name], changed = value, true
		case "remove":
			delete(raw, action.Name)
			changed = true
		}
	}
	if !changed {
		return rollback, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(raw); err != nil {
		return rollback, err
	}
	if !rollback.existed {
		spec, _ := lookupTarget("json-keys", target)
		mode = spec.Mode
	}
	mutation, err := atomicInstructionFile(destination, buf.Bytes(), mode, !rollback.existed)
	if mutation.mutated {
		rollback.afterHash = hashBytes(buf.Bytes())
		rollback.afterInfo = mutation.info
	}
	return rollback, err
}
