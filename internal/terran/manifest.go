package terran

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const manifestLimit = 1 << 20
const instructionLimit = 1 << 20

var skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var toolNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type LoadedManifest struct {
	Manifest           Manifest
	Repository         string
	Fingerprint        string
	Sources            map[string]string
	SkillHashes        map[string]string // skill -> readSkillTree hash of its source
	InstructionSources map[string]string
	InstructionHashes  map[string]string
	ConfigSources      map[string]string
	ConfigHashes       map[string]string
	FileSources        map[string]string                     // keyed by ItemID
	FileHashes         map[string]string                     // keyed by ItemID
	JSONKeySources     map[string]string                     // keyed by target
	JSONKeyHashes      map[string]string                     // keyed by target
	JSONKeyValues      map[string]map[string]json.RawMessage // target -> key -> canonical value
}

func LoadManifest(repo string) (loaded LoadedManifest, err error) {
	defer func() {
		if err != nil {
			err = Coded(CodeManifestInvalid, nextManifest, err)
		}
	}()
	if !filepath.IsAbs(repo) {
		return LoadedManifest{}, fmt.Errorf("repository path must be absolute")
	}
	canonicalRepo, err := filepath.EvalSymlinks(filepath.Clean(repo))
	if err != nil {
		return LoadedManifest{}, fmt.Errorf("resolve repository: %w", err)
	}
	info, err := os.Stat(canonicalRepo)
	if err != nil || !info.IsDir() {
		return LoadedManifest{}, fmt.Errorf("repository is not a directory")
	}
	if err := validateTrustedDirectory(canonicalRepo, "repository"); err != nil {
		return LoadedManifest{}, err
	}
	manifestPath := filepath.Join(canonicalRepo, "terran.json")
	if err := validateTrustedFile(manifestPath, "manifest"); err != nil {
		return LoadedManifest{}, err
	}
	data, err := readFileLimited(manifestPath, manifestLimit)
	if err != nil {
		return LoadedManifest{}, fmt.Errorf("manifest: %w", err)
	}
	manifest, err := decodeManifest(data)
	if err != nil {
		return LoadedManifest{}, fmt.Errorf("manifest: %w", err)
	}
	if !skillNamePattern.MatchString(manifest.ID) {
		return LoadedManifest{}, fmt.Errorf("invalid repository id %q", manifest.ID)
	}
	if manifest.Version == "" {
		return LoadedManifest{}, fmt.Errorf("manifest version is required")
	}
	sources := make(map[string]string)
	skillHashes := make(map[string]string)
	instructionSources := make(map[string]string)
	instructionHashes := make(map[string]string)
	configSources := make(map[string]string)
	configHashes := make(map[string]string)
	fileSources := make(map[string]string)
	fileHashes := make(map[string]string)
	pairs := make(map[string]bool)
	for i := range manifest.Projections {
		p := &manifest.Projections[i]
		if err := validatePlatforms(p.Platforms); err != nil {
			return LoadedManifest{}, fmt.Errorf("platforms for skill %s: %w", p.Skill, err)
		}
		sort.Strings(p.Platforms)
		if !skillNamePattern.MatchString(p.Skill) {
			return LoadedManifest{}, fmt.Errorf("invalid skill name %q", p.Skill)
		}
		if p.Source == "" || filepath.IsAbs(p.Source) || filepath.Clean(p.Source) != p.Source || p.Source == "." || strings.HasPrefix(p.Source, ".."+string(filepath.Separator)) {
			return LoadedManifest{}, fmt.Errorf("source for %s must be a clean relative path", p.Skill)
		}
		sourcePath := filepath.Join(canonicalRepo, p.Source)
		canonicalSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil {
			return LoadedManifest{}, fmt.Errorf("source for %s: %w", p.Skill, err)
		}
		if !contained(canonicalRepo, canonicalSource) {
			return LoadedManifest{}, fmt.Errorf("source for %s escapes repository", p.Skill)
		}
		if err := validateTrustedSource(canonicalRepo, canonicalSource); err != nil {
			return LoadedManifest{}, fmt.Errorf("source for %s: %w", p.Skill, err)
		}
		md := filepath.Join(canonicalSource, "SKILL.md")
		mdCanonical, err := filepath.EvalSymlinks(md)
		if err != nil || mdCanonical != md || !contained(canonicalRepo, mdCanonical) {
			return LoadedManifest{}, fmt.Errorf("SKILL.md for %s is missing or escapes repository", p.Skill)
		}
		if err := validateTrustedFile(md, "SKILL.md"); err != nil {
			return LoadedManifest{}, fmt.Errorf("SKILL.md for %s: %w", p.Skill, err)
		}
		frontName, err := frontmatterName(mdCanonical)
		if err != nil {
			return LoadedManifest{}, fmt.Errorf("SKILL.md for %s: %w", p.Skill, err)
		}
		if frontName != p.Skill {
			return LoadedManifest{}, fmt.Errorf("SKILL.md frontmatter name %q does not match %q", frontName, p.Skill)
		}
		if len(p.Targets) == 0 {
			return LoadedManifest{}, fmt.Errorf("skill %s has no targets", p.Skill)
		}
		for _, target := range p.Targets {
			if _, ok := lookupTarget("skill", target); !ok {
				return LoadedManifest{}, fmt.Errorf("unsupported target %q", target)
			}
			key := p.Skill + "\x00" + target
			if pairs[key] {
				return LoadedManifest{}, fmt.Errorf("duplicate projection for %s/%s", p.Skill, target)
			}
			pairs[key] = true
		}
		sort.Strings(p.Targets)
		if prior, ok := sources[p.Skill]; ok && prior != canonicalSource {
			return LoadedManifest{}, fmt.Errorf("skill %s has multiple sources", p.Skill)
		}
		if _, ok := skillHashes[p.Skill]; !ok {
			_, hash, err := readSkillTree(canonicalSource)
			if err != nil {
				return LoadedManifest{}, fmt.Errorf("source for %s: %w", p.Skill, err)
			}
			skillHashes[p.Skill] = hash
		}
		sources[p.Skill] = canonicalSource
	}
	sort.Slice(manifest.Projections, func(i, j int) bool {
		if manifest.Projections[i].Skill == manifest.Projections[j].Skill {
			return manifest.Projections[i].Source < manifest.Projections[j].Source
		}
		return manifest.Projections[i].Skill < manifest.Projections[j].Skill
	})
	for i := range manifest.Instructions {
		instruction := &manifest.Instructions[i]
		if err := validatePlatforms(instruction.Platforms); err != nil {
			return LoadedManifest{}, fmt.Errorf("platforms for instruction %s: %w", instruction.Target, err)
		}
		sort.Strings(instruction.Platforms)
		if _, ok := lookupTarget("instruction", instruction.Target); !ok {
			return LoadedManifest{}, fmt.Errorf("unsupported instruction target %q", instruction.Target)
		}
		if _, exists := instructionSources[instruction.Target]; exists {
			return LoadedManifest{}, fmt.Errorf("duplicate instruction target %q", instruction.Target)
		}
		if instruction.Source == "" || filepath.IsAbs(instruction.Source) || filepath.Clean(instruction.Source) != instruction.Source || instruction.Source == "." || strings.HasPrefix(instruction.Source, ".."+string(filepath.Separator)) {
			return LoadedManifest{}, fmt.Errorf("source for %s must be a clean relative path", instruction.Target)
		}
		sourcePath := filepath.Join(canonicalRepo, instruction.Source)
		canonicalSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil || canonicalSource != sourcePath || !contained(canonicalRepo, canonicalSource) {
			return LoadedManifest{}, fmt.Errorf("source for %s is missing or escapes repository", instruction.Target)
		}
		if err := validateTrustedInstructionSource(canonicalRepo, canonicalSource); err != nil {
			return LoadedManifest{}, fmt.Errorf("source for %s: %w", instruction.Target, err)
		}
		data, err := os.ReadFile(canonicalSource)
		if err != nil {
			return LoadedManifest{}, fmt.Errorf("source for %s: %w", instruction.Target, err)
		}
		if len(data) > instructionLimit {
			return LoadedManifest{}, fmt.Errorf("source for %s exceeds %d bytes", instruction.Target, instructionLimit)
		}
		sum := sha256.Sum256(data)
		instructionSources[instruction.Target] = canonicalSource
		instructionHashes[instruction.Target] = hex.EncodeToString(sum[:])
	}
	sort.Slice(manifest.Instructions, func(i, j int) bool { return manifest.Instructions[i].Target < manifest.Instructions[j].Target })
	for i := range manifest.Configs {
		config := &manifest.Configs[i]
		if err := validatePlatforms(config.Platforms); err != nil {
			return LoadedManifest{}, fmt.Errorf("platforms for config %s: %w", config.Target, err)
		}
		sort.Strings(config.Platforms)
		if _, ok := lookupTarget("config", config.Target); !ok {
			return LoadedManifest{}, fmt.Errorf("unsupported config target %q", config.Target)
		}
		if _, exists := configSources[config.Target]; exists {
			return LoadedManifest{}, fmt.Errorf("duplicate config target %q", config.Target)
		}
		if config.Source == "" || filepath.IsAbs(config.Source) || filepath.Clean(config.Source) != config.Source || config.Source == "." || strings.HasPrefix(config.Source, ".."+string(filepath.Separator)) {
			return LoadedManifest{}, fmt.Errorf("source for %s must be a clean relative path", config.Target)
		}
		sourcePath := filepath.Join(canonicalRepo, config.Source)
		canonicalSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil || canonicalSource != sourcePath || !contained(canonicalRepo, canonicalSource) {
			return LoadedManifest{}, fmt.Errorf("source for %s is missing or escapes repository", config.Target)
		}
		if err := validateTrustedInstructionSource(canonicalRepo, canonicalSource); err != nil {
			return LoadedManifest{}, fmt.Errorf("source for %s: %w", config.Target, err)
		}
		data, err := os.ReadFile(canonicalSource)
		if err != nil {
			return LoadedManifest{}, fmt.Errorf("source for %s: %w", config.Target, err)
		}
		if len(data) > instructionLimit {
			return LoadedManifest{}, fmt.Errorf("source for %s exceeds %d bytes", config.Target, instructionLimit)
		}
		spec, _ := lookupTarget("config", config.Target)
		if spec.Validate != nil {
			if err := spec.Validate(data); err != nil {
				return LoadedManifest{}, fmt.Errorf("source for %s: %w", config.Target, err)
			}
		}
		sum := sha256.Sum256(data)
		configSources[config.Target] = canonicalSource
		configHashes[config.Target] = hex.EncodeToString(sum[:])
	}
	sort.Slice(manifest.Configs, func(i, j int) bool { return manifest.Configs[i].Target < manifest.Configs[j].Target })
	for i := range manifest.Files {
		file := &manifest.Files[i]
		spec, ok := lookupTarget("file", file.Target)
		if !ok {
			return LoadedManifest{}, fmt.Errorf("unsupported file target %q", file.Target)
		}
		if err := validatePlatforms(file.Platforms); err != nil {
			return LoadedManifest{}, fmt.Errorf("platforms for file %s/%s: %w", file.Target, file.Name, err)
		}
		sort.Strings(file.Platforms)
		if err := validateFileName(spec, file.Name); err != nil {
			return LoadedManifest{}, err
		}
		id := ItemID("file", file.Target, file.Name)
		if _, exists := fileSources[id]; exists {
			return LoadedManifest{}, fmt.Errorf("duplicate file %s/%s", file.Target, file.Name)
		}
		if file.Source == "" || filepath.IsAbs(file.Source) || filepath.Clean(file.Source) != file.Source || file.Source == "." || strings.HasPrefix(file.Source, ".."+string(filepath.Separator)) {
			return LoadedManifest{}, fmt.Errorf("source for file %s/%s must be a clean relative path", file.Target, file.Name)
		}
		sourcePath := filepath.Join(canonicalRepo, file.Source)
		canonicalSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil || canonicalSource != sourcePath || !contained(canonicalRepo, canonicalSource) {
			return LoadedManifest{}, fmt.Errorf("source for file %s/%s is missing or escapes repository", file.Target, file.Name)
		}
		if err := validateTrustedInstructionSource(canonicalRepo, canonicalSource); err != nil {
			return LoadedManifest{}, fmt.Errorf("source for file %s/%s: %w", file.Target, file.Name, err)
		}
		data, _, err := readTrustedFile(canonicalSource, "file source", instructionLimit, 0)
		if err != nil {
			return LoadedManifest{}, fmt.Errorf("source for file %s/%s: %w", file.Target, file.Name, err)
		}
		sum := sha256.Sum256(data)
		fileSources[id] = canonicalSource
		fileHashes[id] = hex.EncodeToString(sum[:])
	}
	sort.Slice(manifest.Files, func(i, j int) bool {
		if manifest.Files[i].Target != manifest.Files[j].Target {
			return manifest.Files[i].Target < manifest.Files[j].Target
		}
		if manifest.Files[i].Name != manifest.Files[j].Name {
			return manifest.Files[i].Name < manifest.Files[j].Name
		}
		return manifest.Files[i].Source < manifest.Files[j].Source
	})
	jsonKeySources := make(map[string]string)
	jsonKeyHashes := make(map[string]string)
	jsonKeyValues := make(map[string]map[string]json.RawMessage)
	for i := range manifest.JSONKeys {
		item := &manifest.JSONKeys[i]
		if _, ok := lookupTarget("json-keys", item.Target); !ok {
			return LoadedManifest{}, fmt.Errorf("unsupported json-keys target %q", item.Target)
		}
		if err := validatePlatforms(item.Platforms); err != nil {
			return LoadedManifest{}, fmt.Errorf("platforms for json-keys target %s: %w", item.Target, err)
		}
		sort.Strings(item.Platforms)
		if _, exists := jsonKeySources[item.Target]; exists {
			return LoadedManifest{}, fmt.Errorf("duplicate json-keys target %q", item.Target)
		}
		if item.Source == "" || filepath.IsAbs(item.Source) || filepath.Clean(item.Source) != item.Source || item.Source == "." || strings.HasPrefix(item.Source, ".."+string(filepath.Separator)) {
			return LoadedManifest{}, fmt.Errorf("source for json-keys target %s must be a clean relative path", item.Target)
		}
		sourcePath := filepath.Join(canonicalRepo, item.Source)
		canonicalSource, err := filepath.EvalSymlinks(sourcePath)
		if err != nil || canonicalSource != sourcePath || !contained(canonicalRepo, canonicalSource) {
			return LoadedManifest{}, fmt.Errorf("source for json-keys target %s is missing or escapes repository", item.Target)
		}
		if err := validateTrustedInstructionSource(canonicalRepo, canonicalSource); err != nil {
			return LoadedManifest{}, fmt.Errorf("source for json-keys target %s: %w", item.Target, err)
		}
		data, _, err := readTrustedFile(canonicalSource, "json-keys source", instructionLimit, 0)
		if err != nil {
			return LoadedManifest{}, fmt.Errorf("source for json-keys target %s: %w", item.Target, err)
		}
		values, err := jsonKeyValuesFromSource(item.Target, data)
		if err != nil {
			return LoadedManifest{}, fmt.Errorf("source for json-keys target %s: %w", item.Target, err)
		}
		jsonKeySources[item.Target] = canonicalSource
		jsonKeyHashes[item.Target] = hashBytes(data)
		jsonKeyValues[item.Target] = values
	}
	sort.Slice(manifest.JSONKeys, func(i, j int) bool {
		if manifest.JSONKeys[i].Target != manifest.JSONKeys[j].Target {
			return manifest.JSONKeys[i].Target < manifest.JSONKeys[j].Target
		}
		return manifest.JSONKeys[i].Source < manifest.JSONKeys[j].Source
	})
	for i := range manifest.Tools {
		tool := &manifest.Tools[i]
		if !toolNamePattern.MatchString(tool.Name) {
			return LoadedManifest{}, fmt.Errorf("invalid tool name %q", tool.Name)
		}
		if err := validatePlatforms(tool.Platforms); err != nil {
			return LoadedManifest{}, fmt.Errorf("platforms for tool %s: %w", tool.Name, err)
		}
		sort.Strings(tool.Platforms)
	}
	sort.Slice(manifest.Tools, func(i, j int) bool { return manifest.Tools[i].Name < manifest.Tools[j].Name })
	normalized, _ := json.Marshal(manifest)
	sum := sha256.Sum256(normalized)
	return LoadedManifest{manifest, canonicalRepo, hex.EncodeToString(sum[:]), sources, skillHashes, instructionSources, instructionHashes, configSources, configHashes, fileSources, fileHashes, jsonKeySources, jsonKeyHashes, jsonKeyValues}, nil
}

func validatePlatforms(platforms []string) error {
	if platforms != nil && len(platforms) == 0 {
		return fmt.Errorf("must not be empty when present")
	}
	seen := make(map[string]bool)
	for _, platform := range platforms {
		if platform != "darwin" && platform != "linux" {
			return fmt.Errorf("unsupported platform %q", platform)
		}
		if seen[platform] {
			return fmt.Errorf("duplicate platform %q", platform)
		}
		seen[platform] = true
	}
	return nil
}

// Catalogs is the enrolled primary catalog plus an optional private overlay
// that may only add items.
type Catalogs struct {
	Primary     LoadedManifest
	Overlay     *LoadedManifest // nil when the enrollment has no overlay
	Fingerprint string          // sha256 over Primary.Fingerprint + "\n" + Overlay.Fingerprint (or "" when nil)
}

func LoadCatalogs(e Enrollment) (Catalogs, error) {
	primary, err := LoadManifest(e.RepositoryPath)
	if err != nil {
		return Catalogs{}, err
	}
	if primary.Manifest.ID != e.RepositoryID {
		return Catalogs{}, Coded(CodeRepositoryMismatch, nextEnroll, fmt.Errorf("enrolled repository id changed"))
	}
	if e.OverlayPath == "" {
		return combineCatalogs(primary, nil)
	}
	unavailable := func(err error) error {
		return Coded(CodeOverlayUnavailable, "clone the private catalog to "+e.OverlayPath+" or re-enroll", fmt.Errorf("overlay catalog unavailable: %w", err))
	}
	overlay, err := LoadManifest(e.OverlayPath)
	if err != nil {
		return Catalogs{}, unavailable(err)
	}
	if overlay.Manifest.ID != e.OverlayID || overlay.Repository != e.OverlayPath {
		return Catalogs{}, unavailable(fmt.Errorf("enrolled overlay id or path changed"))
	}
	return combineCatalogs(primary, &overlay)
}

func combineCatalogs(primary LoadedManifest, overlay *LoadedManifest) (Catalogs, error) {
	catalogs := Catalogs{Primary: primary, Overlay: overlay}
	overlayFingerprint := ""
	if overlay != nil {
		p, o := primary.Manifest, overlay.Manifest
		duplicate := func(what string) error {
			return Coded(CodeManifestInvalid, nextManifest, fmt.Errorf("%s is declared by both catalogs %s and %s", what, p.ID, o.ID))
		}
		if o.ID == p.ID {
			return Catalogs{}, Coded(CodeManifestInvalid, nextManifest, fmt.Errorf("overlay catalog id %q must differ from the primary catalog id", o.ID))
		}
		for skill := range overlay.Sources {
			if _, ok := primary.Sources[skill]; ok {
				return Catalogs{}, duplicate("skill " + skill)
			}
		}
		for target := range overlay.InstructionSources {
			if _, ok := primary.InstructionSources[target]; ok {
				return Catalogs{}, duplicate("instruction target " + target)
			}
		}
		for target := range overlay.ConfigSources {
			if _, ok := primary.ConfigSources[target]; ok {
				return Catalogs{}, duplicate("config target " + target)
			}
		}
		for _, item := range o.JSONKeys {
			for _, prior := range p.JSONKeys {
				if item.Target == prior.Target {
					return Catalogs{}, duplicate("json-keys target " + item.Target)
				}
			}
		}
		for _, item := range o.Files {
			for _, prior := range p.Files {
				if item.Target == prior.Target && item.Name == prior.Name {
					return Catalogs{}, duplicate("file " + item.Target + "/" + item.Name)
				}
			}
		}
		for _, tool := range o.Tools {
			for _, prior := range p.Tools {
				if tool.Name == prior.Name {
					return Catalogs{}, duplicate("tool " + tool.Name)
				}
			}
		}
		overlayFingerprint = overlay.Fingerprint
	}
	sum := sha256.Sum256([]byte(primary.Fingerprint + "\n" + overlayFingerprint))
	catalogs.Fingerprint = hex.EncodeToString(sum[:])
	return catalogs, nil
}

// list returns the primary catalog followed by the overlay, if any.
func (c Catalogs) list() []LoadedManifest {
	if c.Overlay == nil {
		return []LoadedManifest{c.Primary}
	}
	return []LoadedManifest{c.Primary, *c.Overlay}
}

// catalog returns the loaded catalog an item or receipt entry belongs to.
func (c Catalogs) catalog(id string) (LoadedManifest, error) {
	for _, loaded := range c.list() {
		if loaded.Manifest.ID == id {
			return loaded, nil
		}
	}
	return LoadedManifest{}, fmt.Errorf("unknown catalog %q", id)
}

// insideAny reports whether path lies inside any catalog repository.
func (c Catalogs) insideAny(path string) bool {
	for _, loaded := range c.list() {
		if contained(loaded.Repository, path) {
			return true
		}
	}
	return false
}

func revalidateCatalogs(expected Catalogs) error {
	enrollment := Enrollment{RepositoryID: expected.Primary.Manifest.ID, RepositoryPath: expected.Primary.Repository}
	if expected.Overlay != nil {
		enrollment.OverlayID, enrollment.OverlayPath = expected.Overlay.Manifest.ID, expected.Overlay.Repository
	}
	current, err := LoadCatalogs(enrollment)
	if err != nil {
		return err
	}
	if current.Fingerprint != expected.Fingerprint {
		return fmt.Errorf("catalog manifest changed during apply")
	}
	return nil
}

func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func frontmatterName(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	if !s.Scan() || strings.TrimSpace(s.Text()) != "---" {
		return "", fmt.Errorf("missing YAML frontmatter")
	}
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "---" {
			return "", fmt.Errorf("frontmatter name is missing")
		}
		if strings.HasPrefix(line, "name:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			name = strings.Trim(name, `"'`)
			if name == "" {
				return "", fmt.Errorf("frontmatter name is empty")
			}
			return name, nil
		}
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("unterminated frontmatter")
}

const (
	skillEntryLimit = 2000
	skillByteLimit  = 32 << 20
)

// skillEntry is one file or directory below a skill root. Modes are
// normalized: 0755 for directories and executable files, otherwise 0644.
type skillEntry struct {
	path string // slash-separated, relative to the skill root
	dir  bool
	mode os.FileMode
	data []byte
}

// readSkillTree reads a skill directory without following symlinks. It
// returns the entries sorted by path and the tree hash: sha256 over one
// "<type>\x00<path>\x00<mode>\x00<content sha256>\n" line per entry, where
// type is "dir" or "file" and directories have an empty content hash. Only
// trusted real directories and regular files are allowed, within
// skillEntryLimit entries and skillByteLimit bytes.
func readSkillTree(root string) ([]skillEntry, string, error) {
	if err := validateTrustedDirectory(root, "skill directory"); err != nil {
		return nil, "", err
	}
	var entries []skillEntry
	var total int64
	var walk func(dir, relative string) error
	walk = func(dir, relative string) error {
		children, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, child := range children {
			if len(entries) >= skillEntryLimit {
				return fmt.Errorf("skill %s has more than %d entries", root, skillEntryLimit)
			}
			path := filepath.Join(dir, child.Name())
			entry := skillEntry{path: child.Name()}
			if relative != "" {
				entry.path = relative + "/" + child.Name()
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				if err := validateTrustedDirectory(path, "skill directory"); err != nil {
					return err
				}
				entry.dir, entry.mode = true, 0o755
				entries = append(entries, entry)
				if err := walk(path, entry.path); err != nil {
					return err
				}
			case info.Mode().IsRegular():
				data, mode, err := readTrustedFile(path, "skill file", skillByteLimit-total, 0)
				if err != nil {
					return err
				}
				total += int64(len(data))
				entry.mode, entry.data = 0o644, data
				if mode&0o111 != 0 {
					entry.mode = 0o755
				}
				entries = append(entries, entry)
			default:
				return fmt.Errorf("skill entry %s must be a regular file or directory", path)
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	h := sha256.New()
	for _, entry := range entries {
		kind, sum := "dir", ""
		if !entry.dir {
			kind, sum = "file", hashBytes(entry.data)
		}
		fmt.Fprintf(h, "%s\x00%s\x00%04o\x00%s\n", kind, entry.path, entry.mode, sum)
	}
	return entries, hex.EncodeToString(h.Sum(nil)), nil
}

// skillTreeHash is the readSkillTree hash of a directory.
func skillTreeHash(root string) (string, error) {
	_, hash, err := readSkillTree(root)
	return hash, err
}
