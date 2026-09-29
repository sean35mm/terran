package terran

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

type manifestV1 struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Version       string          `json:"version"`
	Projections   []projectionV1  `json:"projections"`
	Instructions  []instructionV1 `json:"instructions,omitempty"`
	Configs       []configV1      `json:"configs,omitempty"`
}

type projectionV1 struct {
	Skill   string   `json:"skill"`
	Source  string   `json:"source"`
	Targets []string `json:"targets"`
}

type instructionV1 struct {
	Target string `json:"target"`
	Source string `json:"source"`
}

type configV1 struct {
	Target string `json:"target"`
	Source string `json:"source"`
}

type enrollmentV1 struct {
	SchemaVersion   int    `json:"schema_version"`
	RepositoryID    string `json:"repository_id"`
	RepositoryPath  string `json:"repository_path"`
	CommandCenterID string `json:"command_center_id"`
	DisplayName     string `json:"display_name"`
}

type receiptV1 struct {
	SchemaVersion       int                    `json:"schema_version"`
	RepositoryID        string                 `json:"repository_id"`
	RepositoryPath      string                 `json:"repository_path"`
	RepositoryVersion   string                 `json:"repository_version"`
	ManifestFingerprint string                 `json:"manifest_fingerprint"`
	Projections         []receiptProjectionV1  `json:"projections"`
	Instructions        []receiptInstructionV1 `json:"instructions,omitempty"`
	Configs             []receiptConfigV1      `json:"configs,omitempty"`
}

type receiptProjectionV1 struct {
	Skill              string    `json:"skill"`
	Target             string    `json:"target"`
	Source             string    `json:"source"`
	Destination        string    `json:"destination"`
	Strategy           string    `json:"strategy"`
	AppliedAt          time.Time `json:"applied_at"`
	TerranBuildVersion string    `json:"terran_build_version"`
}

type receiptInstructionV1 struct {
	Target             string    `json:"target"`
	Source             string    `json:"source"`
	Destination        string    `json:"destination"`
	Strategy           string    `json:"strategy"`
	SourceHash         string    `json:"source_hash"`
	AppliedHash        string    `json:"applied_hash"`
	Origin             string    `json:"origin"`
	OriginalHash       string    `json:"original_hash,omitempty"`
	OriginalMode       uint32    `json:"original_mode,omitempty"`
	Backup             string    `json:"backup,omitempty"`
	AppliedAt          time.Time `json:"applied_at"`
	TerranBuildVersion string    `json:"terran_build_version"`
}

type receiptConfigV1 receiptInstructionV1

func decodeManifest(data []byte) (Manifest, error) {
	version, err := peekSchemaVersion(data)
	if err != nil {
		return Manifest{}, err
	}
	switch version {
	case 1:
		var manifest manifestV1
		if err := decodeStrict(data, &manifest); err != nil {
			return Manifest{}, err
		}
		return upgradeManifestV1(manifest), nil
	case SchemaVersion:
		var manifest Manifest
		if err := decodeStrict(data, &manifest); err != nil {
			return Manifest{}, err
		}
		return manifest, nil
	default:
		return Manifest{}, fmt.Errorf("unsupported manifest schema_version %d", version)
	}
}

func decodeEnrollment(data []byte) (Enrollment, error) {
	version, err := peekSchemaVersion(data)
	if err != nil {
		return Enrollment{}, err
	}
	switch version {
	case 1:
		var enrollment enrollmentV1
		if err := decodeStrict(data, &enrollment); err != nil {
			return Enrollment{}, err
		}
		return upgradeEnrollmentV1(enrollment), nil
	case SchemaVersion:
		var enrollment Enrollment
		if err := decodeStrict(data, &enrollment); err != nil {
			return Enrollment{}, err
		}
		return enrollment, nil
	default:
		return Enrollment{}, fmt.Errorf("unsupported enrollment schema_version %d", version)
	}
}

func decodeReceipt(data []byte) (Receipt, error) {
	version, err := peekSchemaVersion(data)
	if err != nil {
		return Receipt{}, err
	}
	switch version {
	case 1:
		var receipt receiptV1
		if err := decodeStrict(data, &receipt); err != nil {
			return Receipt{}, err
		}
		return upgradeReceiptV1(receipt), nil
	case SchemaVersion:
		var receipt Receipt
		if err := decodeStrict(data, &receipt); err != nil {
			return Receipt{}, err
		}
		return receipt, nil
	default:
		return Receipt{}, fmt.Errorf("unsupported receipt schema_version %d", version)
	}
}

func peekSchemaVersion(data []byte) (int, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return 0, fmt.Errorf("JSON value must be an object")
	}
	seen := make(map[string]bool)
	var schema json.RawMessage
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return 0, fmt.Errorf("read object key: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return 0, fmt.Errorf("JSON object key must be a string")
		}
		if seen[key] {
			return 0, fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return 0, fmt.Errorf("read field %q: %w", key, err)
		}
		if key == "schema_version" {
			schema = value
		}
	}
	if _, err := dec.Token(); err != nil {
		return 0, fmt.Errorf("close JSON object: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return 0, fmt.Errorf("trailing JSON value")
		}
		return 0, fmt.Errorf("trailing JSON: %w", err)
	}
	if schema == nil {
		return 0, fmt.Errorf("schema_version is required")
	}
	var version int
	if err := decodeStrict(schema, &version); err != nil {
		return 0, fmt.Errorf("invalid schema_version: %w", err)
	}
	return version, nil
}

func upgradeManifestV1(old manifestV1) Manifest {
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		ID:            old.ID,
		Version:       old.Version,
	}
	if old.Projections != nil {
		manifest.Projections = make([]Projection, len(old.Projections))
	}
	if old.Instructions != nil {
		manifest.Instructions = make([]Instruction, len(old.Instructions))
	}
	if old.Configs != nil {
		manifest.Configs = make([]Config, len(old.Configs))
	}
	for i, projection := range old.Projections {
		manifest.Projections[i] = Projection{Skill: projection.Skill, Source: projection.Source, Targets: cloneStrings(projection.Targets)}
	}
	for i, instruction := range old.Instructions {
		manifest.Instructions[i] = Instruction{Target: instruction.Target, Source: instruction.Source}
	}
	for i, config := range old.Configs {
		manifest.Configs[i] = Config{Target: config.Target, Source: config.Source}
	}
	return manifest
}

func upgradeEnrollmentV1(old enrollmentV1) Enrollment {
	return Enrollment{
		SchemaVersion:   SchemaVersion,
		RepositoryID:    old.RepositoryID,
		RepositoryPath:  old.RepositoryPath,
		CommandCenterID: old.CommandCenterID,
		DisplayName:     old.DisplayName,
	}
}

func upgradeReceiptV1(old receiptV1) Receipt {
	receipt := Receipt{
		SchemaVersion:       SchemaVersion,
		RepositoryID:        old.RepositoryID,
		RepositoryPath:      old.RepositoryPath,
		RepositoryVersion:   old.RepositoryVersion,
		ManifestFingerprint: old.ManifestFingerprint,
	}
	if old.Projections != nil {
		receipt.Projections = make([]ReceiptProjection, len(old.Projections))
	}
	if len(old.Instructions)+len(old.Configs) != 0 {
		receipt.Managed = make([]ReceiptManaged, 0, len(old.Instructions)+len(old.Configs))
	}
	for i, projection := range old.Projections {
		receipt.Projections[i] = ReceiptProjection{
			Catalog:            old.RepositoryID,
			Skill:              projection.Skill,
			Target:             projection.Target,
			Source:             projection.Source,
			Destination:        projection.Destination,
			Strategy:           projection.Strategy,
			AppliedAt:          projection.AppliedAt,
			TerranBuildVersion: projection.TerranBuildVersion,
		}
	}
	for _, instruction := range old.Instructions {
		receipt.Managed = append(receipt.Managed, upgradeReceiptManagedV1(old.RepositoryID, "instruction", instruction))
	}
	for _, config := range old.Configs {
		receipt.Managed = append(receipt.Managed, upgradeReceiptManagedV1(old.RepositoryID, "config", receiptInstructionV1(config)))
	}
	return receipt
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}

func upgradeReceiptManagedV1(catalog, kind string, old receiptInstructionV1) ReceiptManaged {
	return ReceiptManaged{
		Kind:               kind,
		Catalog:            catalog,
		Target:             old.Target,
		Source:             old.Source,
		Destination:        old.Destination,
		Strategy:           old.Strategy,
		SourceHash:         old.SourceHash,
		AppliedHash:        old.AppliedHash,
		Origin:             old.Origin,
		OriginalHash:       old.OriginalHash,
		OriginalMode:       old.OriginalMode,
		Backup:             old.Backup,
		AppliedAt:          old.AppliedAt,
		TerranBuildVersion: old.TerranBuildVersion,
	}
}

func ItemID(kind, target, name string) string {
	if name == "" {
		return kind + "/" + target
	}
	return kind + "/" + target + "/" + name
}

func ParseItemID(id string) (kind, target, name string, err error) {
	invalid := func() (string, string, string, error) {
		return "", "", "", Coded(CodeUnknownItem, "", fmt.Errorf("invalid item id %q", id))
	}
	if id == "" || strings.IndexFunc(id, unicode.IsSpace) >= 0 || strings.Contains(id, "..") {
		return invalid()
	}
	parts := strings.Split(id, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return invalid()
	}
	for _, part := range parts {
		if part == "" {
			return invalid()
		}
	}
	kind, target = parts[0], parts[1]
	switch kind {
	case "skill", "file", "json-keys":
		if len(parts) != 3 {
			return invalid()
		}
		name = parts[2]
	case "instruction", "config":
		if len(parts) != 2 {
			return invalid()
		}
	default:
		return invalid()
	}
	return kind, target, name, nil
}
