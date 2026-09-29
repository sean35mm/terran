package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/sean35mm/terran/internal/terran"
)

const fleetTimeout = 10 * time.Second

func runStatusCommand(args []string, stdout, stderr io.Writer) int {
	if commandHelpRequested(args) {
		printCommandHelp(stdout, "status")
		return 0
	}
	fs, options := newFlags("status", stderr)
	// The flag package stops at the first positional argument, so parse again after taking the name.
	var name string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return flagError(stdout, stderr, err, jsonRequested(args))
		}
		if fs.NArg() == 0 {
			break
		}
		if name != "" {
			return usage(stdout, stderr, "expected at most one command center name", options.json)
		}
		name, rest = fs.Arg(0), fs.Args()[1:]
	}
	if err := terran.ValidateTarget(options.target); err != nil {
		return usage(stdout, stderr, err.Error(), options.json)
	}
	switch {
	case options.local && (options.summary || name != ""):
		return usage(stdout, stderr, "--local cannot be combined with --summary or a command center name", options.json)
	case options.summary && name != "":
		return usage(stdout, stderr, "--summary cannot be combined with a command center name", options.json)
	case !options.local && options.target != "all":
		return usage(stdout, stderr, "--target requires --local", options.json)
	}
	switch {
	case options.local:
		return runLocalStatus(options, stdout, stderr)
	case options.summary:
		summary, err := terran.LocalSummary(version)
		if err != nil {
			return statusFailed(options.json, stdout, stderr, err)
		}
		if err := writeJSON(stdout, summary); err != nil {
			return operational(stderr, fmt.Errorf("write output: %w", err))
		}
		return 0
	case name != "":
		output, clean, err := terran.RemoteStatus(name, fleetTimeout)
		if err != nil {
			if code, _ := terran.ErrorCode(err); code == terran.CodeUsage {
				return usage(stdout, stderr, err.Error(), options.json)
			}
			return statusFailed(options.json, stdout, stderr, err)
		}
		if _, err := stdout.Write(output); err != nil {
			return operational(stderr, fmt.Errorf("write output: %w", err))
		}
		if !clean {
			return 1
		}
		return 0
	}
	return runFleetStatus(options.json, stdout, stderr)
}

func runFleetStatus(jsonOutput bool, stdout, stderr io.Writer) int {
	rows, err := terran.FleetStatus(version, fleetTimeout)
	if err != nil {
		return statusFailed(jsonOutput, stdout, stderr, err)
	}
	if jsonOutput {
		if err := writeJSON(stdout, struct {
			SchemaVersion  int                     `json:"schema_version"`
			CommandCenters []terran.MachineSummary `json:"command_centers"`
		}{terran.SchemaVersion, rows}); err != nil {
			return operational(stderr, fmt.Errorf("write output: %w", err))
		}
		return 0
	}
	if err := writeFleetTable(stdout, rows); err != nil {
		return operational(stderr, fmt.Errorf("write output: %w", err))
	}
	return 0
}

func statusFailed(jsonOutput bool, stdout, stderr io.Writer, err error) int {
	if jsonOutput {
		return jsonOperational(stdout, stderr, "status failed", err)
	}
	return operational(stderr, err)
}

func writeFleetTable(w io.Writer, rows []terran.MachineSummary) error {
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "CC\tPLATFORM\tTERRAN\tCATALOG\tOVERLAY\tSTATE")
	for _, row := range rows {
		name := row.Name
		if row.Local {
			name += "*"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", name, row.Platform, dash(row.TerranVersion), dash(row.CatalogCommit), dash(row.OverlayCommit), machineState(row))
	}
	return table.Flush()
}

func machineState(row terran.MachineSummary) string {
	if !row.Reachable {
		if row.Error == "" {
			return "offline"
		}
		return row.Error
	}
	state := "clean"
	switch {
	case row.Blocked > 0:
		state = fmt.Sprintf("blocked: %d", row.Blocked)
	case row.Drifted > 0:
		state = fmt.Sprintf("drift: %d", row.Drifted)
	case !row.Clean || !row.Healthy:
		state = "unhealthy"
	case row.Held > 0:
		state = fmt.Sprintf("clean (%d held)", row.Held)
	}
	if row.ToolsMissing > 0 {
		state += fmt.Sprintf(", %d tools missing", row.ToolsMissing)
	}
	return state
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
