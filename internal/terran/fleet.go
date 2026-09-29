package terran

import (
	"bytes"
	"context"
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
)

const (
	inventoryFile  = "command-centers.json"
	inventoryLimit = 1 << 20
	sshOutputLimit = 1 << 20
)

var sshAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
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
	Error         string `json:"error,omitempty"`
}

// runSSH runs the remote terran with a fixed argument vector; nothing goes through a shell.
// It is a variable so tests can stub it.
var runSSH = func(ctx context.Context, alias string, args ...string) ([]byte, error) {
	if !sshAliasPattern.MatchString(alias) {
		return nil, fmt.Errorf("invalid ssh alias")
	}
	cmd := exec.CommandContext(ctx, "ssh", append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", alias, ".local/bin/terran"}, args...)...)
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
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
		Healthy:       Doctor(buildVersion).Healthy,
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
	out, err := runSSH(ctx, cc.SSH, "status", "--summary", "--json")
	if err != nil {
		row.Error = sshFailure(err)
		return row
	}
	var remote MachineSummary
	if err := decodeStrict(out, &remote); err != nil {
		row.Error = err.Error()
		return row
	}
	if !summaryVersionPattern.MatchString(remote.TerranVersion) || (remote.CatalogCommit != "" && !summaryCommitPattern.MatchString(remote.CatalogCommit)) || (remote.OverlayCommit != "" && !summaryCommitPattern.MatchString(remote.OverlayCommit)) || remote.Held < 0 || remote.Drifted < 0 || remote.Blocked < 0 {
		row.Error = "incompatible terran"
		return row
	}
	row.Reachable = true
	row.TerranVersion, row.CatalogCommit, row.OverlayCommit = remote.TerranVersion, remote.CatalogCommit, remote.OverlayCommit
	row.Clean, row.Healthy = remote.Clean, remote.Healthy
	row.Held, row.Drifted, row.Blocked = remote.Held, remote.Drifted, remote.Blocked
	return row
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

// RemoteStatus runs item-level status on the named inventory machine and returns its output as written.
// clean is false when the remote reported non-clean state (exit 1 with output).
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
		out, err := runSSH(ctx, cc.SSH, "status", "--local", "--json")
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) > 0 {
			return out, false, nil
		}
		if err != nil {
			return nil, false, Coded(CodeUnreachable, "check ssh access and that terran is installed at ~/.local/bin/terran on "+name, fmt.Errorf("%s: %s", name, sshFailure(err)))
		}
		return out, true, nil
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
