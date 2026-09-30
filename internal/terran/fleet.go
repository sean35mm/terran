package terran

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	inventoryFile  = "command-centers.json"
	inventoryLimit = 1 << 20
	sshOutputLimit = 1 << 20
)

var sshAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// sshUserPattern matches login names on macOS and Linux.
var sshUserPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
var summaryVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
var summaryCommitPattern = regexp.MustCompile(`^[0-9a-f]{7}$`)

type Inventory struct {
	SchemaVersion  int             `json:"schema_version"`
	CommandCenters []CommandCenter `json:"command_centers"`
}

type CommandCenter struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	SSH      string `json:"ssh"`
	// User is the login name on that machine when it differs from the
	// connecting user's; empty uses ssh's own default or ssh config.
	User string `json:"user,omitempty"`
}

type MachineSummary struct {
	Name          string `json:"name"`
	Platform      string `json:"platform"`
	Local         bool   `json:"local"`
	Reachable     bool   `json:"reachable"`
	TerranVersion string `json:"terran_version,omitempty"`
	CatalogCommit string `json:"catalog_commit,omitempty"`
	OverlayCommit string `json:"overlay_commit,omitempty"`
	Clean         bool   `json:"clean"`
	Healthy       bool   `json:"healthy"`
	Held          int    `json:"held"`
	Drifted       int    `json:"drifted"`
	Blocked       int    `json:"blocked"`
	// ToolsMissing counts catalog tools not on PATH. They do not make a
	// machine unhealthy: non-interactive ssh often has a shorter PATH.
	ToolsMissing int `json:"tools_missing"`
	// Git state of the catalog and overlay checkouts: uncommitted paths and
	// commits ahead of or behind the upstream (no fetch). Nil when git, the
	// repository, or the upstream is unavailable. They do not affect Clean or Healthy.
	CatalogDirty  *int   `json:"catalog_dirty,omitempty"`
	CatalogAhead  *int   `json:"catalog_ahead,omitempty"`
	CatalogBehind *int   `json:"catalog_behind,omitempty"`
	OverlayDirty  *int   `json:"overlay_dirty,omitempty"`
	OverlayAhead  *int   `json:"overlay_ahead,omitempty"`
	OverlayBehind *int   `json:"overlay_behind,omitempty"`
	Error         string `json:"error,omitempty"`
}

// runSSH runs the remote terran with a fixed argument vector; nothing goes through a shell.
// It is a variable so tests can stub it.
var runSSH = func(ctx context.Context, target CommandCenter, args ...string) ([]byte, error) {
	if !sshAliasPattern.MatchString(target.SSH) || (target.User != "" && !sshUserPattern.MatchString(target.User)) {
		return nil, fmt.Errorf("invalid ssh alias or user")
	}
	options := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}
	if target.User != "" {
		options = append(options, "-o", "User="+target.User)
	}
	cmd := exec.CommandContext(ctx, "ssh", append(append(options, target.SSH, ".local/bin/terran"), args...)...)
	out := &cappedBuffer{limit: sshOutputLimit}
	cmd.Stdout = out
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if out.over {
		return nil, errSSHOutputTooLarge
	}
	return out.Bytes(), err
}

var errSSHOutputTooLarge = errors.New("remote output too large")

// cappedBuffer keeps at most limit bytes and reports overflow instead of growing.
type cappedBuffer struct {
	bytes.Buffer
	limit int
	over  bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); len(p) > room {
		b.over = true
		p = p[:room]
		b.Buffer.Write(p)
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

var gitHead = func(repo string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runGit runs git in repo with a fixed argument vector and a short timeout.
func runGit(repo string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-C", repo}, args...)...).Output()
	return string(out), err
}

// gitState reports the uncommitted path count and the ahead/behind counts
// against the upstream; each is nil when git cannot tell.
func gitState(repo string) (dirty, ahead, behind *int) {
	if repo == "" {
		return nil, nil, nil
	}
	if out, err := runGit(repo, "status", "--porcelain"); err == nil {
		n := strings.Count(out, "\n")
		dirty = &n
	}
	if out, err := runGit(repo, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
		var a, b int
		if _, err := fmt.Sscanf(out, "%d\t%d", &a, &b); err == nil {
			ahead, behind = &a, &b
		}
	}
	return dirty, ahead, behind
}

func shortCommit(repo string) string {
	if repo == "" {
		return ""
	}
	head, err := gitHead(repo)
	if err != nil || len(head) < 7 {
		return ""
	}
	return head[:7]
}

// LoadInventory reads command-centers.json from the overlay repository root.
func LoadInventory(overlayPath string) (Inventory, error) {
	data, _, err := readTrustedFile(filepath.Join(overlayPath, inventoryFile), "command center inventory", inventoryLimit, 0)
	if err != nil {
		return Inventory{}, err
	}
	var inventory Inventory
	if err := decodeStrict(data, &inventory); err != nil {
		return Inventory{}, fmt.Errorf("%s: %w", inventoryFile, err)
	}
	if inventory.SchemaVersion != 1 {
		return Inventory{}, fmt.Errorf("%s: unsupported schema_version %d", inventoryFile, inventory.SchemaVersion)
	}
	seen := map[string]bool{}
	for _, cc := range inventory.CommandCenters {
		if err := validateDisplayName(cc.Name); err != nil {
			return Inventory{}, fmt.Errorf("%s: %w", inventoryFile, err)
		}
		if seen[cc.Name] {
			return Inventory{}, fmt.Errorf("%s: duplicate command center %q", inventoryFile, cc.Name)
		}
		seen[cc.Name] = true
		if cc.Platform != "darwin" && cc.Platform != "linux" {
			return Inventory{}, fmt.Errorf("%s: command center %q has unsupported platform", inventoryFile, cc.Name)
		}
		if !sshAliasPattern.MatchString(cc.SSH) {
			return Inventory{}, fmt.Errorf("%s: command center %q has an invalid ssh alias", inventoryFile, cc.Name)
		}
		if cc.User != "" && !sshUserPattern.MatchString(cc.User) {
			return Inventory{}, fmt.Errorf("%s: command center %q has an invalid ssh user", inventoryFile, cc.Name)
		}
	}
	return inventory, nil
}

// loadInventoryIfPresent returns an empty inventory when there is no overlay or no inventory file.
func loadInventoryIfPresent(enrollment Enrollment) (Inventory, error) {
	if enrollment.OverlayPath == "" {
		return Inventory{}, nil
	}
	inventory, err := LoadInventory(enrollment.OverlayPath)
	if errors.Is(err, os.ErrNotExist) {
		return Inventory{}, nil
	}
	if err != nil {
		return Inventory{}, Coded(CodeManifestInvalid, "fix "+inventoryFile+" in the overlay catalog", err)
	}
	return inventory, nil
}

func LocalSummary(buildVersion string) (MachineSummary, error) {
	paths, err := ResolvePaths()
	if err != nil {
		return MachineSummary{}, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return MachineSummary{}, err
	}
	status, err := Status("all")
	if err != nil {
		return MachineSummary{}, err
	}
	summary := MachineSummary{
		Name:          enrollment.DisplayName,
		Platform:      currentPlatform,
		Local:         true,
		Reachable:     true,
		TerranVersion: buildVersion,
		CatalogCommit: shortCommit(enrollment.RepositoryPath),
		OverlayCommit: shortCommit(enrollment.OverlayPath),
		Clean:         status.Clean,
		Healthy:       true,
	}
	summary.CatalogDirty, summary.CatalogAhead, summary.CatalogBehind = gitState(enrollment.RepositoryPath)
	summary.OverlayDirty, summary.OverlayAhead, summary.OverlayBehind = gitState(enrollment.OverlayPath)
	for _, check := range Doctor(buildVersion).Checks {
		switch {
		case check.Status != "fail":
		case strings.HasPrefix(check.Name, "tool:"):
			summary.ToolsMissing++
		default:
			summary.Healthy = false
		}
	}
	for _, item := range status.Items {
		switch item.Status {
		case "held":
			summary.Held++
		case "drift":
			summary.Drifted++
		case "collision":
			summary.Blocked++
		}
	}
	return summary, nil
}

// FleetStatus summarizes this machine and every other inventory entry, querying remotes concurrently.
func FleetStatus(buildVersion string, timeout time.Duration) ([]MachineSummary, error) {
	local, err := LocalSummary(buildVersion)
	if err != nil {
		return nil, err
	}
	paths, err := ResolvePaths()
	if err != nil {
		return nil, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return nil, err
	}
	inventory, err := loadInventoryIfPresent(enrollment)
	if err != nil {
		return nil, err
	}
	rows := make([]MachineSummary, len(inventory.CommandCenters))
	var wg sync.WaitGroup
	for i, cc := range inventory.CommandCenters {
		if cc.Name == local.Name {
			rows[i] = local
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			rows[i] = remoteSummary(ctx, cc)
		}()
	}
	wg.Wait()
	result := []MachineSummary{local}
	for _, row := range rows {
		if !row.Local {
			result = append(result, row)
		}
	}
	rest := result[1:]
	sort.Slice(rest, func(i, j int) bool { return naturalLess(rest[i].Name, rest[j].Name) })
	return result, nil
}

func remoteSummary(ctx context.Context, cc CommandCenter) MachineSummary {
	row := MachineSummary{Name: cc.Name, Platform: cc.Platform}
	out, err := runSSH(ctx, cc, "status", "--summary", "--json")
	if err != nil {
		row.Error = sshFailure(err)
		if envelope, ok := remoteError(err, out); ok {
			row.Error = envelope.Error.Code + ": " + envelope.Error.Message
		}
		return row
	}
	var remote MachineSummary
	if err := decodeStrict(out, &remote); err != nil {
		row.Error = err.Error()
		return row
	}
	negative := false
	for _, count := range []*int{remote.CatalogDirty, remote.CatalogAhead, remote.CatalogBehind, remote.OverlayDirty, remote.OverlayAhead, remote.OverlayBehind} {
		negative = negative || (count != nil && *count < 0)
	}
	if negative || !summaryVersionPattern.MatchString(remote.TerranVersion) || (remote.CatalogCommit != "" && !summaryCommitPattern.MatchString(remote.CatalogCommit)) || (remote.OverlayCommit != "" && !summaryCommitPattern.MatchString(remote.OverlayCommit)) || remote.Held < 0 || remote.Drifted < 0 || remote.Blocked < 0 || remote.ToolsMissing < 0 {
		row.Error = "incompatible terran"
		return row
	}
	row.Reachable = true
	row.TerranVersion, row.CatalogCommit, row.OverlayCommit = remote.TerranVersion, remote.CatalogCommit, remote.OverlayCommit
	row.Clean, row.Healthy = remote.Clean, remote.Healthy
	row.Held, row.Drifted, row.Blocked, row.ToolsMissing = remote.Held, remote.Drifted, remote.Blocked, remote.ToolsMissing
	row.CatalogDirty, row.CatalogAhead, row.CatalogBehind = remote.CatalogDirty, remote.CatalogAhead, remote.CatalogBehind
	row.OverlayDirty, row.OverlayAhead, row.OverlayBehind = remote.OverlayDirty, remote.OverlayAhead, remote.OverlayBehind
	return row
}

var errorCodePattern = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// remoteError decodes the JSON error envelope a remote terran writes to stdout
// when it exits 1. Control characters in the message become spaces.
func remoteError(err error, out []byte) (ErrorEnvelope, bool) {
	var exit *exec.ExitError
	var envelope ErrorEnvelope
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || decodeStrict(out, &envelope) != nil || envelope.SchemaVersion != SchemaVersion || !errorCodePattern.MatchString(envelope.Error.Code) {
		return ErrorEnvelope{}, false
	}
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, s)
	}
	envelope.Error.Message, envelope.Error.Next = clean(envelope.Error.Message), clean(envelope.Error.Next)
	return envelope, true
}

// sshFailure maps ssh exit codes: 255 is a connection failure, 127 a missing remote command.
func sshFailure(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 255:
			return "offline"
		case 127:
			return "terran not found"
		}
		return "incompatible terran"
	}
	if errors.Is(err, errSSHOutputTooLarge) {
		return "incompatible terran"
	}
	return "offline"
}

// RemoteStatus runs item-level status on the named inventory machine and returns its output
// strictly decoded and re-encoded: a StatusResult, or on exit 1 a StatusResult or error envelope.
// clean is false when the remote exited 1.
func RemoteStatus(name string, timeout time.Duration) (output []byte, clean bool, err error) {
	paths, err := ResolvePaths()
	if err != nil {
		return nil, false, err
	}
	enrollment, err := LoadEnrollment(paths)
	if err != nil {
		return nil, false, err
	}
	if name == enrollment.DisplayName {
		return nil, false, Coded(CodeUsage, "", fmt.Errorf("%s is this machine; use terran status --local", name))
	}
	inventory, err := loadInventoryIfPresent(enrollment)
	if err != nil {
		return nil, false, err
	}
	for _, cc := range inventory.CommandCenters {
		if cc.Name != name {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		out, err := runSSH(ctx, cc, "status", "--local", "--json")
		var exit *exec.ExitError
		nonClean := errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) > 0
		if err != nil && !nonClean {
			return nil, false, Coded(CodeUnreachable, "check ssh access and that terran is installed at ~/.local/bin/terran on "+name, fmt.Errorf("%s: %s", name, sshFailure(err)))
		}
		var decoded any
		var result StatusResult
		if decodeStrict(out, &result) == nil && result.SchemaVersion == SchemaVersion {
			decoded = result
		} else if envelope, ok := remoteError(err, out); ok {
			decoded = envelope
		} else {
			return nil, false, Coded(CodeOperational, "install the same terran version on "+name, fmt.Errorf("%s: incompatible terran", name))
		}
		data, err := json.Marshal(decoded)
		if err != nil {
			return nil, false, err
		}
		return append(data, '\n'), !nonClean, nil
	}
	return nil, false, Coded(CodeUsage, "", fmt.Errorf("unknown command center %q", name))
}

// naturalLess orders names so digit runs compare numerically (cc2 < cc10).
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da != "" && db != "" {
			ta, tb := strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")
			if len(ta) != len(tb) {
				return len(ta) < len(tb)
			}
			if ta != tb {
				return ta < tb
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digitPrefix(s string) string {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return s[:n]
}
