package terran

import "time"

const SchemaVersion = 1

type Manifest struct {
	SchemaVersion int           `json:"schema_version"`
	ID            string        `json:"id"`
	Version       string        `json:"version"`
	Projections   []Projection  `json:"projections"`
	Instructions  []Instruction `json:"instructions,omitempty"`
	Configs       []Config      `json:"configs,omitempty"`
}

type Projection struct {
	Skill   string   `json:"skill"`
	Source  string   `json:"source"`
	Targets []string `json:"targets"`
}

type Instruction struct {
	Target string `json:"target"`
	Source string `json:"source"`
}

type Config struct {
	Target string `json:"target"`
	Source string `json:"source"`
}

type Enrollment struct {
	SchemaVersion   int    `json:"schema_version"`
	RepositoryID    string `json:"repository_id"`
	RepositoryPath  string `json:"repository_path"`
	CommandCenterID string `json:"command_center_id"`
	DisplayName     string `json:"display_name"`
}

type Receipt struct {
	SchemaVersion       int                  `json:"schema_version"`
	RepositoryID        string               `json:"repository_id"`
	RepositoryPath      string               `json:"repository_path"`
	RepositoryVersion   string               `json:"repository_version"`
	ManifestFingerprint string               `json:"manifest_fingerprint"`
	Projections         []ReceiptProjection  `json:"projections"`
	Instructions        []ReceiptInstruction `json:"instructions,omitempty"`
	Configs             []ReceiptConfig      `json:"configs,omitempty"`
}

type ReceiptInstruction struct {
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

type ReceiptConfig ReceiptInstruction

type ReceiptProjection struct {
	Skill              string    `json:"skill"`
	Target             string    `json:"target"`
	Source             string    `json:"source"`
	Destination        string    `json:"destination"`
	Strategy           string    `json:"strategy"`
	AppliedAt          time.Time `json:"applied_at"`
	TerranBuildVersion string    `json:"terran_build_version"`
}

type Action struct {
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Skill       string `json:"skill,omitempty"`
	Target      string `json:"target"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Reason      string `json:"reason,omitempty"`
}

type PlanResult struct {
	SchemaVersion int      `json:"schema_version"`
	Clean         bool     `json:"clean"`
	Actions       []Action `json:"actions"`
}

type CollisionDecision string

const (
	CollisionReplace CollisionDecision = "replace"
	CollisionSkip    CollisionDecision = "skip"
	CollisionAbort   CollisionDecision = "abort"
)

// ApplyOptions enables human decisions inside ApplyWithOptions' lock. Callbacks
// receive plan metadata, never file contents.
type ApplyOptions struct {
	ResolveCollision func(Action) (CollisionDecision, error)
	ConfirmPlan      func(PlanResult) error
}

type StatusItem struct {
	Kind        string `json:"kind"`
	Skill       string `json:"skill,omitempty"`
	Target      string `json:"target"`
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
