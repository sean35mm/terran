package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sean35mm/terran/internal/terran"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

type versionInfo struct {
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
	Commit        string `json:"commit"`
	Date          string `json:"date"`
}

func main() {
	args := os.Args[1:]
	os.Exit(runWithIO(args, os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithIO(args, stdout, stderr)
}

func runWithIO(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runBareCommand(stdout, stderr)
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printHelp(stdout)
		return 0
	}
	if args[0] == "--version" {
		if len(args) != 1 {
			return usage(stdout, stderr, "--version takes no arguments", jsonRequested(args[1:]))
		}
		fmt.Fprintln(stdout, version)
		return 0
	}
	switch args[0] {
	case "help":
		if len(args) == 1 {
			printHelp(stdout)
			return 0
		}
		if len(args) == 2 && knownCommand(args[1]) {
			printCommandHelp(stdout, args[1])
			return 0
		}
		return usage(stdout, stderr, "unknown help topic", jsonRequested(args[1:]))
	case "version":
		if commandHelpRequested(args[1:]) {
			printCommandHelp(stdout, "version")
			return 0
		}
		fs, options := newFlags("version", stderr)
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return flagError(stdout, stderr, err, jsonRequested(args[1:]))
		}
		if options.json {
			if err := writeJSON(stdout, versionInfo{terran.SchemaVersion, version, commit, date}); err != nil {
				return operational(stderr, fmt.Errorf("write output: %w", err))
			}
		} else {
			fmt.Fprintln(stdout, version)
		}
		return 0
	case "enroll":
		if commandHelpRequested(args[1:]) {
			printCommandHelp(stdout, "enroll")
			return 0
		}
		fs, options := newFlags("enroll", stderr)
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || options.repo == "" {
			return flagError(stdout, stderr, err, jsonRequested(args[1:]))
		}
		enrollment, changed, err := terran.Enroll(options.repo, options.name, options.overlay, options.replace)
		if err != nil {
			if options.json {
				return jsonOperational(stdout, stderr, "enrollment failed", err)
			}
			return operational(stderr, err)
		}
		if options.json {
			if err := writeJSON(stdout, struct {
				SchemaVersion int               `json:"schema_version"`
				Changed       bool              `json:"changed"`
				Enrollment    terran.Enrollment `json:"enrollment"`
			}{terran.SchemaVersion, changed, enrollment}); err != nil {
				return operational(stderr, fmt.Errorf("write output: %w", err))
			}
		} else if changed {
			fmt.Fprintf(stdout, "Enrolled %s at %s as %s.\n", enrollment.RepositoryID, enrollment.RepositoryPath, enrollment.DisplayName)
			if enrollment.OverlayID != "" {
				fmt.Fprintf(stdout, "Overlay %s at %s.\n", enrollment.OverlayID, enrollment.OverlayPath)
			}
		} else {
			fmt.Fprintf(stdout, "Already enrolled %s at %s.\n", enrollment.RepositoryID, enrollment.RepositoryPath)
		}
		return 0
	case "status":
		return runStatusCommand(args[1:], stdout, stderr)
	case "plan", "apply":
		return runProjectionCommand(args[0], args[1:], stdout, stderr)
	case "capture":
		return runCaptureCommand(args[1:], stdout, stderr)
	case "hold", "unhold":
		return runHoldCommand(args[0], args[1:], stdout, stderr)
	case "doctor":
		if commandHelpRequested(args[1:]) {
			printCommandHelp(stdout, "doctor")
			return 0
		}
		fs, options := newFlags("doctor", stderr)
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return flagError(stdout, stderr, err, jsonRequested(args[1:]))
		}
		result := terran.Doctor(version)
		if options.json {
			if err := writeJSON(stdout, result); err != nil {
				return operational(stderr, fmt.Errorf("write output: %w", err))
			}
		} else {
			for _, check := range result.Checks {
				fmt.Fprintf(stdout, "%s %-24s %s\n", strings.ToUpper(check.Status), check.Name, check.Message)
			}
		}
		if !result.Healthy {
			return 1
		}
		return 0
	default:
		return usage(stdout, stderr, "unknown command "+args[0], jsonRequested(args[1:]))
	}
}

func runProjectionCommand(command string, args []string, stdout, stderr io.Writer) int {
	if commandHelpRequested(args) {
		printCommandHelp(stdout, command)
		return 0
	}
	fs, options := newFlags(command, stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return flagError(stdout, stderr, err, jsonRequested(args))
	}
	if err := terran.ValidateTarget(options.target); err != nil {
		return usage(stdout, stderr, err.Error(), options.json)
	}
	var result terran.PlanResult
	var err error
	if command == "apply" {
		result, err = terran.ApplyWithOptions(options.target, version, terran.ApplyOptions{Decisions: options.decisions, ExpectDigest: options.expect})
	} else {
		result, err = terran.Plan(options.target)
	}
	if err != nil {
		if code, _ := terran.ErrorCode(err); code == terran.CodeUsage {
			return usage(stdout, stderr, err.Error(), options.json)
		}
		if options.json {
			return jsonOperational(stdout, stderr, command+" failed", err)
		}
		return operational(stderr, err)
	}
	if options.json {
		if err := writeJSON(stdout, result); err != nil {
			return operational(stderr, fmt.Errorf("write output: %w", err))
		}
	} else {
		for _, action := range result.Actions {
			reason := action.Reason
			if reason == "" {
				reason = "-"
			}
			name := action.Skill
			if name == "" {
				name = action.Name
			}
			if name == "" {
				name = action.Target
			}
			fmt.Fprintf(stdout, "%-18s kind=%-11s target=%-16s name=%-24s source=%s destination=%s reason=%s\n", action.Action, action.Kind, action.Target, name, action.Source, action.Destination, reason)
		}
		if command == "plan" {
			fmt.Fprintf(stdout, "digest=%s\n", result.Digest)
		}
	}
	for _, action := range result.Actions {
		if action.Action == "blocked_collision" || action.Action == "blocked_drift" {
			return 3
		}
	}
	return 0
}

func runLocalStatus(options *commandOptions, stdout, stderr io.Writer) int {
	result, err := terran.Status(options.target)
	if err != nil {
		return statusFailed(options.json, stdout, stderr, err)
	}
	if options.json {
		if err := writeJSON(stdout, result); err != nil {
			return operational(stderr, fmt.Errorf("write output: %w", err))
		}
	} else {
		for _, item := range result.Items {
			name := item.Skill
			if name == "" {
				name = item.Name
			}
			if name == "" {
				name = item.Target
			}
			fmt.Fprintf(stdout, "%-10s %-11s %-16s %s\n", item.Status, item.Kind, item.Target, name)
		}
	}
	if !result.Clean {
		return 1
	}
	return 0
}

// runBareCommand shows the fleet table, or help on a machine that is not enrolled yet.
func runBareCommand(stdout, stderr io.Writer) int {
	paths, err := terran.ResolvePaths()
	if err == nil {
		if missing, err := terran.EnrollmentMissing(paths); err == nil && missing {
			printHelp(stdout)
			return 0
		}
	}
	return runFleetStatus(false, stdout, stderr)
}

func runCaptureCommand(args []string, stdout, stderr io.Writer) int {
	if commandHelpRequested(args) {
		printCommandHelp(stdout, "capture")
		return 0
	}
	fs, options := newFlags("capture", stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return flagError(stdout, stderr, err, jsonRequested(args))
	}
	if err := terran.ValidateTarget(options.target); err != nil {
		return usage(stdout, stderr, err.Error(), options.json)
	}
	result, err := terran.Capture(options.target)
	if err != nil {
		if options.json {
			return jsonOperational(stdout, stderr, "capture failed", err)
		}
		return operational(stderr, err)
	}
	if options.json {
		if err := writeJSON(stdout, result); err != nil {
			return operational(stderr, fmt.Errorf("write output: %w", err))
		}
	} else {
		for _, item := range result.Items {
			fmt.Fprintf(stdout, "%s  %s\n", item.ID, item.Kind)
		}
	}
	return 0
}

func runHoldCommand(command string, args []string, stdout, stderr io.Writer) int {
	if commandHelpRequested(args) {
		printCommandHelp(stdout, command)
		return 0
	}
	fs, options := newFlags(command, stderr)
	// The flag package stops at the first positional argument, so parse again after taking the id.
	var id string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return flagError(stdout, stderr, err, jsonRequested(args))
		}
		if fs.NArg() == 0 {
			break
		}
		if id != "" {
			return usage(stdout, stderr, "expected exactly one item id", options.json)
		}
		id, rest = fs.Arg(0), fs.Args()[1:]
	}
	if id == "" {
		return usage(stdout, stderr, "expected exactly one item id", options.json)
	}
	change := terran.Hold
	if command == "unhold" {
		change = terran.Unhold
	}
	enrollment, err := change(id)
	if err != nil {
		if options.json {
			return jsonOperational(stdout, stderr, command+" failed", err)
		}
		return operational(stderr, err)
	}
	if options.json {
		holds := enrollment.Holds
		if holds == nil {
			holds = []string{}
		}
		if err := writeJSON(stdout, struct {
			SchemaVersion int      `json:"schema_version"`
			Holds         []string `json:"holds"`
		}{terran.SchemaVersion, holds}); err != nil {
			return operational(stderr, fmt.Errorf("write output: %w", err))
		}
	} else {
		fmt.Fprintf(stdout, "%s %s. Held items: %d.\n", map[string]string{"hold": "Held", "unhold": "Released"}[command], id, len(enrollment.Holds))
	}
	return 0
}

type commandOptions struct {
	repo    string
	name    string
	overlay string
	target  string
	replace bool
	json    bool
	local   bool
	summary bool

	decisions map[string]terran.CollisionDecision
	expect    string
}

func newFlags(name string, output io.Writer) (*flag.FlagSet, *commandOptions) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	options := &commandOptions{target: "all"}
	switch name {
	case "enroll":
		fs.StringVar(&options.repo, "repo", "", "absolute path to the local catalog repository (required)")
		fs.StringVar(&options.name, "name", "", "Command Center display name (default: hostname; kept when re-enrolling)")
		fs.StringVar(&options.overlay, "overlay", "", "absolute path to a private overlay catalog that only adds items (default: keep the enrolled overlay)")
		fs.BoolVar(&options.replace, "replace", false, "replace a different existing enrollment; with the same repository, drop an overlay not given by --overlay")
	case "plan", "apply", "status", "capture":
		fs.StringVar(&options.target, "target", "all", "projection target: all, "+strings.Join(terran.TargetGroups(), ", "))
	}
	if name == "status" {
		fs.BoolVar(&options.local, "local", false, "show item-level status for this machine only (supports --target)")
		fs.BoolVar(&options.summary, "summary", false, "emit this machine's one-line fleet summary as JSON (used over ssh)")
	}
	if name == "apply" {
		fs.Func("decide", "resolve a blocked_collision item: ITEM_ID=replace|keep (repeatable); replace backs up the existing value and installs the catalog version, keep holds the item", func(value string) error {
			id, decision, ok := strings.Cut(value, "=")
			if !ok || id == "" || (decision != string(terran.CollisionReplace) && decision != string(terran.CollisionKeep)) {
				return fmt.Errorf("want ITEM_ID=replace|keep")
			}
			if _, duplicate := options.decisions[id]; duplicate {
				return fmt.Errorf("duplicate decision for %s", id)
			}
			if options.decisions == nil {
				options.decisions = map[string]terran.CollisionDecision{}
			}
			options.decisions[id] = terran.CollisionDecision(decision)
			return nil
		})
		fs.StringVar(&options.expect, "expect", "", "apply only if the plan digest equals DIGEST from terran plan")
	}
	fs.BoolVar(&options.json, "json", false, "emit one JSON object instead of human output")
	fs.Usage = func() {
		printCommandIntro(output, name)
		fs.PrintDefaults()
	}
	return fs, options
}

func flagError(stdout, stderr io.Writer, err error, jsonOutput bool) int {
	if errors.Is(err, flag.ErrHelp) {
		return 2
	}
	message := "invalid arguments"
	if err != nil {
		message = err.Error()
	}
	return usage(stdout, stderr, message, jsonOutput)
}

func commandHelpRequested(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
}

// jsonOperational reports err as a JSON error whose message is the context
// followed by the full error text.
func jsonOperational(stdout, stderr io.Writer, context string, err error) int {
	writeJSONError(stdout, stderr, context+": "+err.Error(), err)
	return 1
}

func writeJSONError(stdout, stderr io.Writer, message string, err error) {
	code, next := terran.ErrorCode(err)
	result := terran.ErrorEnvelope{SchemaVersion: terran.SchemaVersion}
	result.Error.Code = code
	result.Error.Message = message
	result.Error.Next = next
	if writeErr := writeJSON(stdout, result); writeErr != nil {
		fmt.Fprintln(stderr, "terran: write JSON error:", writeErr)
	}
	printError(stderr, err)
}

func operational(stderr io.Writer, err error) int { printError(stderr, err); return 1 }
func usage(stdout, stderr io.Writer, message string, jsonOutput bool) int {
	err := terran.Coded(terran.CodeUsage, "", errors.New(message))
	if jsonOutput {
		writeJSONError(stdout, stderr, message, err)
		return 2
	}
	printError(stderr, err)
	return 2
}
func writeJSON(w io.Writer, value any) error { return json.NewEncoder(w).Encode(value) }

func jsonRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--json" {
			return true
		}
	}
	return false
}

func printError(stderr io.Writer, err error) {
	fmt.Fprintln(stderr, "terran:", err)
	if _, next := terran.ErrorCode(err); next != "" {
		fmt.Fprintln(stderr, "next:", next)
	}
}

func knownCommand(command string) bool {
	switch command {
	case "enroll", "plan", "apply", "status", "capture", "hold", "unhold", "doctor", "version":
		return true
	}
	return false
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `Terran manages local skills, global instructions, and fixed global configs on a Command Center.

Start here:
  terran                  Show the fleet table (help when not enrolled)
  terran --help           Show this help
  terran doctor           Diagnose enrollment and managed state
  terran help             Show this help or help for one command

Advanced and automation:
  terran enroll           Record a trusted local catalog
  terran plan             Inspect proposed changes without mutation
  terran apply            Apply an explicitly selected, validated plan
  terran status           Fleet table; --local for this machine's item-level state
  terran capture          List unmanaged agent setup on this machine
  terran hold             Pin one item on this machine so apply leaves it alone
  terran unhold           Release a held item
  terran version          Print build metadata

Run terran help COMMAND for flags and exit codes. Commands support stable
--json output where documented.`)
}

func printCommandHelp(w io.Writer, command string) {
	fs, _ := newFlags(command, w)
	fs.Usage()
}

func printCommandIntro(w io.Writer, command string) {
	targets := "all|" + strings.Join(terran.TargetGroups(), "|")
	lines := map[string]string{
		"version": "Usage: terran version [--json]\nRead-only. Prints build metadata. Exit: 0 success, 1 output failure, 2 usage.\n\nFlags:",
		"enroll":  "Usage: terran enroll --repo PATH [--name NAME] [--overlay PATH] [--replace] [--json]\nMutates private enrollment state; it never creates skill copies, instruction files, or config files. Re-enrolling the same repository may rename it or add an overlay and keeps holds. Changing or dropping an overlay that still owns applied items fails with repository_mismatch. Exit: 0 success, 1 operational failure, 2 usage.\n\nFlags:",
		"plan":    "Usage: terran plan [--target " + targets + "] [--json]\nRead-only. Reports every proposed source, destination, action, and reason. Exit: 0 unblocked, 1 operational failure, 2 usage, 3 blocked.\n\nFlags:",
		"apply":   "Usage: terran apply [--target " + targets + "] [--decide ITEM_ID=replace|keep]... [--expect DIGEST] [--json]\nUndecided collisions block (exit 3). A --decide for an item that is not a blocked_collision is a usage error and nothing is changed; a stale --expect fails with plan_changed. Mutates only validated skill leaves, fixed instruction/config files, named files in fixed directories, owned top-level keys in fixed JSON settings files, and the receipt after an all-actions preflight. Exit: 0 applied, 1 operational failure, 2 usage, 3 blocked.\n\nFlags:",
		"status":  "Usage: terran status [--json]\n       terran status NAME [--json]\n       terran status --local [--target " + targets + "] [--json]\n       terran status --summary --json\nRead-only. Without arguments, prints one row per Command Center in the overlay's command-centers.json (this machine plus each ssh alias, queried as ~/.local/bin/terran); unreachable machines are rows, not errors. NAME prints that machine's item-level status over ssh as the remote returned it (unreachable failures use error code unreachable). --local prints this machine's item-level status. Exit: fleet table and --summary 0 (1 operational failure), NAME and --local 0 clean, 1 non-clean or operational failure, 2 usage.\n\nFlags:",
		"capture": "Usage: terran capture [--target " + targets + "] [--json]\nRead-only. Lists entries in skill and file directories, whole-file targets, and top-level settings keys that Terran does not own, as ITEM_ID KIND lines (values are never printed). Skips hidden, naru- prefixed, held, and already owned entries. Exit: 0 success, 1 operational failure (including not_enrolled), 2 usage.\n\nFlags:",
		"hold":    "Usage: terran hold ITEM_ID [--json]\nMutates private enrollment state only. Pins an item id from terran plan --json so plan and apply never inspect or change it. Exit: 0 success, 1 operational failure (including unknown_item), 2 usage.\n\nFlags:",
		"unhold":  "Usage: terran unhold ITEM_ID [--json]\nMutates private enrollment state only. Releases a held item; releasing an item that is not held succeeds. Exit: 0 success, 1 operational failure, 2 usage.\n\nFlags:",
		"doctor":  "Usage: terran doctor [--json]\nRead-only diagnostics. Exit: 0 healthy, 1 unhealthy or output failure, 2 usage.\n\nFlags:",
	}
	fmt.Fprintln(w, lines[command])
}
