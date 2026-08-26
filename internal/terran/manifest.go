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

type LoadedManifest struct {
	Manifest           Manifest
	Repository         string
	Fingerprint        string
	Sources            map[string]string
	InstructionSources map[string]string
	InstructionHashes  map[string]string
	ConfigSources      map[string]string
	ConfigHashes       map[string]string
}

func LoadManifest(repo string) (LoadedManifest, error) {
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
	var manifest Manifest
	if err := readStrict(manifestPath, &manifest, manifestLimit); err != nil {
		return LoadedManifest{}, fmt.Errorf("manifest: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion {
		return LoadedManifest{}, fmt.Errorf("unsupported manifest schema_version %d", manifest.SchemaVersion)
	}
	if !skillNamePattern.MatchString(manifest.ID) {
		return LoadedManifest{}, fmt.Errorf("invalid repository id %q", manifest.ID)
	}
	if manifest.Version == "" {
		return LoadedManifest{}, fmt.Errorf("manifest version is required")
	}
	sources := make(map[string]string)
	instructionSources := make(map[string]string)
	instructionHashes := make(map[string]string)
	configSources := make(map[string]string)
	configHashes := make(map[string]string)
	pairs := make(map[string]bool)
	for i := range manifest.Projections {
		p := &manifest.Projections[i]
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
			if target != "agents" && target != "claude" {
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
		if instruction.Target != "claude-global" && instruction.Target != "opencode-global" {
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
		if config.Target != "opencode-config" {
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
		if err := validateOpenCodeConfig(data); err != nil {
			return LoadedManifest{}, fmt.Errorf("source for %s: %w", config.Target, err)
		}
		sum := sha256.Sum256(data)
		configSources[config.Target] = canonicalSource
		configHashes[config.Target] = hex.EncodeToString(sum[:])
	}
	sort.Slice(manifest.Configs, func(i, j int) bool { return manifest.Configs[i].Target < manifest.Configs[j].Target })
	normalized, _ := json.Marshal(manifest)
	sum := sha256.Sum256(normalized)
	return LoadedManifest{manifest, canonicalRepo, hex.EncodeToString(sum[:]), sources, instructionSources, instructionHashes, configSources, configHashes}, nil
}

func revalidateLoadedManifest(expected LoadedManifest) error {
	current, err := LoadManifest(expected.Repository)
	if err != nil {
		return err
	}
	if current.Manifest.ID != expected.Manifest.ID || current.Manifest.Version != expected.Manifest.Version || current.Fingerprint != expected.Fingerprint {
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
