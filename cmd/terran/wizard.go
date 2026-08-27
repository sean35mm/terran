package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sean35mm/terran/internal/terran"
)

type promptReader struct {
	scanner *bufio.Scanner
}

const promptInputLimit = 8 << 10

func newPromptReader(input io.Reader) *promptReader {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 256), promptInputLimit)
	return &promptReader{scanner: scanner}
}

func (r *promptReader) read() (string, bool, error) {
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	return r.scanner.Text(), true, nil
}

func runWizard(reader *promptReader, stdout, stderr io.Writer) int {
	paths, err := terran.ResolvePaths()
	if err != nil {
		return operational(stderr, err)
	}
	enrollment, err := terran.LoadEnrollment(paths)
	if errors.Is(err, os.ErrNotExist) {
		missing, missingErr := terran.EnrollmentMissing(paths)
		if missingErr != nil || !missing {
			if missingErr == nil {
				missingErr = err
			}
			return operational(stderr, fmt.Errorf("load enrollment: %w; run terran doctor", missingErr))
		}
		enrollment, err = wizardEnroll(reader, stdout, stderr, paths)
		if errors.Is(err, terran.ErrApplyAborted) {
			return 1
		}
		if err != nil {
			return operational(stderr, err)
		}
	} else if err != nil {
		return operational(stderr, fmt.Errorf("load enrollment: %w; run terran doctor", err))
	}

	plan, err := terran.Plan("all")
	if err != nil {
		return operational(stderr, err)
	}
	if plan.Clean {
		if err := writeCleanSummary(stdout, enrollment, plan); err != nil {
			return operational(stderr, err)
		}
		return 0
	}

	blocked, err := hasIneligibleBlock(plan)
	if err != nil {
		return operational(stderr, err)
	}
	if blocked {
		if err := writeBlockedSummary(stdout, plan); err != nil {
			return operational(stderr, err)
		}
		return 3
	}
	if err := writePlanSummary(stdout, "Changes are ready for review:", plan); err != nil {
		return operational(stderr, err)
	}
	for {
		if _, err := fmt.Fprint(stderr, "[d] Details, [a] Apply, [q] Quit (default): "); err != nil {
			return operational(stderr, fmt.Errorf("write guided choice prompt: %w", err))
		}
		choice, ok, err := reader.read()
		if err != nil {
			return operational(stderr, fmt.Errorf("read guided choice: %w", err))
		}
		if !ok {
			return 1
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "q", "quit":
			return 1
		case "d", "details":
			if err := writePlanDetails(stdout, plan); err != nil {
				return operational(stderr, err)
			}
		case "a", "apply":
			return wizardApply(reader, stdout, stderr)
		default:
			if _, err := fmt.Fprintln(stderr, "Please enter d, a, or q."); err != nil {
				return operational(stderr, fmt.Errorf("write guided choice help: %w", err))
			}
		}
	}
}

func wizardEnroll(reader *promptReader, stdout, stderr io.Writer, paths terran.Paths) (terran.Enrollment, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return terran.Enrollment{}, fmt.Errorf("read current directory: %w", err)
	}
	defaultRepo := ""
	if info, statErr := os.Stat(filepath.Join(cwd, "terran.json")); statErr == nil && !info.IsDir() {
		defaultRepo = cwd
	}
	prompt := "Catalog path: "
	if defaultRepo != "" {
		prompt = fmt.Sprintf("Catalog path [%s]: ", defaultRepo)
	}
	if _, err := fmt.Fprint(stderr, prompt); err != nil {
		return terran.Enrollment{}, fmt.Errorf("write catalog prompt: %w", err)
	}
	answer, ok, err := reader.read()
	if err != nil {
		return terran.Enrollment{}, fmt.Errorf("read catalog path: %w", err)
	}
	if !ok {
		return terran.Enrollment{}, terran.ErrApplyAborted
	}
	repo := strings.TrimSpace(answer)
	if repo == "" {
		repo = defaultRepo
	}
	if repo == "" {
		return terran.Enrollment{}, terran.ErrApplyAborted
	}
	repo, err = expandCatalogPath(repo, paths.Home, cwd)
	if err != nil {
		return terran.Enrollment{}, err
	}
	loaded, err := terran.LoadManifest(repo)
	if err != nil {
		return terran.Enrollment{}, fmt.Errorf("catalog is not valid: %w", err)
	}
	skills := 0
	for _, projection := range loaded.Manifest.Projections {
		skills += len(projection.Targets)
	}
	if _, err := fmt.Fprintf(stdout, "Catalog %s %s: %d skill projections, %d global instructions, %d global configurations.\n  Repository: %s\n", loaded.Manifest.ID, loaded.Manifest.Version, skills, len(loaded.Manifest.Instructions), len(loaded.Manifest.Configs), loaded.Repository); err != nil {
		return terran.Enrollment{}, fmt.Errorf("write catalog summary: %w", err)
	}
	trusted, err := promptYesNo(reader, stderr, "Trust and enroll this local catalog? [y/N]: ")
	if err != nil {
		return terran.Enrollment{}, err
	}
	if !trusted {
		return terran.Enrollment{}, terran.ErrApplyAborted
	}
	enrollment, _, err := terran.Enroll(loaded.Repository, "", false)
	if err != nil {
		return terran.Enrollment{}, err
	}
	if _, err := fmt.Fprintf(stdout, "Enrolled %s as %s. Nothing has been projected yet.\n", enrollment.RepositoryID, enrollment.DisplayName); err != nil {
		return terran.Enrollment{}, fmt.Errorf("write enrollment summary: %w", err)
	}
	return enrollment, nil
}

func expandCatalogPath(path, home, cwd string) (string, error) {
	switch {
	case path == "~":
		path = home
	case strings.HasPrefix(path, "~"+string(filepath.Separator)):
		path = filepath.Join(home, strings.TrimPrefix(path, "~"+string(filepath.Separator)))
	case strings.HasPrefix(path, "~"):
		return "", fmt.Errorf("catalog path supports only ~ or ~/...")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve catalog path: %w", err)
	}
	return filepath.Clean(abs), nil
}

func promptYesNo(reader *promptReader, output io.Writer, prompt string) (bool, error) {
	for {
		if _, err := fmt.Fprint(output, prompt); err != nil {
			return false, fmt.Errorf("write confirmation prompt: %w", err)
		}
		answer, ok, err := reader.read()
		if err != nil {
			return false, fmt.Errorf("read confirmation: %w", err)
		}
		if !ok {
			return false, nil
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		default:
			if _, err := fmt.Fprintln(output, "Please enter y or n."); err != nil {
				return false, fmt.Errorf("write confirmation help: %w", err)
			}
		}
	}
}

func hasIneligibleBlock(plan terran.PlanResult) (bool, error) {
	for _, action := range plan.Actions {
		if action.Action == "blocked_drift" {
			return true, nil
		}
		if action.Action != "blocked_collision" {
			continue
		}
		eligible, err := terran.CanResolveCollision(action)
		if err != nil {
			return false, err
		}
		if !eligible {
			return true, nil
		}
	}
	return false, nil
}

func wizardApply(reader *promptReader, stdout, stderr io.Writer) int {
	options := terran.ApplyOptions{
		ResolveCollision: promptCollisionResolver(reader, stderr),
		ConfirmPlan: func(plan terran.PlanResult) error {
			if err := writePlanSummary(stdout, "Final validated plan (locked):", plan); err != nil {
				return err
			}
			if _, err := fmt.Fprintln(stdout, "Exact locked actions:"); err != nil {
				return fmt.Errorf("write locked plan heading: %w", err)
			}
			if err := writePlanDetails(stdout, plan); err != nil {
				return err
			}
			confirmed, err := promptYesNo(reader, stderr, "Apply this exact plan? [y] Apply, [N] Quit: ")
			if err != nil {
				return err
			}
			if !confirmed {
				return terran.ErrApplyAborted
			}
			return nil
		},
	}
	result, err := terran.ApplyWithOptions("all", version, options)
	if err != nil {
		if errors.Is(err, terran.ErrApplyAborted) {
			return 1
		}
		return operational(stderr, err)
	}
	if planBlocked(result) {
		if err := writeBlockedSummary(stdout, result); err != nil {
			return operational(stderr, err)
		}
		return 3
	}
	status, err := terran.Status("all")
	if err != nil {
		return operational(stderr, fmt.Errorf("verify applied state: %w", err))
	}
	if status.Clean {
		if _, err := fmt.Fprintln(stdout, "Applied and verified. Terran is up to date."); err != nil {
			return operational(stderr, fmt.Errorf("write verification summary: %w", err))
		}
		return 0
	}
	if onlySkippedUnmanaged(result, status) {
		if _, err := fmt.Fprintln(stdout, "Applied and verified the selected changes. Kept items remain unmanaged."); err != nil {
			return operational(stderr, fmt.Errorf("write partial verification summary: %w", err))
		}
		return 0
	}
	if _, err := fmt.Fprintln(stderr, "terran: post-apply verification found state that is not clean; run terran doctor"); err != nil {
		return 1
	}
	return 1
}

func writeCleanSummary(w io.Writer, enrollment terran.Enrollment, plan terran.PlanResult) error {
	skills, instructions, configs := planCounts(plan, false)
	_, err := fmt.Fprintf(w, "%s is up to date: %d skills, %d global instructions, %d global configurations.\n", enrollment.DisplayName, skills, instructions, configs)
	if err != nil {
		return fmt.Errorf("write clean summary: %w", err)
	}
	return nil
}

func writePlanSummary(w io.Writer, heading string, plan terran.PlanResult) error {
	skills, instructions, configs := planCounts(plan, true)
	if _, err := fmt.Fprintf(w, "%s\n  Skills: %d\n", heading, skills); err != nil {
		return fmt.Errorf("write plan summary: %w", err)
	}
	if err := writeFriendlyActions(w, plan, "skill"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Global instructions: %d\n", instructions); err != nil {
		return fmt.Errorf("write plan summary: %w", err)
	}
	if err := writeFriendlyActions(w, plan, "instruction"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Global configuration: %d\n", configs); err != nil {
		return fmt.Errorf("write plan summary: %w", err)
	}
	if err := writeFriendlyActions(w, plan, "config"); err != nil {
		return err
	}
	return nil
}

func writeFriendlyActions(w io.Writer, plan terran.PlanResult, kind string) error {
	for _, action := range plan.Actions {
		if action.Kind != kind || action.Action == "noop" {
			continue
		}
		name := friendlyActionName(action)
		verb := strings.ReplaceAll(action.Action, "_", " ")
		if action.Action == "blocked_collision" {
			verb = "needs a choice"
		} else if action.Action == "skip" {
			verb = "keep unmanaged"
		}
		if _, err := fmt.Fprintf(w, "    - %s: %s\n", name, verb); err != nil {
			return fmt.Errorf("write plan summary: %w", err)
		}
	}
	return nil
}

func friendlyActionName(action terran.Action) string {
	if action.Kind == "skill" {
		target := action.Target
		switch action.Target {
		case "agents":
			target = "shared agents"
		case "claude":
			target = "Claude"
		}
		return fmt.Sprintf("%s for %s", action.Skill, target)
	}
	switch action.Target {
	case "claude-global":
		return "Claude instructions"
	case "opencode-global":
		return "OpenCode instructions"
	case "opencode-config":
		return "OpenCode configuration"
	default:
		return action.Target
	}
}

func planCounts(plan terran.PlanResult, changesOnly bool) (skills, instructions, configs int) {
	for _, action := range plan.Actions {
		if changesOnly && action.Action == "noop" {
			continue
		}
		switch action.Kind {
		case "skill":
			skills++
		case "instruction":
			instructions++
		case "config":
			configs++
		}
	}
	return
}

func writePlanDetails(w io.Writer, plan terran.PlanResult) error {
	for _, action := range plan.Actions {
		if action.Action == "noop" {
			continue
		}
		name := action.Skill
		if name == "" {
			name = action.Target
		}
		reason := action.Reason
		if reason == "" {
			reason = "-"
		}
		if _, err := fmt.Fprintf(w, "%s %s (%s)\n  source: %s\n  destination: %s\n  reason: %s\n", action.Action, name, action.Kind, action.Source, action.Destination, reason); err != nil {
			return fmt.Errorf("write plan details: %w", err)
		}
	}
	return nil
}

func writeBlockedSummary(w io.Writer, plan terran.PlanResult) error {
	if _, err := fmt.Fprintln(w, "Terran is blocked and made no changes:"); err != nil {
		return fmt.Errorf("write blocked summary: %w", err)
	}
	for _, action := range plan.Actions {
		if action.Action != "blocked_collision" && action.Action != "blocked_drift" {
			continue
		}
		name := action.Skill
		if name == "" {
			name = action.Target
		}
		detail := "an existing item cannot be safely managed"
		if action.Action == "blocked_drift" {
			detail = "a Terran-managed item changed or became unsafe"
		}
		if _, err := fmt.Fprintf(w, "  %s (%s): %s\n", name, action.Kind, detail); err != nil {
			return fmt.Errorf("write blocked summary: %w", err)
		}
	}
	if _, err := fmt.Fprintln(w, "Run terran doctor, or use advanced terran plan for full paths and reasons."); err != nil {
		return fmt.Errorf("write blocked guidance: %w", err)
	}
	return nil
}

func planBlocked(plan terran.PlanResult) bool {
	for _, action := range plan.Actions {
		if action.Action == "blocked_collision" || action.Action == "blocked_drift" {
			return true
		}
	}
	return false
}

func onlySkippedUnmanaged(plan terran.PlanResult, status terran.StatusResult) bool {
	skipped := make(map[string]bool)
	for _, action := range plan.Actions {
		if action.Action == "skip" {
			skipped[action.Kind+"\x00"+action.Target] = true
		}
	}
	if len(skipped) == 0 {
		return false
	}
	for _, item := range status.Items {
		if item.Status == "ok" {
			continue
		}
		if item.Status != "collision" || !skipped[item.Kind+"\x00"+item.Target] {
			return false
		}
	}
	return true
}
