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
	interactive := terminalFile(os.Stdin) && terminalFile(os.Stderr)
	if len(args) == 0 {
		interactive = guidedTerminalStreams(os.Stdin, os.Stdout, os.Stderr, terminalFile)
	}
	os.Exit(runWithIO(args, os.Stdin, os.Stdout, os.Stderr, interactive))
}

func guidedTerminalStreams(stdin, stdout, stderr *os.File, isTerminal func(*os.File) bool) bool {
	return isTerminal(stdin) && isTerminal(stdout) && isTerminal(stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithIO(args, strings.NewReader(""), stdout, stderr, false)
}

func runWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool) int {
	if len(args) == 0 {
		if interactive {
			return runWizard(newPromptReader(stdin), stdout, stderr)
		}
		printHelp(stdout)
		return 0
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printHelp(stdout)
		return 0
	}
	if args[0] == "--version" {
		if len(args) != 1 {
			return usage(stderr, "--version takes no arguments")
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
		return usage(stderr, "unknown help topic")
	case "version":
		if commandHelpRequested(args[1:]) {
			printCommandHelp(stdout, "version")
			return 0
		}
		fs, options := newFlags("version", stderr)
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return flagError(stderr, err)
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
			return flagError(stderr, err)
		}
		enrollment, changed, err := terran.Enroll(options.repo, options.name, options.replace)
		if err != nil {
			if options.json {
				return jsonOperational(stdout, stderr, "enroll_failed", "enrollment failed", err)
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
		} else {
			fmt.Fprintf(stdout, "Already enrolled %s at %s.\n", enrollment.RepositoryID, enrollment.RepositoryPath)
		}
		return 0
	case "plan", "apply", "status":
		return runProjectionCommand(args[0], args[1:], stdin, stdout, stderr, interactive)
	case "doctor":
		if commandHelpRequested(args[1:]) {
			printCommandHelp(stdout, "doctor")
			return 0
		}
		fs, options := newFlags("doctor", stderr)
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return flagError(stderr, err)
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
		return usage(stderr, "unknown command "+args[0])
	}
}

func runProjectionCommand(command string, args []string, stdin io.Reader, stdout, stderr io.Writer, interactive bool) int {
	if commandHelpRequested(args) {
		printCommandHelp(stdout, command)
		return 0
	}
	fs, options := newFlags(command, stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return flagError(stderr, err)
	}
	if options.target != "all" && options.target != "agents" && options.target != "claude" && options.target != "opencode" {
		return usage(stderr, "target must be all, agents, claude, or opencode")
	}
	if command == "status" {
		result, err := terran.Status(options.target)
		if err != nil {
			if options.json {
				return jsonOperational(stdout, stderr, "status_failed", "status failed", err)
			}
			return operational(stderr, err)
		}
		if options.json {
			if err := writeJSON(stdout, result); err != nil {
				return operational(stderr, fmt.Errorf("write output: %w", err))
			}
		} else {
			for _, item := range result.Items {
				name := item.Skill
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
	var result terran.PlanResult
	var err error
	if command == "apply" {
		if interactive && !options.json {
			result, err = terran.ApplyWithOptions(options.target, version, terran.ApplyOptions{ResolveCollision: promptCollisionResolver(newPromptReader(stdin), stderr)})
		} else {
			result, err = terran.Apply(options.target, version)
		}
	} else {
		result, err = terran.Plan(options.target)
	}
	if err != nil {
		if options.json {
			return jsonOperational(stdout, stderr, command+"_failed", command+" failed", err)
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
				name = action.Target
			}
			fmt.Fprintf(stdout, "%-18s kind=%-11s target=%-16s name=%-24s source=%s destination=%s reason=%s\n", action.Action, action.Kind, action.Target, name, action.Source, action.Destination, reason)
		}
	}
	for _, action := range result.Actions {
		if action.Action == "blocked_collision" || action.Action == "blocked_drift" {
			return 3
		}
	}
	return 0
}

func promptCollisionResolver(reader *promptReader, stderr io.Writer) func(terran.Action) (terran.CollisionDecision, error) {
	return func(action terran.Action) (terran.CollisionDecision, error) {
		for {
			if _, err := fmt.Fprintf(stderr, "Existing %s %s differs. [r] Replace, [k] Keep, [q] Quit (default): ", action.Kind, action.Target); err != nil {
				return "", fmt.Errorf("write collision prompt: %w", err)
			}
			choice, ok, err := reader.read()
			if err != nil {
				return "", fmt.Errorf("read collision choice: %w", err)
			}
			if !ok {
				return terran.CollisionAbort, nil
			}
			switch strings.ToLower(strings.TrimSpace(choice)) {
			case "r", "replace":
				return terran.CollisionReplace, nil
			case "k", "keep":
				return terran.CollisionSkip, nil
			case "", "q", "quit":
				return terran.CollisionAbort, nil
			default:
				if _, err := fmt.Fprintln(stderr, "Please enter r, k, or q."); err != nil {
					return "", fmt.Errorf("write invalid collision choice: %w", err)
				}
			}
		}
	}
}

type commandOptions struct {
	repo    string
	name    string
	target  string
	replace bool
	json    bool
}

func newFlags(name string, output io.Writer) (*flag.FlagSet, *commandOptions) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	options := &commandOptions{target: "all"}
	switch name {
	case "enroll":
		fs.StringVar(&options.repo, "repo", "", "absolute path to the local catalog repository (required)")
		fs.StringVar(&options.name, "name", "", "Command Center display name (default: hostname)")
		fs.BoolVar(&options.replace, "replace", false, "replace a different existing enrollment")
	case "plan", "apply", "status":
		fs.StringVar(&options.target, "target", "all", "projection target: all, agents, claude, or opencode")
	}
	fs.BoolVar(&options.json, "json", false, "emit one JSON object instead of human output")
	fs.Usage = func() {
		printCommandIntro(output, name)
		fs.PrintDefaults()
	}
	return fs, options
}

func flagError(stderr io.Writer, err error) int {
	if err == nil {
		fmt.Fprintln(stderr, "invalid arguments")
	} else if !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stderr, err)
	}
	return 2
}

func commandHelpRequested(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
}

type errorOutput struct {
	SchemaVersion int `json:"schema_version"`
	Error         struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func jsonOperational(stdout, stderr io.Writer, code, message string, err error) int {
	result := errorOutput{SchemaVersion: terran.SchemaVersion}
	result.Error.Code = code
	result.Error.Message = message
	if writeErr := writeJSON(stdout, result); writeErr != nil {
		fmt.Fprintln(stderr, "terran: write JSON error:", writeErr)
	}
	fmt.Fprintln(stderr, "terran:", err)
	return 1
}

func operational(stderr io.Writer, err error) int { fmt.Fprintln(stderr, "terran:", err); return 1 }
func usage(stderr io.Writer, message string) int  { fmt.Fprintln(stderr, "terran:", message); return 2 }
func writeJSON(w io.Writer, value any) error      { return json.NewEncoder(w).Encode(value) }

func knownCommand(command string) bool {
	switch command {
	case "enroll", "plan", "apply", "status", "doctor", "version":
		return true
	}
	return false
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, `Terran manages local skills, global instructions, and fixed global configs on a Command Center.

Start here:
  terran                  Guide enrollment, review changes, and confirm apply
  terran doctor           Diagnose enrollment and managed state
  terran help             Show this help or help for one command

Advanced and automation:
  terran enroll           Record a trusted local catalog
  terran plan             Inspect proposed changes without mutation
  terran apply            Apply an explicitly selected, validated plan
  terran status           Inspect low-level managed state
  terran version          Print build metadata

Run terran help COMMAND for flags and exit codes. Advanced commands support
stable --json output where documented and never launch the guided workflow.`)
}

func printCommandHelp(w io.Writer, command string) {
	fs, _ := newFlags(command, w)
	fs.Usage()
}

func printCommandIntro(w io.Writer, command string) {
	lines := map[string]string{
		"version": "Usage: terran version [--json]\nRead-only. Prints build metadata. Exit: 0 success, 1 output failure, 2 usage.\n\nFlags:",
		"enroll":  "Usage: terran enroll --repo PATH [--name NAME] [--replace] [--json]\nMutates private enrollment state; it never creates skill links, instruction files, or config files. Exit: 0 success, 1 operational failure, 2 usage.\n\nFlags:",
		"plan":    "Usage: terran plan [--target all|claude|agents|opencode] [--json]\nRead-only. Reports every proposed source, destination, action, and reason. Exit: 0 unblocked, 1 operational failure, 2 usage, 3 blocked.\n\nFlags:",
		"apply":   "Usage: terran apply [--target all|claude|agents|opencode] [--json]\nMutates only validated skill leaves, fixed instruction/config files, and the receipt after an all-actions preflight. On a human terminal only, safe differing unowned instruction/config files may be replaced with a private backup, kept, or quit before mutation. Exit: 0 applied, 1 quit or operational failure, 2 usage, 3 blocked.\n\nFlags:",
		"status":  "Usage: terran status [--target all|claude|agents|opencode] [--json]\nRead-only. Exit: 0 clean, 1 non-clean or operational failure, 2 usage.\n\nFlags:",
		"doctor":  "Usage: terran doctor [--json]\nRead-only diagnostics. Exit: 0 healthy, 1 unhealthy or output failure, 2 usage.\n\nFlags:",
	}
	fmt.Fprintln(w, lines[command])
}
