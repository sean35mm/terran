package terran

import (
	"encoding/json"
	"time"
)

const SchemaVersion = 2

type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Version       string         `json:"version"`
	Projections   []Projection   `json:"projections"`
	Instructions  []Instruction  `json:"instructions,omitempty"`
	Configs       []Config       `json:"configs,omitempty"`
	Files         []FileItem     `json:"files,omitempty"`
	JSONKeys      []JSONKeysItem `json:"json_keys,omitempty"`
	Tools         []Tool         `json:"tools,omitempty"`
}

type Projection struct {
	Skill     string   `json:"skill"`
	Source    string   `json:"source"`
	Targets   []string `json:"targets"`
	Platforms []string `json:"platforms,omitempty"`
}

type Instruction struct {
	Target    string   `json:"target"`
	Source    string   `json:"source"`
	Platforms []string `json:"platforms,omitempty"`
}

type Config struct {
	Target    string   `json:"target"`
	Source    string   `json:"source"`
	Platforms []string `json:"platforms,omitempty"`
}

type FileItem struct {
	Target    string   `json:"target"`
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Platforms []string `json:"platforms,omitempty"`
}

type JSONKeysItem struct {
	Target    string   `json:"target"`
	Source    string   `json:"source"`
	Platforms []string `json:"platforms,omitempty"`
}

type Tool struct {
	Name      string   `json:"name"`
	Platforms []string `json:"platforms,omitempty"`
}

type Enrollment struct {
	SchemaVersion   int      `json:"schema_version"`
	RepositoryID    string   `json:"repository_id"`
	RepositoryPath  string   `json:"repository_path"`
	CommandCenterID string   `json:"command_center_id"`
	DisplayName     string   `json:"display_name"`
	OverlayID       string   `json:"overlay_id,omitempty"`
	OverlayPath     string   `json:"overlay_path,omitempty"`
	Holds           []string `json:"holds,omitempty"`
}

type Receipt struct {
	SchemaVersion       int                 `json:"schema_version"`
	RepositoryID        string              `json:"repository_id"`
	RepositoryPath      string              `json:"repository_path"`
	RepositoryVersion   string              `json:"repository_version"`
	ManifestFingerprint string              `json:"manifest_fingerprint"`
	Projections         []ReceiptProjection `json:"projections"`
	Managed             []ReceiptManaged    `json:"managed,omitempty"`
	JSONKeys            []ReceiptJSONKey    `json:"json_keys,omitempty"`
}

// ReceiptJSONKey records one owned top-level key in a shared JSON settings
// file. Hashes are sha256 over the canonical JSON of the value.
type ReceiptJSONKey struct {
	Catalog            string          `json:"catalog"`
	Target             string          `json:"target"`
	Key                string          `json:"key"`
	AppliedHash        string          `json:"applied_hash"`
	Origin             string          `json:"origin"`
	OriginalValue      json.RawMessage `json:"original_value,omitempty"`
	AppliedAt          time.Time       `json:"applied_at"`
	TerranBuildVersion string          `json:"terran_build_version"`
}

type ReceiptManaged struct {
	Kind               string    `json:"kind"`
	Catalog            string    `json:"catalog"`
	Target             string    `json:"target"`
	Name               string    `json:"name,omitempty"`
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

// ReceiptProjection records one owned skill. Strategy "copy" is a managed
// directory copy whose AppliedHash is the readSkillTree hash Terran wrote;
// "symlink" is a legacy live link, converted to a copy by the next apply.
type ReceiptProjection struct {
	Catalog            string    `json:"catalog"`
	Skill              string    `json:"skill"`
	Target             string    `json:"target"`
	Source             string    `json:"source"`
	Destination        string    `json:"destination"`
	Strategy           string    `json:"strategy"`
	AppliedHash        string    `json:"applied_hash,omitempty"`
	Origin             string    `json:"origin,omitempty"` // copy only: "created" or "adopted"
	AppliedAt          time.Time `json:"applied_at"`
	TerranBuildVersion string    `json:"terran_build_version"`
}

type Action struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Catalog     string `json:"catalog"`
	Skill       string `json:"skill,omitempty"`
	Target      string `json:"target"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Reason      string `json:"reason,omitempty"`
}

type PlanResult struct {
	SchemaVersion int      `json:"schema_version"`
	Clean         bool     `json:"clean"`
	Digest        string   `json:"digest"`
	Actions       []Action `json:"actions"`
	// jsonFiles maps each json-keys target the plan inspected to the sha256
	// of the whole settings file ("" when missing); apply refuses to write
	// if the file no longer matches.
	jsonFiles map[string]string
	// collisions maps each blocked_collision item id to a hash of the content
	// at its destination, so the digest changes when that content does.
	collisions map[string]string
}

type CollisionDecision string

const (
	CollisionReplace CollisionDecision = "replace"
	CollisionSkip    CollisionDecision = "skip"
	CollisionAbort   CollisionDecision = "abort"
	// CollisionKeep leaves the destination untouched and holds the item.
	CollisionKeep CollisionDecision = "keep"
)

// ApplyOptions enables decisions inside ApplyWithOptions' lock. Callbacks
// receive plan metadata, never file contents. Decisions maps item ids to
// CollisionReplace or CollisionKeep and takes precedence over ResolveCollision;
// each id must be a blocked collision in the plan. ExpectDigest, when set,
// must equal the digest of the plan computed under the lock.
type ApplyOptions struct {
	ResolveCollision func(Action) (CollisionDecision, error)
	ConfirmPlan      func(PlanResult) error
	Decisions        map[string]CollisionDecision
	ExpectDigest     string
}

type StatusItem struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Skill       string `json:"skill,omitempty"`
	Target      string `json:"target"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Detail      string `json:"detail,omitempty"`
}

type StatusResult struct {
	SchemaVersion int          `json:"schema_version"`
	Clean         bool         `json:"clean"`
	Items         []StatusItem `json:"items"`
}

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DoctorResult struct {
	SchemaVersion int     `json:"schema_version"`
	Healthy       bool    `json:"healthy"`
	Checks        []Check `json:"checks"`
}
