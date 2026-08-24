// Command releasecheck runs CodePilot's tag-release preflight.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/eaglc/codepilot/internal/releasecheck"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}

func run(ctx context.Context, arguments []string) int {
	flags := flag.NewFlagSet("releasecheck", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", ".", "repository root")
	version := flags.String("version", "", "semantic version without a leading v")
	commit := flags.String("commit", "", "full Git commit object ID")
	buildDate := flags.String("date", "", "Git commit timestamp in RFC3339 format")
	requireClean := flags.Bool("require-clean", false, "reject tracked or untracked repository changes")
	requireChangelog := flags.Bool("require-changelog", false, "require a dated release entry in CHANGELOG.md")
	jsonOutput := flags.Bool("json", false, "write the successful report as JSON")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "releasecheck: unexpected positional arguments")
		return 2
	}
	report, err := releasecheck.Verify(ctx, releasecheck.Options{
		Root:             *root,
		Metadata:         releasecheck.Metadata{Version: *version, Commit: *commit, BuildDate: *buildDate},
		RequireClean:     *requireClean,
		RequireChangelog: *requireChangelog,
	})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "releasecheck: "+err.Error())
		return 1
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "releasecheck: encode report: "+err.Error())
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintf(os.Stdout, "Release inputs verified with %s.\n", report.GoVersion)
	for _, artifact := range report.Artifacts {
		_, _ = fmt.Fprintf(os.Stdout, "- %s sha256:%s\n", artifact.Target, artifact.SHA256)
	}
	return 0
}
