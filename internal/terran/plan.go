package terran

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

func validateTarget(target string) error {
	return ValidateTarget(target)
}

func selectedSkill(filter, target string) bool {
	spec, ok := lookupTarget("skill", target)
	return filter == "all" || (ok && spec.Group == filter)
}

func selectedManaged(filter, kind, target string) bool {
	spec, ok := lookupTarget(kind, target)
	return filter == "all" || (ok && spec.Group == filter)
}

// managedItem is one desired whole-file item: an instruction, a config, or a
// named file. Instructions and configs have an empty Name.
type managedItem struct {
	Kind, Target, Name, Source, Hash string
	Platforms                        []string
}

func (item managedItem) id() string { return ItemID(item.Kind, item.Target, item.Name) }

func (loaded LoadedManifest) managedItems() []managedItem {
	var items []managedItem
	for _, instruction := range loaded.Manifest.Instructions {
		items = append(items, managedItem{"instruction", instruction.Target, "", loaded.InstructionSources[instruction.Target], loaded.InstructionHashes[instruction.Target], instruction.Platforms})
	}
	for _, config := range loaded.Manifest.Configs {
		items = append(items, managedItem{"config", config.Target, "", loaded.ConfigSources[config.Target], loaded.ConfigHashes[config.Target], config.Platforms})
	}
	for _, file := range loaded.Manifest.Files {
		id := ItemID("file", file.Target, file.Name)
		items = append(items, managedItem{"file", file.Target, file.Name, loaded.FileSources[id], loaded.FileHashes[id], file.Platforms})
	}
	return items
}

func managedLabel(target, name string) string {
	if name == "" {
		return target
	}
	return target + "/" + name
}

// LoadReceipt validates the receipt against the enrollment: every entry must
// belong to the primary catalog or the enrolled overlay, with its source inside
// that catalog's repository.
func LoadReceipt(paths Paths, enrollment Enrollment) (receipt Receipt, err error) {
	defer func() {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			err = Coded(CodeReceiptInvalid, nextState, err)
		}
	}()
	data, _, err := readTrustedFile(paths.Receipt, "receipt", 4<<20, 0o600)
	if err != nil {
		return Receipt{}, err
	}
	receipt, err = decodeReceipt(data)
	if err != nil {
		return Receipt{}, err
	}
	if receipt.SchemaVersion != SchemaVersion || receipt.RepositoryID == "" || !filepath.IsAbs(receipt.RepositoryPath) || filepath.Clean(receipt.RepositoryPath) != receipt.RepositoryPath {
		return Receipt{}, fmt.Errorf("invalid receipt repository")
	}
	catalogRepository := func(catalog string) (string, bool) {
		switch {
		case catalog == receipt.RepositoryID:
			return receipt.RepositoryPath, true
		case enrollment.OverlayID != "" && catalog == enrollment.OverlayID:
			return enrollment.OverlayPath, true
		}
		return "", false
	}
	seen := map[string]bool{}
	for _, p := range receipt.Projections {
		repository, known := catalogRepository(p.Catalog)
		validStrategy := (p.Strategy == "copy" && validHash(p.AppliedHash) && (p.Origin == "created" || p.Origin == "adopted")) ||
			(p.Strategy == "symlink" && p.AppliedHash == "" && p.Origin == "")
		if !known || !skillNamePattern.MatchString(p.Skill) || !validStrategy || !filepath.IsAbs(p.Source) {
			return Receipt{}, fmt.Errorf("invalid receipt projection")
		}
		if _, ok := lookupTarget("skill", p.Target); !ok {
			return Receipt{}, fmt.Errorf("invalid receipt projection")
		}
		destination, destinationErr := skillDestination(paths, p.Target, p.Skill)
		if destinationErr != nil || p.Destination != destination {
			return Receipt{}, fmt.Errorf("unsafe receipt destination for %s/%s", p.Skill, p.Target)
		}
		if filepath.Clean(p.Source) != p.Source || !contained(repository, p.Source) {
			return Receipt{}, fmt.Errorf("unsafe receipt source for %s/%s", p.Skill, p.Target)
		}
		if canonical, err := filepath.EvalSymlinks(p.Source); err == nil && canonical != p.Source {
			return Receipt{}, fmt.Errorf("unsafe receipt source for %s/%s", p.Skill, p.Target)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Receipt{}, fmt.Errorf("unsafe receipt source for %s/%s", p.Skill, p.Target)
		}
		key := pairKey(p.Skill, p.Target)
		if seen[key] {
			return Receipt{}, fmt.Errorf("duplicate receipt projection")
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for _, managed := range receipt.Managed {
		repository, known := catalogRepository(managed.Catalog)
		if (managed.Kind != "instruction" && managed.Kind != "config" && managed.Kind != "file") || !known || (managed.Kind == "file") == (managed.Name == "") {
			return Receipt{}, fmt.Errorf("invalid receipt managed entry")
		}
		destination, err := managedFileDestination(paths, managed.Kind, managed.Target, managed.Name)
		if err != nil || managed.Strategy != "copy" || !validHash(managed.SourceHash) || !validHash(managed.AppliedHash) {
			return Receipt{}, fmt.Errorf("invalid receipt %s", managed.Kind)
		}
		key := ItemID(managed.Kind, managed.Target, managed.Name)
		if seen[key] {
			return Receipt{}, fmt.Errorf("duplicate receipt %s", managed.Kind)
		}
		seen[key] = true
		// codex-global follows CODEX_HOME, which may have changed since apply;
		// the plan then blocks only that item as drift.
		codexMoved := key == "instruction/codex-global" && filepath.IsAbs(managed.Destination) && filepath.Clean(managed.Destination) == managed.Destination && filepath.Base(managed.Destination) == "AGENTS.md"
		if (managed.Destination != destination && !codexMoved) || !filepath.IsAbs(managed.Source) || filepath.Clean(managed.Source) != managed.Source || !contained(repository, managed.Source) {
			return Receipt{}, fmt.Errorf("unsafe receipt %s paths for %s", managed.Kind, managed.Target)
		}
		switch managed.Origin {
		case "created":
			if managed.OriginalHash != "" || managed.OriginalMode != 0 || managed.Backup != "" {
				return Receipt{}, fmt.Errorf("invalid created %s receipt for %s", managed.Kind, managed.Target)
			}
		case "adopted":
			if !validHash(managed.OriginalHash) || managed.OriginalMode&0o022 != 0 || managed.OriginalMode&^0o777 != 0 || managed.Backup != managedBackup(paths, managed.Kind, managed.Target, managed.Name) {
				return Receipt{}, fmt.Errorf("invalid adopted %s receipt for %s", managed.Kind, managed.Target)
			}
		default:
			return Receipt{}, fmt.Errorf("invalid %s origin for %s", managed.Kind, managed.Target)
		}
	}
	seen = map[string]bool{}
	for i, entry := range receipt.JSONKeys {
		_, known := catalogRepository(entry.Catalog)
		_, supported := lookupTarget("json-keys", entry.Target)
		id := ItemID("json-keys", entry.Target, entry.Key)
		_, _, name, idErr := ParseItemID(id)
		if !known || !supported || entry.Key == "" || idErr != nil || name != entry.Key || !validHash(entry.AppliedHash) {
			return Receipt{}, fmt.Errorf("invalid receipt json key")
		}
		switch entry.Origin {
		case "created":
			if len(entry.OriginalValue) != 0 {
				return Receipt{}, fmt.Errorf("invalid created json key receipt for %s", id)
			}
		case "adopted":
			canonical, err := canonicalRaw(entry.OriginalValue)
			if len(entry.OriginalValue) == 0 || err != nil {
				return Receipt{}, fmt.Errorf("invalid adopted json key receipt for %s", id)
			}
			receipt.JSONKeys[i].OriginalValue = canonical
		default:
			return Receipt{}, fmt.Errorf("invalid json key origin for %s", id)
		}
		if seen[id] {
			return Receipt{}, fmt.Errorf("duplicate receipt json key")
		}
		seen[id] = true
	}
	return receipt, nil
}

func validHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func Plan(target string) (PlanResult, error) {
	if err := validateTarget(target); err != nil {
		return PlanResult{}, err
	}
	paths, err := ResolvePaths()
	if err != nil {
		return PlanResult{}, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return PlanResult{}, fmt.Errorf("load enrollment: %w", err)
	}
	return planEnrolled(paths, enrollment, target)
}

func planEnrolled(paths Paths, enrollment Enrollment, target string) (PlanResult, error) {
	catalogs, receipt, err := loadPlanInputs(paths, enrollment)
	if err != nil {
		return PlanResult{}, err
	}
	return makePlan(paths, catalogs, receipt, enrollment.Holds, target)
}

// loadPlanInputs loads every enrolled catalog before the receipt so an
// unavailable overlay fails closed instead of its owned items being planned
// for removal.
func loadPlanInputs(paths Paths, enrollment Enrollment) (Catalogs, Receipt, error) {
	catalogs, err := LoadCatalogs(enrollment)
	if err != nil {
		return Catalogs{}, Receipt{}, err
	}
	receipt, err := LoadReceipt(paths, enrollment)
	if errors.Is(err, os.ErrNotExist) {
		receipt = Receipt{}
	} else if err != nil {
		return Catalogs{}, Receipt{}, fmt.Errorf("load receipt: %w", err)
	} else if receipt.RepositoryID != enrollment.RepositoryID || receipt.RepositoryPath != catalogs.Primary.Repository {
		return Catalogs{}, Receipt{}, Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("receipt repository differs from enrollment"))
	}
	return catalogs, receipt, nil
}

// currentPlatform is a variable so tests can simulate another platform.
var currentPlatform = runtime.GOOS

// platformIncluded reports whether an item applies on this platform; no
// platforms means every platform.
func platformIncluded(platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, platform := range platforms {
		if platform == currentPlatform {
			return true
		}
	}
	return false
}

// inert actions never touch the machine: the item is held or not for this platform.
func inert(action Action) bool { return action.Action == "held" || action.Action == "excluded" }

func makePlan(paths Paths, catalogs Catalogs, receipt Receipt, holds []string, filter string) (PlanResult, error) {
	held := make(map[string]bool, len(holds))
	for _, id := range holds {
		held[id] = true
	}
	var actions []Action
	// add classifies an item unless the machine holds it; held items are never inspected.
	add := func(action Action, classify func() (string, string)) {
		if held[action.ID] {
			action.Action, action.Reason = "held", "held on this machine"
		} else {
			action.Action, action.Reason = classify()
		}
		actions = append(actions, action)
	}
	excluded := func(platforms []string) func() (string, string) {
		return func() (string, string) { return "excluded", strings.Join(platforms, ",") + "-only" }
	}
	ownedSkills := map[string]ReceiptProjection{}
	for _, projection := range receipt.Projections {
		ownedSkills[pairKey(projection.Skill, projection.Target)] = projection
	}
	ownedManaged := map[string]ReceiptManaged{}
	for _, managed := range receipt.Managed {
		ownedManaged[ItemID(managed.Kind, managed.Target, managed.Name)] = managed
	}
	desiredSkills := map[string]bool{}
	for _, loaded := range catalogs.list() {
		for _, projection := range loaded.Manifest.Projections {
			for _, target := range projection.Targets {
				if !selectedSkill(filter, target) {
					continue
				}
				destination, _ := skillDestination(paths, target, projection.Skill)
				action := Action{ID: ItemID("skill", target, projection.Skill), Kind: "skill", Catalog: loaded.Manifest.ID, Skill: projection.Skill, Target: target, Source: loaded.Sources[projection.Skill], Destination: destination}
				key := pairKey(projection.Skill, target)
				prior, owned := ownedSkills[key]
				if !platformIncluded(projection.Platforms) {
					// An owned projection falls through to the removal loop below.
					if !owned {
						add(action, excluded(projection.Platforms))
					}
					continue
				}
				desiredSkills[key] = true
				add(action, func() (string, string) {
					return classifySkill(paths, action, loaded.SkillHashes[projection.Skill], prior, owned)
				})
			}
		}
	}
	for key, prior := range ownedSkills {
		if !selectedSkill(filter, prior.Target) || desiredSkills[key] {
			continue
		}
		destination, _ := skillDestination(paths, prior.Target, prior.Skill)
		action := Action{ID: ItemID("skill", prior.Target, prior.Skill), Kind: "skill", Catalog: prior.Catalog, Skill: prior.Skill, Target: prior.Target, Source: prior.Source, Destination: destination}
		add(action, func() (string, string) { return classifySkillRemoval(paths, prior, action.Destination) })
	}
	// Two desired items must never write one path, e.g. when CODEX_HOME points
	// at another harness's directory.
	claimed := map[string]string{}
	claim := func(destination, owner string) error {
		if prior, ok := claimed[destination]; ok && prior != owner {
			return Coded(CodeManifestInvalid, "set CODEX_HOME and XDG_CONFIG_HOME so each item has its own destination", fmt.Errorf("%s and %s both resolve to %s", prior, owner, destination))
		}
		claimed[destination] = owner
		return nil
	}
	// insideCatalog checks the lexical destination and, when it resolves, the
	// real path behind a symlinked HOME or XDG root.
	insideCatalog := func(destination string) bool {
		resolved, err := resolveDestination(paths, destination)
		return catalogs.insideAny(destination) || (err == nil && catalogs.insideAny(resolved))
	}
	desiredManaged := map[string]bool{}
	for _, loaded := range catalogs.list() {
		for _, item := range loaded.managedItems() {
			if !selectedManaged(filter, item.Kind, item.Target) {
				continue
			}
			destination, _ := managedFileDestination(paths, item.Kind, item.Target, item.Name)
			if insideCatalog(destination) {
				return PlanResult{}, fmt.Errorf("%s destination for %s must not be inside a catalog repository", item.Kind, managedLabel(item.Target, item.Name))
			}
			action := Action{ID: item.id(), Kind: item.Kind, Catalog: loaded.Manifest.ID, Target: item.Target, Name: item.Name, Source: item.Source, Destination: destination}
			prior, owned := ownedManaged[action.ID]
			if !platformIncluded(item.Platforms) {
				if !owned {
					add(action, excluded(item.Platforms))
				}
				continue
			}
			desiredManaged[action.ID] = true
			if err := claim(destination, action.ID); err != nil {
				return PlanResult{}, err
			}
			add(action, func() (string, string) {
				return classifyInstruction(paths, action, item.Hash, prior, owned)
			})
		}
	}
	jsonClaimed := jsonKeysDestinations(paths, catalogs)
	for id, prior := range ownedManaged {
		if !selectedManaged(filter, prior.Kind, prior.Target) || desiredManaged[id] {
			continue
		}
		destination, _ := managedFileDestination(paths, prior.Kind, prior.Target, prior.Name)
		action := Action{ID: id, Kind: prior.Kind, Catalog: prior.Catalog, Target: prior.Target, Name: prior.Name, Source: prior.Source, Destination: destination}
		add(action, func() (string, string) {
			return classifyInstructionRemoval(paths, prior, destination, jsonClaimed[destination])
		})
	}
	ownedJSONKeys := map[string]ReceiptJSONKey{}
	for _, entry := range receipt.JSONKeys {
		ownedJSONKeys[ItemID("json-keys", entry.Target, entry.Key)] = entry
	}
	// Each settings file is read at most once, and only when a key needs it.
	jsonFiles := map[string]jsonSettings{}
	inspect := func(target, destination string) jsonSettings {
		file, ok := jsonFiles[target]
		if !ok {
			file = inspectJSONSettings(paths, destination)
			jsonFiles[target] = file
		}
		return file
	}
	desiredJSONKeys := map[string]bool{}
	for _, loaded := range catalogs.list() {
		for _, item := range loaded.Manifest.JSONKeys {
			if !selectedManaged(filter, "json-keys", item.Target) {
				continue
			}
			destination, _ := jsonKeysDestination(paths, item.Target)
			if insideCatalog(destination) {
				return PlanResult{}, fmt.Errorf("json-keys destination for %s must not be inside a catalog repository", item.Target)
			}
			if platformIncluded(item.Platforms) {
				if err := claim(destination, ItemID("json-keys", item.Target, "")); err != nil {
					return PlanResult{}, err
				}
			}
			for key, value := range loaded.JSONKeyValues[item.Target] {
				action := Action{ID: ItemID("json-keys", item.Target, key), Kind: "json-keys", Catalog: loaded.Manifest.ID, Target: item.Target, Name: key, Source: loaded.JSONKeySources[item.Target], Destination: destination}
				prior, owned := ownedJSONKeys[action.ID]
				if !platformIncluded(item.Platforms) {
					if !owned {
						add(action, excluded(item.Platforms))
					}
					continue
				}
				desiredJSONKeys[action.ID] = true
				add(action, func() (string, string) {
					return classifyJSONKey(inspect(item.Target, destination), key, value, prior, owned)
				})
			}
		}
	}
	for id, prior := range ownedJSONKeys {
		if !selectedManaged(filter, "json-keys", prior.Target) || desiredJSONKeys[id] {
			continue
		}
		destination, _ := jsonKeysDestination(paths, prior.Target)
		action := Action{ID: id, Kind: "json-keys", Catalog: prior.Catalog, Target: prior.Target, Name: prior.Key, Destination: destination}
		if loaded, err := catalogs.catalog(prior.Catalog); err == nil {
			action.Source = loaded.JSONKeySources[prior.Target]
		}
		add(action, func() (string, string) {
			return classifyJSONKey(inspect(prior.Target, destination), prior.Key, nil, prior, true)
		})
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].Kind != actions[j].Kind {
			return actions[i].Kind < actions[j].Kind
		}
		if actions[i].Target != actions[j].Target {
			return actions[i].Target < actions[j].Target
		}
		if actions[i].Skill != actions[j].Skill {
			return actions[i].Skill < actions[j].Skill
		}
		return actions[i].Name < actions[j].Name
	})
	clean := true
	for _, action := range actions {
		if action.Action != "noop" && !inert(action) {
			clean = false
		}
	}
	plan := PlanResult{SchemaVersion: SchemaVersion, Clean: clean, Actions: actions, jsonFiles: map[string]string{}, collisions: map[string]string{}, sources: map[string]string{}}
	for _, action := range actions {
		if hash, ok := actionSourceHash(catalogs, action); ok {
			plan.sources[action.ID] = hash
		}
	}
	for target, file := range jsonFiles {
		plan.jsonFiles[target] = file.hash
	}
	for _, action := range actions {
		if action.Action != "blocked_collision" {
			continue
		}
		if value, ok := jsonFiles[action.Target].values[action.Name]; action.Kind == "json-keys" && ok {
			plan.collisions[action.ID] = hashBytes(value)
		} else {
			plan.collisions[action.ID] = contentHash(action.Destination)
		}
	}
	plan.Digest = PlanDigest(plan, catalogs.Fingerprint)
	return plan, nil
}

// PlanDigest identifies a plan and the catalogs it was computed from: sha256
// over the canonical JSON of the actions sorted by item id, a newline, the
// catalogs fingerprint, and one "\n<id> <hash>" line per blocked collision
// (sorted by id) binding the colliding content a decision was reviewed against,
// then one "\nsource <id> <hash>" line per desired item binding its catalog content.
func PlanDigest(plan PlanResult, catalogsFingerprint string) string {
	actions := append([]Action{}, plan.Actions...)
	sort.Slice(actions, func(i, j int) bool { return actions[i].ID < actions[j].ID })
	data, _ := json.Marshal(actions) // Action holds only strings; Marshal cannot fail
	data = append(append(data, '\n'), catalogsFingerprint...)
	for _, action := range actions {
		if hash, ok := plan.collisions[action.ID]; ok {
			data = append(data, "\n"+action.ID+" "+hash...)
		}
	}
	for _, action := range actions {
		if hash, ok := plan.sources[action.ID]; ok {
			data = append(data, "\nsource "+action.ID+" "+hash...)
		}
	}
	return hashBytes(data)
}

// actionSourceHash returns the hash of the catalog content an action would
// install, or false for items no catalog declares (removals).
func actionSourceHash(catalogs Catalogs, action Action) (string, bool) {
	loaded, err := catalogs.catalog(action.Catalog)
	if err != nil {
		return "", false
	}
	switch action.Kind {
	case "skill":
		hash, ok := loaded.SkillHashes[action.Skill]
		return hash, ok
	case "json-keys":
		value, ok := loaded.JSONKeyValues[action.Target][action.Name]
		if !ok {
			return "", false
		}
		return hashBytes(value), true
	default:
		return managedSourceHash(loaded, action.Kind, action.Target, action.Name)
	}
}

// contentHash identifies what currently occupies a destination without
// following symlinks: a file's bytes, a link's target, or a directory's sorted
// relative paths, modes, and link targets.
func contentHash(path string) string {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return hashBytes([]byte("error: " + err.Error()))
	case info.Mode()&os.ModeSymlink != 0:
		link, _ := os.Readlink(path)
		return hashBytes([]byte("link " + link))
	case info.Mode().IsRegular():
		hash, err := fileHash(path)
		if err != nil {
			return hashBytes([]byte("error: " + err.Error()))
		}
		return hash
	case !info.IsDir():
		return hashBytes([]byte("mode " + info.Mode().String()))
	}
	var listing strings.Builder
	_ = filepath.WalkDir(path, func(current string, entry os.DirEntry, err error) error {
		relative, _ := filepath.Rel(path, current)
		if err != nil {
			fmt.Fprintf(&listing, "%s error\n", relative)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(&listing, "%s error\n", relative)
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(current)
		}
		fmt.Fprintf(&listing, "%q %s %q\n", relative, info.Mode(), link)
		return nil
	})
	return hashBytes([]byte(listing.String()))
}

// leftoverPrefixes name the siblings an apply creates beside a destination and
// removes before it returns; one that remains means an apply was interrupted.
var leftoverPrefixes = []string{".terran-tmp-", ".terran-old-", ".terran-quarantine-"}

// leftovers lists the entries of dir whose names start with any prefix.
func leftovers(dir string, prefixes ...string) []string {
	entries, _ := os.ReadDir(dir)
	var found []string
	for _, entry := range entries {
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry.Name(), prefix) {
				found = append(found, filepath.Join(dir, entry.Name()))
				break
			}
		}
	}
	return found
}

// leftoverQuarantine blocks a destination whose directory still holds a
// quarantined file from an interrupted replacement.
func leftoverQuarantine(destination string) error {
	if found := leftovers(filepath.Dir(destination), ".terran-quarantine-"); len(found) > 0 {
		return fmt.Errorf("leftover Terran quarantine found at %s; an earlier apply was interrupted — inspect it before continuing", found[0])
	}
	return nil
}

func classifyInstruction(paths Paths, action Action, sourceHash string, prior ReceiptManaged, owned bool) (string, string) {
	if owned && prior.Destination != action.Destination {
		return "blocked_drift", "CODEX_HOME changed"
	}
	if _, err := resolveDestination(paths, action.Destination); err != nil {
		if owned {
			return "blocked_drift", err.Error()
		}
		return "blocked_collision", err.Error()
	}
	if err := leftoverQuarantine(action.Destination); err != nil {
		return "blocked_collision", err.Error()
	}
	parent := filepath.Dir(action.Destination)
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		if owned {
			return "blocked_drift", "receipt-owned instruction parent is missing"
		}
		if err := validateProspectiveInstructionParent(parent); err != nil {
			return "blocked_collision", err.Error()
		}
		return "create", "instruction target parent will be created"
	} else if err != nil {
		return "blocked_collision", err.Error()
	} else if err := validateInstructionParent(action.Destination); err != nil {
		if owned {
			return "blocked_drift", err.Error()
		}
		return "blocked_collision", err.Error()
	}
	if owned {
		if err := validateTrustedFile(action.Destination, "managed instruction"); err != nil {
			return "blocked_drift", "receipt-owned instruction is missing or unsafe: " + err.Error()
		}
		hash, err := fileHash(action.Destination)
		if err == nil && hash != prior.AppliedHash && hash == sourceHash {
			// Edited on this machine and then synced into the catalog, or an
			// apply interrupted before its receipt write: only the receipt moves.
			if err := validateExecutableMode(action.Kind, action.Target, action.Destination); err != nil {
				return "blocked_drift", err.Error()
			}
			return "update", matchesCatalogReason
		}
		if err != nil || hash != prior.AppliedHash {
			return "blocked_drift", "receipt-owned instruction is missing or changed"
		}
		if err := validateExecutableMode(action.Kind, action.Target, action.Destination); err != nil {
			return "blocked_drift", err.Error()
		}
		if sourceHash == prior.SourceHash {
			return "noop", ""
		}
		return "update", "instruction source changed"
	}
	if _, err := os.Lstat(action.Destination); errors.Is(err, os.ErrNotExist) {
		return "create", ""
	} else if err != nil {
		return "blocked_collision", err.Error()
	}
	if err := validateTrustedFile(action.Destination, "instruction destination"); err != nil {
		return "blocked_collision", err.Error()
	}
	hash, err := fileHash(action.Destination)
	if err != nil {
		return "blocked_collision", err.Error()
	}
	if hash != sourceHash {
		return "blocked_collision", "instruction destination differs from source"
	}
	if err := validateExecutableMode(action.Kind, action.Target, action.Destination); err != nil {
		return "blocked_collision", err.Error()
	}
	backup := managedBackup(paths, action.Kind, action.Target, action.Name)
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return "blocked_collision", err.Error()
		}
		if err := validateUnreferencedBackup(backup); err != nil {
			return "blocked_collision", "instruction adoption backup already exists and is unsafe: " + err.Error()
		}
		backupData, _, err := readSafeFile(backup, "unreferenced instruction backup")
		if err != nil {
			return "blocked_collision", "cannot validate unreferenced instruction backup: " + err.Error()
		}
		if hashBytes(backupData) != sourceHash {
			return "blocked_collision", "unreferenced backup differs from the exact active file; possible interrupted replacement requires manual recovery"
		}
		return "adopt", "existing exact regular file; reuse exact unreferenced backup"
	}
	return "adopt", "existing exact regular file"
}

func validateProspectiveInstructionParent(parent string) error {
	current := parent
	for {
		_, err := os.Lstat(current)
		if err == nil {
			return validateTrustedDirectory(current, "instruction target ancestor")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		next := filepath.Dir(current)
		if next == current {
			return fmt.Errorf("instruction target has no existing trusted ancestor")
		}
		current = next
	}
}

// jsonKeysDestinations lists the settings files desired json-keys items write.
func jsonKeysDestinations(paths Paths, catalogs Catalogs) map[string]bool {
	claimed := map[string]bool{}
	for _, loaded := range catalogs.list() {
		for _, item := range loaded.Manifest.JSONKeys {
			if destination, err := jsonKeysDestination(paths, item.Target); err == nil && platformIncluded(item.Platforms) {
				claimed[destination] = true
			}
		}
	}
	return claimed
}

// classifyInstructionRemoval plans an owned file that left the catalog. When
// json-keys items now write the same file (opencode-config replaced by
// opencode-settings), whole-file ownership is released and the file is kept.
func classifyInstructionRemoval(paths Paths, prior ReceiptManaged, destination string, sharedWithJSONKeys bool) (string, string) {
	if sharedWithJSONKeys {
		return "release", "settings keys now own this file; keep it and drop whole-file ownership"
	}
	if prior.Destination != destination {
		return "blocked_drift", "CODEX_HOME changed"
	}
	if _, err := resolveDestination(paths, destination); err != nil {
		return "blocked_drift", err.Error()
	}
	if err := leftoverQuarantine(destination); err != nil {
		return "blocked_collision", err.Error()
	}
	if err := validateInstructionParent(destination); err != nil {
		return "blocked_drift", err.Error()
	}
	if err := validateTrustedFile(destination, "managed instruction"); err != nil {
		return "blocked_drift", "receipt-owned instruction is missing or unsafe: " + err.Error()
	}
	hash, err := fileHash(destination)
	if err != nil || hash != prior.AppliedHash {
		return "blocked_drift", "receipt-owned instruction is missing or changed"
	}
	if err := validateExecutableMode(prior.Kind, prior.Target, destination); err != nil {
		return "blocked_drift", err.Error()
	}
	if prior.Origin == "created" {
		return "remove", "created instruction is no longer in manifest"
	}
	if err := validateBackup(paths, prior); err != nil {
		return "blocked_drift", err.Error()
	}
	return "restore", "adopted instruction is no longer in manifest"
}

// validateExecutableMode requires targets with an executable mode, such as
// hooks, to keep exactly that mode; other managed files ignore mode.
func validateExecutableMode(kind, target, path string) error {
	spec, ok := lookupTarget(kind, target)
	if !ok || spec.Mode&0o111 == 0 {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != spec.Mode {
		return fmt.Errorf("%s file %s must be mode %04o", target, path, spec.Mode)
	}
	return nil
}

func validateBackup(paths Paths, prior ReceiptManaged) error {
	backup := managedBackup(paths, prior.Kind, prior.Target, prior.Name)
	if prior.Backup != backup {
		return fmt.Errorf("instruction backup path does not match fixed target")
	}
	if err := validateTrustedFile(backup, "instruction backup"); err != nil {
		return fmt.Errorf("instruction backup is missing or unsafe: %w", err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("instruction backup must be mode 0600")
	}
	hash, err := fileHash(backup)
	if err != nil || hash != prior.OriginalHash {
		return fmt.Errorf("instruction backup hash mismatch")
	}
	return nil
}

// classifySkill plans a desired skill as a managed directory copy of a source
// whose tree hash is sourceHash.
func classifySkill(paths Paths, action Action, sourceHash string, prior ReceiptProjection, owned bool) (string, string) {
	if _, err := resolveDestination(paths, action.Destination); err != nil {
		if owned {
			return "blocked_drift", err.Error()
		}
		return "blocked_collision", err.Error()
	}
	root := filepath.Dir(action.Destination)
	rootInfo, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if owned {
			return "blocked_drift", "receipt-owned target root is missing"
		}
		return "create", "target root will be created"
	}
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "blocked_collision", "target root is not a real directory"
	}
	if err := validateTargetRoot(root); err != nil {
		return "blocked_collision", err.Error()
	}
	if owned {
		if !ownedSkillIntact(action.Destination, prior) {
			// An apply interrupted after installing the copy but before the
			// receipt write leaves exactly the catalog content in place.
			if hash, err := skillTreeHash(action.Destination); err == nil && hash == sourceHash {
				return "update", recoverSkillReason
			}
			return "blocked_drift", "receipt-owned skill is missing or changed"
		}
		if prior.Strategy == "symlink" {
			return "update", "convert live symlink to managed copy"
		}
		if prior.AppliedHash == sourceHash {
			return "noop", ""
		}
		return "update", "skill source changed"
	}
	info, err := os.Lstat(action.Destination)
	if errors.Is(err, os.ErrNotExist) {
		return "create", ""
	}
	if err != nil {
		return "blocked_collision", err.Error()
	}
	if info.IsDir() {
		if hash, err := skillTreeHash(action.Destination); err == nil && hash == sourceHash {
			return "adopt", "existing identical directory"
		}
	}
	return "blocked_collision", "destination exists and is not safely owned"
}

// recoverSkillReason marks an update that only records an already-installed
// copy in the receipt.
const recoverSkillReason = "recover interrupted apply (content already matches catalog)"

// matchesCatalogReason marks an update of an owned file or settings key whose
// destination already holds the catalog content; apply writes only the receipt.
const matchesCatalogReason = "destination already matches catalog; record it"

// classifySkillRemoval plans an owned skill that is no longer desired: a
// created copy or legacy link is removed, an adopted copy is released.
func classifySkillRemoval(paths Paths, prior ReceiptProjection, destination string) (string, string) {
	if _, err := resolveDestination(paths, destination); err != nil {
		return "blocked_drift", err.Error()
	}
	if err := validateTargetRoot(filepath.Dir(destination)); err != nil {
		return "blocked_drift", err.Error()
	}
	if !ownedSkillIntact(destination, prior) {
		return "blocked_drift", "receipt-owned skill is missing or changed"
	}
	if prior.Origin == "adopted" {
		return "release", "adopted skill is no longer in manifest; Terran stops managing it and keeps the directory"
	}
	return "remove", "skill is no longer in manifest"
}

// ownedSkillIntact reports whether a destination is exactly what the receipt
// recorded: the legacy link, or a copy with the applied tree hash.
func ownedSkillIntact(destination string, prior ReceiptProjection) bool {
	if prior.Strategy == "symlink" {
		return exactSymlink(destination, prior.Source)
	}
	hash, err := skillTreeHash(destination)
	return err == nil && hash == prior.AppliedHash
}

func exactSymlink(path, expected string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	link, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}
	link = filepath.Clean(link)
	resolved, err := filepath.EvalSymlinks(link)
	if err == nil {
		return resolved == expected
	}
	return errors.Is(err, os.ErrNotExist) && link == expected
}

func pairKey(skill, target string) string { return skill + "\x00" + target }

func blocked(plan PlanResult) bool {
	for _, action := range plan.Actions {
		if action.Action == "blocked_collision" || action.Action == "blocked_drift" {
			return true
		}
	}
	return false
}

var (
	beforeInstructionMutation func(Action) error
	beforeReceiptWrite        func() error
	afterInstructionRename    func(string) error
	afterInstructionHash      func(string) error
	afterReceiptRename        func() error
	afterReplacementBackup    func(Action) error
	beforeReplacementInstall  func(Action) error
	afterReplacementInstall   func(Action) error
	beforeReplacementCleanup  func(string) error
	beforeBackupPublish       func(string) error
	afterBackupPublication    func(string) error
	afterSkillInstall         func(Action) error
	restoreReceiptFile        = restoreReceiptSnapshot
	removeInstructionBackup   = safelyRemoveInstructionBackup
)

var ErrApplyAborted = errors.New("apply aborted by user")

// holdInterrupts captures and drops SIGINT, SIGTERM, and SIGHUP so rollback
// or commit always completes; the returned release restores default handling.
// signal.Ignore is not used because signal.Reset does not undo it.
func holdInterrupts() (release func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return func() { signal.Stop(signals) }
}

type resolvedCollision struct {
	destinationInfo os.FileInfo
	originalHash    string
	originalMode    os.FileMode
	backup          string
	backupInfo      os.FileInfo
	backupHash      string
	originalValue   json.RawMessage // json-keys: the replaced unowned value
	fileHash        string          // json-keys: settings file the value was read from
}

func Apply(target, buildVersion string) (PlanResult, error) {
	return ApplyWithOptions(target, buildVersion, ApplyOptions{})
}

func ApplyWithOptions(target, buildVersion string, options ApplyOptions) (PlanResult, error) {
	if err := validateTarget(target); err != nil {
		return PlanResult{}, err
	}
	paths, err := ResolvePaths()
	if err != nil {
		return PlanResult{}, err
	}
	var result PlanResult
	err = withLock(paths.Lock, func() error {
		enrollment, err := LoadEnrollment(paths)
		if err != nil {
			return err
		}
		catalogs, receipt, err := loadPlanInputs(paths, enrollment)
		if err != nil {
			return err
		}
		result, err = makePlan(paths, catalogs, receipt, enrollment.Holds, target)
		if err != nil {
			return err
		}
		if options.ExpectDigest != "" && options.ExpectDigest != result.Digest {
			return Coded(CodePlanChanged, "run terran plan --json again and review", fmt.Errorf("plan digest %s does not match the expected digest", result.Digest))
		}
		decided := make([]string, 0, len(options.Decisions))
		for id, decision := range options.Decisions {
			if decision != CollisionReplace && decision != CollisionKeep {
				return Coded(CodeUsage, "", fmt.Errorf("invalid decision %q for %s", decision, id))
			}
			decided = append(decided, id)
		}
		sort.Strings(decided)
		for _, id := range decided {
			if actionByID(result, id).Action != "blocked_collision" {
				return Coded(CodeUsage, "run terran plan --json to list blocked_collision items", fmt.Errorf("%s is not a blocked_collision in the current plan", id))
			}
		}
		resolved := map[string]resolvedCollision{}
		var kept []string
		for i := range result.Actions {
			action := result.Actions[i]
			decision, hasDecision := options.Decisions[action.ID]
			if action.Action != "blocked_collision" || (!hasDecision && options.ResolveCollision == nil) {
				continue
			}
			var state resolvedCollision
			if decision != CollisionKeep {
				loaded, err := catalogs.catalog(action.Catalog)
				if err != nil {
					return err
				}
				state, err = resolvableCollision(paths, loaded, action)
				if err != nil {
					if hasDecision {
						result.Actions[i].Reason = "replace not possible: " + err.Error()
					}
					continue
				}
				if action.Kind == "json-keys" && state.fileHash != result.jsonFiles[action.Target] {
					return planChanged(action.Destination)
				}
				if !hasDecision {
					decision, err = options.ResolveCollision(action)
					if err != nil {
						return fmt.Errorf("resolve collision for %s: %w", action.Target, err)
					}
				}
			}
			switch decision {
			case CollisionReplace:
				result.Actions[i].Action = "replace"
				switch action.Kind {
				case "skill":
					result.Actions[i].Reason = "replace existing skill; move it to a private backup (not restored on removal)"
				case "json-keys":
					result.Actions[i].Reason = "replace differing existing key; original value is restored on removal"
				default:
					result.Actions[i].Reason = "replace differing existing file; preserve original in private backup"
				}
			case CollisionSkip:
				result.Actions[i].Action = "skip"
				result.Actions[i].Reason = "keep differing existing file"
			case CollisionKeep:
				result.Actions[i].Action = "held"
				result.Actions[i].Reason = "kept existing destination; held on this machine"
				kept = append(kept, action.ID)
			case CollisionAbort:
				return ErrApplyAborted
			default:
				return fmt.Errorf("resolver returned invalid decision %q for %s", decision, action.Target)
			}
			resolved[managedActionKey(action)] = state
		}
		if blocked(result) {
			return nil
		}
		if options.ConfirmPlan != nil {
			if err := options.ConfirmPlan(result); err != nil {
				return fmt.Errorf("confirm plan: %w", err)
			}
		}
		if err := revalidateCatalogs(catalogs); err != nil {
			return fmt.Errorf("revalidate catalog: %w", err)
		}
		ownedSkills := map[string]ReceiptProjection{}
		for _, projection := range receipt.Projections {
			ownedSkills[pairKey(projection.Skill, projection.Target)] = projection
		}
		ownedInstructions := map[string]ReceiptManaged{}
		for _, managed := range receipt.Managed {
			ownedInstructions[ItemID(managed.Kind, managed.Target, managed.Name)] = managed
		}
		ownedJSONKeys := map[string]ReceiptJSONKey{}
		for _, entry := range receipt.JSONKeys {
			ownedJSONKeys[ItemID("json-keys", entry.Target, entry.Key)] = entry
		}
		for _, action := range result.Actions {
			if err := preflightAction(paths, catalogs, action, ownedSkills, ownedInstructions, resolved); err != nil {
				return err
			}
		}
		jsonTargets := jsonTargetsToVerify(result)
		for _, jsonTarget := range jsonTargets {
			if _, _, _, err := verifyJSONSettings(paths, catalogs, result, jsonTarget); err != nil {
				return err
			}
		}
		// From the first mutation until the receipt commits or rolls back, an
		// interrupt or SSH disconnect must not kill the process.
		defer holdInterrupts()()
		var skillRollbacks []skillRollback
		for _, action := range result.Actions {
			if action.Kind != "skill" || inert(action) {
				continue
			}
			if err := preflightAction(paths, catalogs, action, ownedSkills, ownedInstructions, resolved); err != nil {
				return errors.Join(err, rollbackSkills(skillRollbacks))
			}
			skillRollbacks = append(skillRollbacks, prepareSkillRollback(action, resolved))
			if err := mutateSkill(catalogs, action, ownedSkills, resolved, &skillRollbacks[len(skillRollbacks)-1]); err != nil {
				return errors.Join(err, rollbackSkills(skillRollbacks))
			}
		}
		var rollbacks []instructionRollback
		verifiedInstructionHashes := map[string]string{}
		for _, action := range result.Actions {
			if action.Kind == "skill" || action.Kind == "json-keys" || action.Action == "noop" || action.Action == "skip" || inert(action) {
				continue
			}
			if err := preflightAction(paths, catalogs, action, ownedSkills, ownedInstructions, resolved); err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			if beforeInstructionMutation != nil {
				if err := beforeInstructionMutation(action); err != nil {
					return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
				}
			}
			loaded, err := catalogs.catalog(action.Catalog)
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
			var sourceBytes []byte
			if expectedHash, desired := managedSourceHash(loaded, action.Kind, action.Target, action.Name); desired {
				sourceBytes, err = readVerifiedManagedSource(loaded.Repository, action.Kind, action.Target, action.Source, expectedHash)
				if err != nil {
					rollbackErr := rollbackAll(rollbacks, skillRollbacks)
					return errors.Join(err, rollbackErr)
				}
				verifiedInstructionHashes[action.ID] = hashBytes(sourceBytes)
			}
			state, hasResolvedState := resolved[managedActionKey(action)]
			rollback, err := mutateInstruction(paths, loaded, action, ownedInstructions[action.ID], sourceBytes, state, hasResolvedState)
			rollbacks = append(rollbacks, rollback)
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		for _, jsonTarget := range jsonTargets {
			rollback, err := mutateJSONKeys(paths, catalogs, result, jsonTarget, ownedJSONKeys)
			rollbacks = append(rollbacks, rollback)
			if err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		if err := revalidateCatalogs(catalogs); err != nil {
			return errors.Join(fmt.Errorf("revalidate catalog before receipt: %w", err), rollbackAll(rollbacks, skillRollbacks))
		}
		// Held items keep their receipt entries untouched; platform-excluded
		// items get none (an owned one was just removed).
		heldIDs := map[string]bool{}
		for _, action := range result.Actions {
			if action.Action == "held" {
				heldIDs[action.ID] = true
			}
		}
		untouched := func(kind, target, name string, platforms []string) bool {
			return !platformIncluded(platforms) || heldIDs[ItemID(kind, target, name)]
		}
		for _, loaded := range catalogs.list() {
			for _, item := range loaded.managedItems() {
				if !selectedManaged(target, item.Kind, item.Target) || untouched(item.Kind, item.Target, item.Name, item.Platforms) || actionByID(result, item.id()).Action == "skip" {
					continue
				}
				data, err := readVerifiedManagedSource(loaded.Repository, item.Kind, item.Target, item.Source, item.Hash)
				if err != nil {
					return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
				}
				verifiedInstructionHashes[item.id()] = hashBytes(data)
			}
			for _, item := range loaded.Manifest.JSONKeys {
				if !selectedManaged(target, "json-keys", item.Target) || !platformIncluded(item.Platforms) {
					continue
				}
				if _, err := readVerifiedManagedSource(loaded.Repository, "json-keys", item.Target, loaded.JSONKeySources[item.Target], loaded.JSONKeyHashes[item.Target]); err != nil {
					return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
				}
			}
		}
		now := time.Now().UTC()
		newReceipt := Receipt{SchemaVersion: SchemaVersion, RepositoryID: enrollment.RepositoryID, RepositoryPath: enrollment.RepositoryPath, RepositoryVersion: catalogs.Primary.Manifest.Version, ManifestFingerprint: catalogs.Fingerprint}
		for _, old := range receipt.Projections {
			if !selectedSkill(target, old.Target) || heldIDs[ItemID("skill", old.Target, old.Skill)] {
				newReceipt.Projections = append(newReceipt.Projections, old)
			}
		}
		for _, old := range receipt.Managed {
			if !selectedManaged(target, old.Kind, old.Target) || heldIDs[ItemID(old.Kind, old.Target, old.Name)] {
				newReceipt.Managed = append(newReceipt.Managed, old)
			}
		}
		for _, old := range receipt.JSONKeys {
			id := ItemID("json-keys", old.Target, old.Key)
			if !selectedManaged(target, "json-keys", old.Target) || heldIDs[id] {
				newReceipt.JSONKeys = append(newReceipt.JSONKeys, old)
			}
		}
		for _, loaded := range catalogs.list() {
			for _, projection := range loaded.Manifest.Projections {
				for _, destinationTarget := range projection.Targets {
					if !selectedSkill(target, destinationTarget) || untouched("skill", destinationTarget, projection.Skill, projection.Platforms) || actionByID(result, ItemID("skill", destinationTarget, projection.Skill)).Action == "skip" {
						continue
					}
					destination, _ := skillDestination(paths, destinationTarget, projection.Skill)
					// Created copies, collision replacements, and converted
					// legacy links are removed with the item; adopted copies are released.
					origin := "created"
					if prior, owned := ownedSkills[pairKey(projection.Skill, destinationTarget)]; owned && prior.Strategy == "copy" {
						origin = prior.Origin
					} else if !owned && actionByID(result, ItemID("skill", destinationTarget, projection.Skill)).Action == "adopt" {
						origin = "adopted"
					}
					newReceipt.Projections = append(newReceipt.Projections, ReceiptProjection{Catalog: loaded.Manifest.ID, Skill: projection.Skill, Target: destinationTarget, Source: loaded.Sources[projection.Skill], Destination: destination, Strategy: "copy", AppliedHash: loaded.SkillHashes[projection.Skill], Origin: origin, AppliedAt: now, TerranBuildVersion: buildVersion})
				}
			}
			for _, item := range loaded.managedItems() {
				if !selectedManaged(target, item.Kind, item.Target) || untouched(item.Kind, item.Target, item.Name, item.Platforms) {
					continue
				}
				action := actionByID(result, item.id())
				if action.Action == "skip" {
					continue
				}
				destination, _ := managedFileDestination(paths, item.Kind, item.Target, item.Name)
				prior, owned := ownedInstructions[item.id()]
				origin := "created"
				var originalHash, backup string
				var originalMode uint32
				if owned {
					origin, originalHash, originalMode, backup = prior.Origin, prior.OriginalHash, prior.OriginalMode, prior.Backup
				} else if action.Action == "adopt" {
					origin = "adopted"
					info, _ := os.Stat(destination)
					originalHash = item.Hash
					originalMode = uint32(info.Mode().Perm())
					backup = managedBackup(paths, item.Kind, item.Target, item.Name)
				} else if action.Action == "replace" {
					state := resolved[managedActionKey(action)]
					origin = "adopted"
					originalHash = state.originalHash
					originalMode = uint32(state.originalMode.Perm())
					backup = state.backup
				}
				sourceHash := verifiedInstructionHashes[item.id()]
				if sourceHash == "" {
					sourceHash = item.Hash
				}
				newReceipt.Managed = append(newReceipt.Managed, ReceiptManaged{Kind: item.Kind, Catalog: loaded.Manifest.ID, Target: item.Target, Name: item.Name, Source: item.Source, Destination: destination, Strategy: "copy", SourceHash: sourceHash, AppliedHash: sourceHash, Origin: origin, OriginalHash: originalHash, OriginalMode: originalMode, Backup: backup, AppliedAt: now, TerranBuildVersion: buildVersion})
			}
			for _, item := range loaded.Manifest.JSONKeys {
				if !selectedManaged(target, "json-keys", item.Target) {
					continue
				}
				for key, value := range loaded.JSONKeyValues[item.Target] {
					id := ItemID("json-keys", item.Target, key)
					if untouched("json-keys", item.Target, key, item.Platforms) || actionByID(result, id).Action == "skip" {
						continue
					}
					entry := ReceiptJSONKey{Catalog: loaded.Manifest.ID, Target: item.Target, Key: key, AppliedHash: hashBytes(value), Origin: "created", AppliedAt: now, TerranBuildVersion: buildVersion}
					if prior, owned := ownedJSONKeys[id]; owned {
						entry.Origin, entry.OriginalValue = prior.Origin, prior.OriginalValue
					} else if action := actionByID(result, id); action.Action == "adopt" {
						entry.Origin, entry.OriginalValue = "adopted", value
					} else if action.Action == "replace" {
						entry.Origin, entry.OriginalValue = "adopted", resolved[managedActionKey(action)].originalValue
					}
					newReceipt.JSONKeys = append(newReceipt.JSONKeys, entry)
				}
			}
		}
		sort.Slice(newReceipt.Projections, func(i, j int) bool {
			return pairKey(newReceipt.Projections[i].Skill, newReceipt.Projections[i].Target) < pairKey(newReceipt.Projections[j].Skill, newReceipt.Projections[j].Target)
		})
		sort.Slice(newReceipt.Managed, func(i, j int) bool {
			a, b := newReceipt.Managed[i], newReceipt.Managed[j]
			if pairKey(a.Kind, a.Target) != pairKey(b.Kind, b.Target) {
				return pairKey(a.Kind, a.Target) < pairKey(b.Kind, b.Target)
			}
			return a.Name < b.Name
		})
		sort.Slice(newReceipt.JSONKeys, func(i, j int) bool {
			a, b := newReceipt.JSONKeys[i], newReceipt.JSONKeys[j]
			return pairKey(a.Target, a.Key) < pairKey(b.Target, b.Key)
		})
		priorReceipt, _, priorReceiptErr := readTrustedFile(paths.Receipt, "receipt", 4<<20, 0o600)
		priorReceiptExisted := priorReceiptErr == nil
		if priorReceiptErr != nil && !errors.Is(priorReceiptErr, os.ErrNotExist) {
			return errors.Join(priorReceiptErr, rollbackAll(rollbacks, skillRollbacks))
		}
		if beforeReceiptWrite != nil {
			if err := beforeReceiptWrite(); err != nil {
				return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		intendedReceipt, err := marshalJSON(newReceipt)
		if err != nil {
			return errors.Join(err, rollbackAll(rollbacks, skillRollbacks))
		}
		writeResult, writeErr := atomicPrivateJSONBytes(paths.Receipt, intendedReceipt, afterReceiptRename)
		if writeErr != nil {
			if writeResult.renamed {
				installed, _, verifyErr := readTrustedFile(paths.Receipt, "installed receipt", 4<<20, 0o600)
				if verifyErr == nil && bytes.Equal(installed, intendedReceipt) {
					appendReceiptWarning(&result, "receipt committed but directory durability sync failed: "+writeErr.Error())
				} else {
					if verifyErr == nil {
						verifyErr = fmt.Errorf("installed receipt bytes differ from intended receipt")
					}
					restoreErr := restoreReceiptFile(paths.Receipt, priorReceipt, priorReceiptExisted)
					if restoreErr != nil {
						return errors.Join(writeErr, fmt.Errorf("verify installed receipt: %w", verifyErr), fmt.Errorf("restore prior receipt: %w", restoreErr))
					}
					return errors.Join(writeErr, fmt.Errorf("verify installed receipt: %w", verifyErr), rollbackAll(rollbacks, skillRollbacks))
				}
			} else {
				return errors.Join(writeErr, rollbackAll(rollbacks, skillRollbacks))
			}
		}
		removeSetAsideSkills(&result, skillRollbacks)
		for _, action := range result.Actions {
			if action.Kind == "skill" || action.Kind == "json-keys" || action.Action != "restore" {
				continue
			}
			backup := managedBackup(paths, action.Kind, action.Target, action.Name)
			if receiptReferencesBackup(newReceipt, backup) {
				continue
			}
			if err := validateUnreferencedBackup(backup); err != nil {
				appendActionWarning(&result, action, "backup cleanup warning: "+err.Error())
				continue
			}
			if err := removeInstructionBackup(backup); err != nil {
				appendActionWarning(&result, action, "backup cleanup warning: "+err.Error())
			}
		}
		// Kept collisions were never touched, so their holds are written last.
		// Their ids come from this plan and none is already held.
		if len(kept) > 0 {
			updated := enrollment
			updated.Holds = append(append([]string(nil), enrollment.Holds...), kept...)
			sort.Strings(updated.Holds)
			if _, err := writeEnrollment(paths, updated); err != nil {
				return Coded(CodePartialApply, "run terran hold ITEM_ID for each kept item, then terran plan", fmt.Errorf("apply was committed but holds for %s were not saved: %w", strings.Join(kept, ", "), err))
			}
		}
		return nil
	})
	return result, err
}

// CanResolveCollision reports whether an action from Plan is currently eligible
// for ApplyWithOptions' narrowly scoped managed-file collision prompt.
func CanResolveCollision(action Action) (bool, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return false, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return false, err
	}
	catalogs, err := LoadCatalogs(enrollment)
	if err != nil {
		return false, err
	}
	loaded, err := catalogs.catalog(action.Catalog)
	if err != nil {
		return false, err
	}
	_, ineligible := resolvableCollision(paths, loaded, action)
	return ineligible == nil, nil
}

func appendReceiptWarning(plan *PlanResult, warning string) {
	if len(plan.Actions) == 0 {
		return
	}
	if plan.Actions[0].Reason != "" {
		plan.Actions[0].Reason += "; "
	}
	plan.Actions[0].Reason += warning
}

func actionByID(plan PlanResult, id string) Action {
	for _, action := range plan.Actions {
		if action.ID == id {
			return action
		}
	}
	return Action{}
}

func actionFor(plan PlanResult, kind, target string) Action {
	for _, action := range plan.Actions {
		if action.Kind == kind && action.Target == target {
			return action
		}
	}
	return Action{}
}

func receiptReferencesBackup(receipt Receipt, backup string) bool {
	for _, managed := range receipt.Managed {
		if managed.Backup == backup {
			return true
		}
	}
	return false
}

func appendActionWarning(plan *PlanResult, action Action, warning string) {
	for i := range plan.Actions {
		if plan.Actions[i].ID == action.ID {
			if plan.Actions[i].Reason != "" {
				plan.Actions[i].Reason += "; "
			}
			plan.Actions[i].Reason += warning
			return
		}
	}
}

func restoreReceiptSnapshot(path string, data []byte, existed bool) error {
	if existed {
		result, err := atomicPrivateJSONBytes(path, data, nil)
		if err == nil {
			return nil
		}
		if result.renamed {
			installed, _, verifyErr := readTrustedFile(path, "restored receipt", 4<<20, 0o600)
			if verifyErr == nil && bytes.Equal(installed, data) {
				return nil
			}
			if verifyErr == nil {
				verifyErr = fmt.Errorf("restored receipt bytes differ from prior receipt")
			}
			return errors.Join(err, verifyErr)
		}
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		if _, lstatErr := os.Lstat(path); errors.Is(lstatErr, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func validateUnreferencedBackup(path string) error {
	if err := validateTrustedFile(path, "unreferenced instruction backup"); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("instruction backup must be mode 0600")
	}
	return nil
}

func safelyRemoveInstructionBackup(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func managedActionKey(action Action) string { return action.ID }

// resolvableCollision reports whether a blocked collision can be replaced; the
// error says why not.
func resolvableCollision(paths Paths, loaded LoadedManifest, action Action) (resolvedCollision, error) {
	switch action.Kind {
	case "skill":
		return resolvableSkillCollision(paths, loaded, action)
	case "json-keys":
		return resolvableJSONKeyCollision(paths, loaded, action)
	}
	if (action.Kind != "instruction" && action.Kind != "config" && action.Kind != "file") || action.Action != "blocked_collision" {
		return resolvedCollision{}, fmt.Errorf("not a managed-file collision")
	}
	if _, err := resolveDestination(paths, action.Destination); err != nil {
		return resolvedCollision{}, err
	}
	if err := leftoverQuarantine(action.Destination); err != nil {
		return resolvedCollision{}, err
	}
	if err := validateInstructionParent(action.Destination); err != nil {
		return resolvedCollision{}, err
	}
	source, desired := managedItemSource(loaded, action.Kind, action.Target, action.Name)
	if !desired || source != action.Source {
		return resolvedCollision{}, fmt.Errorf("catalog source changed")
	}
	sourceBytes, err := readVerifiedManagedSource(loaded.Repository, action.Kind, action.Target, source, managedHash(loaded, action.Kind, action.Target, action.Name))
	if err != nil {
		return resolvedCollision{}, err
	}
	original, mode, err := readSafeFile(action.Destination, "managed-file collision")
	if err != nil {
		return resolvedCollision{}, err
	}
	if bytes.Equal(original, sourceBytes) {
		return resolvedCollision{}, fmt.Errorf("destination already matches the catalog")
	}
	info, err := os.Lstat(action.Destination)
	if err != nil {
		return resolvedCollision{}, err
	}
	state := resolvedCollision{destinationInfo: info, originalHash: hashBytes(original), originalMode: mode, backup: managedBackup(paths, action.Kind, action.Target, action.Name)}
	if !safeBackupParent(paths, filepath.Dir(state.backup)) {
		return resolvedCollision{}, errUnsafeBackupParent
	}
	if info, err := os.Lstat(state.backup); err == nil {
		if err := validateUnreferencedBackup(state.backup); err != nil {
			return resolvedCollision{}, fmt.Errorf("existing unreferenced backup is unsafe: %w", err)
		}
		backupData, _, err := readSafeFile(state.backup, "unreferenced instruction backup")
		if err != nil {
			return resolvedCollision{}, err
		}
		hash := hashBytes(backupData)
		if hash != state.originalHash {
			return resolvedCollision{}, fmt.Errorf("an unreferenced backup with different content exists at %s", state.backup)
		}
		state.backupInfo, state.backupHash = info, hash
	} else if !errors.Is(err, os.ErrNotExist) {
		return resolvedCollision{}, err
	}
	return state, nil
}

var errUnsafeBackupParent = errors.New("backup directory is unsafe")

// resolvableSkillCollision accepts an existing unowned skill directory or link
// that can be renamed into a private backup on the same filesystem.
func resolvableSkillCollision(paths Paths, loaded LoadedManifest, action Action) (resolvedCollision, error) {
	if action.Action != "blocked_collision" {
		return resolvedCollision{}, fmt.Errorf("not a skill collision")
	}
	if source, desired := loaded.Sources[action.Skill]; !desired || source != action.Source {
		return resolvedCollision{}, fmt.Errorf("catalog source changed")
	} else if err := validateTrustedSource(loaded.Repository, source); err != nil {
		return resolvedCollision{}, err
	}
	if _, err := resolveDestination(paths, action.Destination); err != nil {
		return resolvedCollision{}, err
	}
	if err := validateTargetRoot(filepath.Dir(action.Destination)); err != nil {
		return resolvedCollision{}, err
	}
	info, err := os.Lstat(action.Destination)
	if err != nil {
		return resolvedCollision{}, err
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return resolvedCollision{}, fmt.Errorf("destination is not a directory or symlink")
	}
	backup := filepath.Join(paths.BackupDir, "skill", action.Target, action.Skill, "original")
	if !safeBackupParent(paths, filepath.Dir(backup)) {
		return resolvedCollision{}, errUnsafeBackupParent
	}
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return resolvedCollision{}, fmt.Errorf("a skill backup already exists at %s", backup)
	}
	ancestor := filepath.Dir(backup)
	for {
		ancestorInfo, err := os.Lstat(ancestor)
		if err == nil {
			if !sameDevice(info, ancestorInfo) {
				return resolvedCollision{}, fmt.Errorf("the backup directory is on another filesystem")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return resolvedCollision{}, err
		}
		ancestor = filepath.Dir(ancestor)
	}
	return resolvedCollision{destinationInfo: info, backup: backup}, nil
}

// resolvableJSONKeyCollision accepts a key holding a different, valid value in
// a readable settings file and remembers that value for restoration.
func resolvableJSONKeyCollision(paths Paths, loaded LoadedManifest, action Action) (resolvedCollision, error) {
	value, desired := loaded.JSONKeyValues[action.Target][action.Name]
	if action.Action != "blocked_collision" || !desired || loaded.JSONKeySources[action.Target] != action.Source {
		return resolvedCollision{}, fmt.Errorf("catalog value changed")
	}
	file := inspectJSONSettings(paths, action.Destination)
	current, present := file.values[action.Name]
	switch {
	case file.blocked != "":
		return resolvedCollision{}, errors.New(file.blocked)
	case !present:
		return resolvedCollision{}, fmt.Errorf("key is not present")
	case bytes.Equal(current, value):
		return resolvedCollision{}, fmt.Errorf("key already has the catalog value")
	}
	return resolvedCollision{originalValue: current, fileHash: file.hash}, nil
}

func safeBackupParent(paths Paths, parent string) bool {
	if !contained(paths.StateDir, parent) {
		return false
	}
	for current := parent; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			if err := validateTrustedDirectory(current, "backup directory"); err != nil {
				return false
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		if current == paths.StateDir {
			return true
		}
	}
}

func preflightResolvedCollision(paths Paths, loaded LoadedManifest, action Action, expected resolvedCollision) error {
	fresh, ineligible := resolvableCollision(paths, loaded, Action{ID: action.ID, Kind: action.Kind, Action: "blocked_collision", Skill: action.Skill, Target: action.Target, Name: action.Name, Source: action.Source, Destination: action.Destination})
	eligible := ineligible == nil
	if action.Kind == "skill" {
		if !eligible || !os.SameFile(fresh.destinationInfo, expected.destinationInfo) {
			return fmt.Errorf("skill collision changed during apply")
		}
		return nil
	}
	if !eligible || fresh.originalHash != expected.originalHash || fresh.originalMode != expected.originalMode || !os.SameFile(fresh.destinationInfo, expected.destinationInfo) {
		return fmt.Errorf("managed-file collision changed during apply")
	}
	if (fresh.backupInfo == nil) != (expected.backupInfo == nil) || fresh.backupHash != expected.backupHash {
		return fmt.Errorf("managed-file collision backup changed during apply")
	}
	if fresh.backupInfo != nil && !os.SameFile(fresh.backupInfo, expected.backupInfo) {
		return fmt.Errorf("managed-file collision backup changed during apply")
	}
	return nil
}

func preflightAction(paths Paths, catalogs Catalogs, action Action, ownedSkills map[string]ReceiptProjection, ownedInstructions map[string]ReceiptManaged, resolved map[string]resolvedCollision) error {
	if inert(action) {
		return nil
	}
	if err := revalidateCatalogs(catalogs); err != nil {
		return fmt.Errorf("revalidate catalog before %s: %w", action.Target, err)
	}
	// Sources are verified against the repository of the item's own catalog.
	loaded, err := catalogs.catalog(action.Catalog)
	if err != nil {
		return err
	}
	if action.Kind == "json-keys" {
		// Verified per settings file by verifyJSONSettings.
		return nil
	}
	if state, ok := resolved[managedActionKey(action)]; ok && action.Kind == "skill" {
		return preflightResolvedCollision(paths, loaded, action, state)
	}
	if action.Kind == "skill" {
		if source, desired := loaded.Sources[action.Skill]; desired && source == action.Source {
			if err := validateTrustedSource(loaded.Repository, action.Source); err != nil {
				return err
			}
		}
		prior, owned := ownedSkills[pairKey(action.Skill, action.Target)]
		var fresh string
		if action.Action == "remove" || action.Action == "release" {
			fresh, _ = classifySkillRemoval(paths, prior, action.Destination)
		} else {
			fresh, _ = classifySkill(paths, action, loaded.SkillHashes[action.Skill], prior, owned)
		}
		if fresh != action.Action && !(action.Action == "record" && fresh == "noop") {
			return fmt.Errorf("projection changed during apply")
		}
		return nil
	}
	if state, ok := resolved[managedActionKey(action)]; ok {
		return preflightResolvedCollision(paths, loaded, action, state)
	}
	source, desired := managedItemSource(loaded, action.Kind, action.Target, action.Name)
	if desired {
		if err := validateTrustedInstructionSource(loaded.Repository, source); err != nil {
			return err
		}
		data, err := readVerifiedManagedSource(loaded.Repository, action.Kind, action.Target, source, managedHash(loaded, action.Kind, action.Target, action.Name))
		if err != nil {
			return fmt.Errorf("managed source changed during apply: %w", err)
		}
		hash := hashBytes(data)
		prior, owned := ownedInstructions[action.ID]
		fresh, _ := classifyInstruction(paths, action, hash, prior, owned)
		if fresh != action.Action {
			return fmt.Errorf("instruction changed during apply")
		}
	} else {
		fresh, _ := classifyInstructionRemoval(paths, ownedInstructions[action.ID], action.Destination, jsonKeysDestinations(paths, catalogs)[action.Destination])
		if fresh != action.Action {
			return fmt.Errorf("instruction changed during apply")
		}
	}
	return nil
}

// skillRollback records how one skill mutation changed its destination.
type skillRollback struct {
	id          string
	destination string
	installed   os.FileInfo // the copy Terran renamed into place, if any
	old         string      // update or removal: where the prior destination was set aside
	backup      string      // collision replacement: where the original was moved
	createdDirs []string
}

func prepareSkillRollback(action Action, resolved map[string]resolvedCollision) skillRollback {
	rollback := skillRollback{id: action.ID, destination: action.Destination}
	if state, ok := resolved[managedActionKey(action)]; ok && action.Action == "replace" {
		rollback.backup = state.backup
	}
	if action.Action == "create" {
		for current := filepath.Dir(action.Destination); ; current = filepath.Dir(current) {
			if _, err := os.Lstat(current); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				break
			}
			rollback.createdDirs = append(rollback.createdDirs, current)
		}
	}
	return rollback
}

func rollbackSkills(rollbacks []skillRollback) error {
	var rollbackErrs []error
	for i := len(rollbacks) - 1; i >= 0; i-- {
		rollback := rollbacks[i]
		if rollback.installed != nil {
			if info, err := os.Lstat(rollback.destination); err == nil && os.SameFile(info, rollback.installed) {
				if err := os.RemoveAll(rollback.destination); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback installed skill %s: %w", rollback.destination, err))
				}
			}
		}
		for _, displaced := range []string{rollback.old, rollback.backup} {
			if displaced == "" {
				continue
			}
			_, destinationErr := os.Lstat(rollback.destination)
			if _, err := os.Lstat(displaced); err == nil && errors.Is(destinationErr, os.ErrNotExist) {
				if err := os.Rename(displaced, rollback.destination); err != nil {
					rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback displaced skill %s: %w", rollback.destination, err))
				}
			}
		}
		for _, dir := range rollback.createdDirs {
			if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback projection directory %s: %w", dir, err))
			}
		}
	}
	return errors.Join(rollbackErrs...)
}

// removeSetAsideSkills deletes the destinations that updates and removals set
// aside, once the receipt no longer references them.
func removeSetAsideSkills(result *PlanResult, rollbacks []skillRollback) {
	for _, rollback := range rollbacks {
		if rollback.old == "" {
			continue
		}
		if err := os.RemoveAll(rollback.old); err != nil {
			appendActionWarning(result, Action{ID: rollback.id}, "cleanup warning: "+err.Error())
		}
	}
}

// mutateSkill creates, updates, replaces, or removes one skill copy. A new
// copy is built and verified beside the destination before anything is
// displaced, and displaced destinations are set aside until the receipt
// commits. rollback records each step as it happens.
func mutateSkill(catalogs Catalogs, action Action, owned map[string]ReceiptProjection, resolved map[string]resolvedCollision, rollback *skillRollback) error {
	if action.Action != "create" && action.Action != "update" && action.Action != "replace" && action.Action != "remove" {
		return nil
	}
	if err := revalidateCatalogs(catalogs); err != nil {
		return err
	}
	root := filepath.Dir(action.Destination)
	prior := owned[pairKey(action.Skill, action.Target)]
	if action.Reason == recoverSkillReason {
		// Nothing is written; the receipt records the copy already in place.
		loaded, err := catalogs.catalog(action.Catalog)
		if err != nil {
			return err
		}
		if hash, err := skillTreeHash(action.Destination); err != nil || hash != loaded.SkillHashes[action.Skill] {
			return fmt.Errorf("projection changed during apply")
		}
		return nil
	}
	if action.Action == "remove" {
		old, err := setAsideSkill(action, prior)
		rollback.old = old
		if err != nil {
			return err
		}
		return syncDirectory(root)
	}
	if err := ensureTargetRoot(root); err != nil {
		return err
	}
	loaded, err := catalogs.catalog(action.Catalog)
	if err != nil {
		return err
	}
	temp, err := buildSkillCopy(loaded, action)
	if err != nil {
		return err
	}
	defer func() {
		if temp != "" {
			_ = os.RemoveAll(temp)
		}
	}()
	switch action.Action {
	case "create":
		if _, err := os.Lstat(action.Destination); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("projection changed during apply")
		}
	case "update":
		old, err := setAsideSkill(action, prior)
		rollback.old = old
		if err != nil {
			return err
		}
	case "replace":
		// Collision replacement: move the unowned skill into a private backup.
		state, collision := resolved[managedActionKey(action)]
		if !collision {
			return fmt.Errorf("projection changed during apply")
		}
		if info, err := os.Lstat(action.Destination); err != nil || !os.SameFile(info, state.destinationInfo) {
			return fmt.Errorf("projection changed during apply")
		}
		if err := ensurePrivateDir(filepath.Dir(state.backup)); err != nil {
			return err
		}
		if err := os.Rename(action.Destination, state.backup); err != nil {
			return fmt.Errorf("backup not possible: %w", err)
		}
	}
	if err := os.Rename(temp, action.Destination); err != nil {
		return err
	}
	temp = ""
	info, err := os.Lstat(action.Destination)
	if err != nil {
		return err
	}
	rollback.installed = info
	if afterSkillInstall != nil {
		if err := afterSkillInstall(action); err != nil {
			return err
		}
	}
	return syncDirectory(root)
}

// setAsideSkill renames an owned destination that is exactly what the
// receipt recorded to a hidden sibling, and checks it again there.
func setAsideSkill(action Action, prior ReceiptProjection) (string, error) {
	if !ownedSkillIntact(action.Destination, prior) {
		return "", fmt.Errorf("projection changed during apply")
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	old := filepath.Join(filepath.Dir(action.Destination), ".terran-old-"+action.Skill+"-"+strings.TrimPrefix(id, "cc-"))
	if err := os.Rename(action.Destination, old); err != nil {
		return "", err
	}
	if !ownedSkillIntact(old, prior) {
		return old, fmt.Errorf("projection changed during apply")
	}
	return old, nil
}

// buildSkillCopy re-reads a skill source through the trusted readers,
// requires the tree hash the plan saw, and writes a copy with normalized modes
// into a hidden temporary directory beside the destination. Every file and
// directory is synced and the copy's tree hash is verified.
func buildSkillCopy(loaded LoadedManifest, action Action) (string, error) {
	if err := validateTrustedSource(loaded.Repository, action.Source); err != nil {
		return "", err
	}
	entries, hash, err := readSkillTree(action.Source, true)
	if err == nil && hash != loaded.SkillHashes[action.Skill] {
		err = fmt.Errorf("tree hash differs")
	}
	if err != nil {
		return "", Coded(CodePlanChanged, "run terran plan again", fmt.Errorf("%s changed since it was planned: %w", action.Source, err))
	}
	if err := validateTrustedSource(loaded.Repository, action.Source); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(filepath.Dir(action.Destination), ".terran-tmp-"+action.Skill+"-*")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(temp)
		}
	}()
	// Entries are sorted, so every directory precedes its contents.
	for _, entry := range entries {
		path := filepath.Join(temp, filepath.FromSlash(entry.path))
		if entry.dir {
			if err := os.Mkdir(path, 0o700); err != nil {
				return "", err
			}
			continue
		}
		if err := writeSkillFile(path, entry.data, entry.mode); err != nil {
			return "", err
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].dir {
			path := filepath.Join(temp, filepath.FromSlash(entries[i].path))
			if err := os.Chmod(path, 0o755); err != nil {
				return "", err
			}
			if err := syncDirectory(path); err != nil {
				return "", err
			}
		}
	}
	if err := os.Chmod(temp, 0o755); err != nil {
		return "", err
	}
	if err := syncDirectory(temp); err != nil {
		return "", err
	}
	if written, err := skillTreeHash(temp); err != nil || written != hash {
		return "", fmt.Errorf("skill copy verification failed for %s", action.Destination)
	}
	ok = true
	return temp, nil
}

func writeSkillFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

type instructionRollback struct {
	destination     string
	before          []byte
	mode            os.FileMode
	existed         bool
	afterHash       string
	backup          string
	backupMade      bool
	backupExisted   bool
	backupAfterInfo os.FileInfo
	backupAfterHash string
	afterInfo       os.FileInfo
	recovery        string
}

func mutateInstruction(paths Paths, loaded LoadedManifest, action Action, prior ReceiptManaged, verifiedSource []byte, expected resolvedCollision, hasExpected bool) (instructionRollback, error) {
	rollback := instructionRollback{destination: action.Destination}
	if action.Action == "release" {
		return rollback, nil
	}
	if action.Reason == matchesCatalogReason {
		// Only the receipt moves, so the destination must still hold exactly
		// the verified catalog bytes.
		data, _, err := readSafeFile(action.Destination, "managed instruction")
		if err != nil || !bytes.Equal(data, verifiedSource) {
			return rollback, fmt.Errorf("instruction changed during apply")
		}
		return rollback, nil
	}
	if action.Action == "adopt" || action.Action == "replace" {
		data, mode, err := readSafeFile(action.Destination, "instruction destination")
		if err != nil {
			return rollback, err
		}
		if hasExpected {
			info, err := os.Lstat(action.Destination)
			if err != nil || hashBytes(data) != expected.originalHash || mode != expected.originalMode || !os.SameFile(info, expected.destinationInfo) {
				return rollback, fmt.Errorf("managed-file collision changed during apply")
			}
		}
		backup := managedBackup(paths, action.Kind, action.Target, action.Name)
		if _, err := os.Lstat(backup); err == nil {
			existing, _, err := readSafeFile(backup, "unreferenced instruction backup")
			if err != nil {
				return rollback, err
			}
			info, err := os.Lstat(backup)
			if hasExpected && (err != nil || expected.backupInfo == nil || hashBytes(existing) != expected.backupHash || !os.SameFile(info, expected.backupInfo)) {
				return rollback, fmt.Errorf("managed-file collision backup changed during apply")
			}
			if !bytes.Equal(existing, data) {
				return rollback, fmt.Errorf("unreferenced backup differs from active file; possible interrupted replacement requires manual recovery")
			}
			rollback.backupExisted = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback, err
		} else if hasExpected && expected.backupInfo != nil {
			return rollback, fmt.Errorf("managed-file collision backup changed during apply")
		}
		rollback.backup = backup
		if !rollback.backupExisted {
			info, created, err := publishBackup(backup, data)
			rollback.backupMade = created
			rollback.backupAfterInfo = info
			rollback.backupAfterHash = hashBytes(data)
			if err != nil {
				return rollback, err
			}
		}
		if afterBackupPublication != nil {
			if err := afterBackupPublication(backup); err != nil {
				return rollback, err
			}
		}
		if rollback.backupMade {
			info, err := os.Lstat(backup)
			if err != nil || rollback.backupAfterInfo == nil || !os.SameFile(info, rollback.backupAfterInfo) {
				return rollback, fmt.Errorf("instruction backup changed during apply")
			}
		}
		if err := validatePublishedBackup(backup, data); err != nil {
			return rollback, err
		}
		if action.Action == "adopt" {
			return rollback, nil
		}
		rollback.before, rollback.mode, rollback.existed = data, mode, true
		if afterReplacementBackup != nil {
			if err := afterReplacementBackup(action); err != nil {
				return rollback, err
			}
		}
	}
	if action.Action != "create" && action.Action != "replace" {
		data, mode, err := readSafeFile(action.Destination, "managed instruction")
		if err != nil {
			return rollback, err
		}
		rollback.before, rollback.mode, rollback.existed = data, mode, true
	}
	switch action.Action {
	case "create", "update":
		spec, _ := lookupTarget(action.Kind, action.Target)
		mode := spec.Mode
		if action.Kind == "instruction" && rollback.existed {
			mode = rollback.mode
		}
		mutation, err := atomicInstructionFile(action.Destination, verifiedSource, mode, action.Action == "create")
		if mutation.mutated {
			rollback.afterHash = managedHash(loaded, action.Kind, action.Target, action.Name)
			rollback.afterInfo = mutation.info
		}
		if err != nil {
			return rollback, err
		}
	case "replace":
		spec, _ := lookupTarget(action.Kind, action.Target)
		mode := spec.Mode
		if action.Kind == "instruction" {
			mode = rollback.mode
		}
		mutation, err := conditionalInstructionReplace(action, verifiedSource, mode, expected.destinationInfo, expected.originalHash, expected.originalMode, true)
		if mutation.mutated {
			rollback.afterHash = managedHash(loaded, action.Kind, action.Target, action.Name)
			rollback.afterInfo = mutation.info
		}
		rollback.recovery = mutation.recovery
		if err != nil {
			return rollback, err
		}
	case "remove":
		if err := os.Remove(action.Destination); err != nil {
			return rollback, err
		}
	case "restore":
		if err := validateBackup(paths, prior); err != nil {
			return rollback, err
		}
		data, _, err := readSafeFile(managedBackup(paths, action.Kind, action.Target, action.Name), "instruction backup")
		if err != nil {
			return rollback, err
		}
		mutation, err := atomicInstructionFile(action.Destination, data, os.FileMode(prior.OriginalMode), false)
		if mutation.mutated {
			rollback.afterHash = prior.OriginalHash
			rollback.afterInfo = mutation.info
		}
		if err != nil {
			return rollback, err
		}
	}
	return rollback, nil
}

func readVerifiedManagedSource(repository, kind, target, source, expectedHash string) ([]byte, error) {
	if err := validateTrustedInstructionSource(repository, source); err != nil {
		return nil, err
	}
	data, _, err := readTrustedFile(source, "instruction source", instructionLimit, 0)
	if err != nil {
		return nil, err
	}
	if err := validateTrustedInstructionSource(repository, source); err != nil {
		return nil, err
	}
	if hashBytes(data) != expectedHash {
		return nil, fmt.Errorf("managed source changed during apply")
	}
	spec, ok := lookupTarget(kind, target)
	if !ok {
		return nil, fmt.Errorf("unsupported %s target %q", kind, target)
	}
	if spec.Validate != nil {
		if err := spec.Validate(data); err != nil {
			return nil, fmt.Errorf("config source is unsafe: %w", err)
		}
	}
	return data, nil
}

// managedSource looks up an unnamed instruction or config source.
func managedSource(loaded LoadedManifest, kind, target string) (string, bool) {
	return managedItemSource(loaded, kind, target, "")
}

func managedItemSource(loaded LoadedManifest, kind, target, name string) (string, bool) {
	var source string
	var ok bool
	switch kind {
	case "config":
		source, ok = loaded.ConfigSources[target]
	case "file":
		source, ok = loaded.FileSources[ItemID(kind, target, name)]
	default:
		source, ok = loaded.InstructionSources[target]
	}
	return source, ok
}

func managedHash(loaded LoadedManifest, kind, target, name string) string {
	switch kind {
	case "config":
		return loaded.ConfigHashes[target]
	case "file":
		return loaded.FileHashes[ItemID(kind, target, name)]
	}
	return loaded.InstructionHashes[target]
}

func managedSourceHash(loaded LoadedManifest, kind, target, name string) (string, bool) {
	_, ok := managedItemSource(loaded, kind, target, name)
	return managedHash(loaded, kind, target, name), ok
}

func rollbackInstructions(rollbacks []instructionRollback) error {
	var rollbackErrs []error
	for i := len(rollbacks) - 1; i >= 0; i-- {
		rollback := rollbacks[i]
		activeRestored := true
		if !rollback.existed {
			if rollback.afterHash != "" {
				if current, err := os.Stat(rollback.destination); err == nil && rollback.afterInfo != nil && os.SameFile(current, rollback.afterInfo) {
					if err := os.Remove(rollback.destination); err != nil {
						rollbackErrs = append(rollbackErrs, fmt.Errorf("remove created instruction %s: %w", rollback.destination, err))
					}
				}
			}
		} else {
			if rollback.afterHash == "" {
				if _, err := os.Lstat(rollback.destination); !errors.Is(err, os.ErrNotExist) {
					activeRestored = false
				}
			} else {
				current, err := os.Stat(rollback.destination)
				if err != nil || rollback.afterInfo == nil || !os.SameFile(current, rollback.afterInfo) {
					activeRestored = false
				}
			}
			if activeRestored {
				var err error
				if rollback.afterHash != "" {
					_, err = conditionalInstructionReplace(Action{Destination: rollback.destination}, rollback.before, rollback.mode, rollback.afterInfo, rollback.afterHash, rollback.afterInfo.Mode().Perm(), false)
				} else {
					_, err = atomicInstructionFile(rollback.destination, rollback.before, rollback.mode, false)
				}
				if err != nil {
					activeRestored = false
					rollbackErrs = append(rollbackErrs, fmt.Errorf("restore instruction %s: %w", rollback.destination, err))
				}
			}
		}
		if rollback.recovery != "" {
			recovery, mode, err := readSafeFile(rollback.recovery, "replacement recovery file")
			switch {
			case errors.Is(err, os.ErrNotExist):
			case err != nil:
				activeRestored = false
				rollbackErrs = append(rollbackErrs, fmt.Errorf("inspect replacement recovery %s: %w", rollback.recovery, err))
			case hashBytes(recovery) != hashBytes(rollback.before) || mode != rollback.mode:
				activeRestored = false
				rollbackErrs = append(rollbackErrs, fmt.Errorf("newer displaced bytes preserved at %s", rollback.recovery))
			case !activeRestored:
				rollbackErrs = append(rollbackErrs, fmt.Errorf("displaced original preserved at %s", rollback.recovery))
			default:
				if err := removeRecoveryFile(rollback.recovery); err != nil {
					activeRestored = false
					rollbackErrs = append(rollbackErrs, fmt.Errorf("remove replacement recovery %s: %w", rollback.recovery, err))
				}
			}
		}
		if rollback.backupMade && activeRestored {
			if err := removeBackupConditional(rollback.backup, rollback.backupAfterInfo, rollback.backupAfterHash); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
		}
	}
	return errors.Join(rollbackErrs...)
}

func rollbackAll(instructions []instructionRollback, skills []skillRollback) error {
	return errors.Join(rollbackInstructions(instructions), rollbackSkills(skills))
}

func ensureInstructionParent(destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	return validateInstructionParent(destination)
}

type instructionFileMutation struct {
	mutated  bool
	info     os.FileInfo
	recovery string
}

func atomicInstructionFile(destination string, data []byte, mode os.FileMode, requireMissing bool) (instructionFileMutation, error) {
	var mutation instructionFileMutation
	if err := ensureInstructionParent(destination); err != nil {
		return mutation, err
	}
	if requireMissing {
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return mutation, fmt.Errorf("instruction changed during apply")
		}
	}
	dir := filepath.Dir(destination)
	f, err := os.CreateTemp(dir, ".terran-instruction-*")
	if err != nil {
		return mutation, err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode.Perm()); err != nil {
		return mutation, err
	}
	if _, err := f.Write(data); err != nil {
		return mutation, err
	}
	if err := f.Sync(); err != nil {
		return mutation, err
	}
	if err := f.Close(); err != nil {
		return mutation, err
	}
	if requireMissing {
		if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
			return mutation, fmt.Errorf("instruction changed during apply")
		}
	}
	if err := os.Rename(tmp, destination); err != nil {
		return mutation, err
	}
	mutation.mutated = true
	mutation.info, _ = os.Stat(destination)
	if afterInstructionRename != nil {
		if err := afterInstructionRename(destination); err != nil {
			return mutation, err
		}
	}
	if err := syncDirectory(dir); err != nil {
		return mutation, err
	}
	written, err := fileHash(destination)
	if err != nil || written != hashBytes(data) {
		return mutation, fmt.Errorf("instruction hash verification failed")
	}
	if afterInstructionHash != nil {
		if err := afterInstructionHash(destination); err != nil {
			return mutation, err
		}
	}
	ok = true
	return mutation, nil
}

// errReplacementTargetChanged reports that the quarantined destination was not
// the verified file; conditionalInstructionReplace then puts it back.
var errReplacementTargetChanged = errors.New("managed-file collision changed before replacement")

func conditionalInstructionReplace(action Action, data []byte, mode os.FileMode, expectedInfo os.FileInfo, expectedHash string, expectedMode os.FileMode, runReplacementHooks bool) (instructionFileMutation, error) {
	var mutation instructionFileMutation
	destination := action.Destination
	if err := ensureInstructionParent(destination); err != nil {
		return mutation, err
	}
	dir := filepath.Dir(destination)
	tmp, err := prepareInstructionTemp(dir, data, mode)
	if err != nil {
		return mutation, err
	}
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	quarantineDir, err := os.MkdirTemp(dir, ".terran-quarantine-*")
	if err != nil {
		return mutation, err
	}
	quarantine := filepath.Join(quarantineDir, "displaced")
	mutation.recovery = quarantine
	if err := os.Rename(destination, quarantine); err != nil {
		_ = os.Remove(quarantineDir)
		return mutation, fmt.Errorf("quarantine expected destination: %w", err)
	}
	if err := syncDirectory(quarantineDir); err != nil {
		return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
	}
	if err := syncDirectory(dir); err != nil {
		return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
	}
	displaced, displacedMode, err := readSafeFile(quarantine, "quarantined managed file")
	if err != nil {
		return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
	}
	displacedInfo, err := os.Lstat(quarantine)
	if err != nil || !os.SameFile(displacedInfo, expectedInfo) || hashBytes(displaced) != expectedHash || displacedMode != expectedMode {
		return mutation, errors.Join(errReplacementTargetChanged, restoreDisplacedNoReplace(destination, quarantine))
	}
	if runReplacementHooks && beforeReplacementInstall != nil {
		if err := beforeReplacementInstall(action); err != nil {
			return mutation, errors.Join(err, restoreDisplacedNoReplace(destination, quarantine))
		}
	}
	if err := os.Link(tmp, destination); err != nil {
		return mutation, errors.Join(fmt.Errorf("install managed file without overwrite: %w", err), restoreDisplacedNoReplace(destination, quarantine))
	}
	mutation.mutated = true
	mutation.info, err = os.Lstat(destination)
	if err != nil {
		return mutation, err
	}
	if runReplacementHooks && beforeReplacementCleanup != nil {
		if err := beforeReplacementCleanup(tmp); err != nil {
			return mutation, err
		}
	}
	if err := os.Remove(tmp); err != nil {
		return mutation, err
	}
	tmp = ""
	if runReplacementHooks && afterReplacementInstall != nil {
		if err := afterReplacementInstall(action); err != nil {
			return mutation, err
		}
	}
	if afterInstructionRename != nil {
		if err := afterInstructionRename(destination); err != nil {
			return mutation, err
		}
	}
	if err := syncDirectory(dir); err != nil {
		return mutation, err
	}
	if err := validateTrustedFile(destination, "installed managed file"); err != nil {
		return mutation, err
	}
	installedInfo, err := os.Lstat(destination)
	if err != nil || !os.SameFile(installedInfo, mutation.info) || installedInfo.Mode().Perm() != mode.Perm() {
		return mutation, fmt.Errorf("installed managed file changed during replacement")
	}
	installedHash, err := fileHash(destination)
	if err != nil || installedHash != hashBytes(data) {
		return mutation, fmt.Errorf("installed managed file changed during replacement")
	}
	if afterInstructionHash != nil {
		if err := afterInstructionHash(destination); err != nil {
			return mutation, err
		}
	}
	displaced, displacedMode, err = readSafeFile(quarantine, "quarantined managed file")
	if err != nil || hashBytes(displaced) != expectedHash || displacedMode != expectedMode {
		return mutation, fmt.Errorf("quarantined original changed during replacement; recovery preserved at %s", quarantine)
	}
	displacedInfo, err = os.Lstat(quarantine)
	if err != nil || !os.SameFile(displacedInfo, expectedInfo) {
		return mutation, fmt.Errorf("quarantined original changed during replacement; recovery preserved at %s", quarantine)
	}
	if err := removeRecoveryFile(quarantine); err != nil {
		return mutation, err
	}
	mutation.recovery = ""
	return mutation, nil
}

func prepareInstructionTemp(dir string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, ".terran-instruction-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode.Perm()); err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return tmp, nil
}

func restoreDisplacedNoReplace(destination, quarantine string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination was recreated; displaced file preserved at %s", quarantine)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination while restoring displaced file: %w; recovery preserved at %s", err, quarantine)
	}
	if err := os.Link(quarantine, destination); err != nil {
		return fmt.Errorf("restore displaced file without overwrite: %w; recovery preserved at %s", err, quarantine)
	}
	if err := removeRecoveryFile(quarantine); err != nil {
		return fmt.Errorf("restored displaced file but cleanup failed: %w", err)
	}
	return nil
}

func removeRecoveryFile(path string) error {
	dir := filepath.Dir(path)
	parent := filepath.Dir(dir)
	if err := os.Remove(path); err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func removeBackupConditional(path string, expectedInfo os.FileInfo, expectedHash string) error {
	if expectedInfo == nil {
		return fmt.Errorf("preserve changed instruction backup %s", path)
	}
	dir := filepath.Dir(path)
	quarantineDir, err := os.MkdirTemp(dir, ".terran-backup-quarantine-*")
	if err != nil {
		return err
	}
	quarantine := filepath.Join(quarantineDir, "displaced")
	if err := os.Rename(path, quarantine); err != nil {
		_ = os.Remove(quarantineDir)
		return fmt.Errorf("preserve changed instruction backup %s: %w", path, err)
	}
	if err := syncDirectory(quarantineDir); err != nil {
		return errors.Join(err, restoreDisplacedNoReplace(path, quarantine))
	}
	if err := syncDirectory(dir); err != nil {
		return errors.Join(err, restoreDisplacedNoReplace(path, quarantine))
	}
	data, mode, err := readSafeFile(quarantine, "rollback instruction backup")
	info, statErr := os.Lstat(quarantine)
	if err != nil || statErr != nil || !os.SameFile(info, expectedInfo) || mode != 0o600 || hashBytes(data) != expectedHash {
		return errors.Join(fmt.Errorf("preserve changed instruction backup %s", path), restoreDisplacedNoReplace(path, quarantine))
	}
	if err := removeRecoveryFile(quarantine); err != nil {
		return fmt.Errorf("remove instruction backup %s: %w", path, err)
	}
	return nil
}

func publishBackup(path string, data []byte) (os.FileInfo, bool, error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, false, err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".terran-backup-*")
	if err != nil {
		return nil, false, err
	}
	tmp := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(0o600); err != nil {
		return nil, false, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, false, err
	}
	if err := f.Sync(); err != nil {
		return nil, false, err
	}
	if err := f.Close(); err != nil {
		return nil, false, err
	}
	if beforeBackupPublish != nil {
		if err := beforeBackupPublish(path); err != nil {
			return nil, false, err
		}
	}
	if err := os.Link(tmp, path); err != nil {
		return nil, false, fmt.Errorf("publish instruction backup without overwrite: %w", err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return nil, true, statErr
	}
	if err := os.Remove(tmp); err != nil {
		return info, true, err
	}
	if err := syncDirectory(dir); err != nil {
		return info, true, err
	}
	return info, true, nil
}

func validatePublishedBackup(path string, data []byte) error {
	if err := validateTrustedFile(path, "instruction backup"); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("instruction backup must be mode 0600")
	}
	hash, err := fileHash(path)
	if err != nil || hash != hashBytes(data) {
		return fmt.Errorf("instruction backup hash verification failed")
	}
	return nil
}

func readSafeFile(path, description string) ([]byte, os.FileMode, error) {
	return readTrustedFile(path, description, 4<<20, 0)
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
