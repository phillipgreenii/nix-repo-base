package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/eventlog"
	"github.com/phillipgreenii/nix-repo-base/modules/pn/internal/traceexplain"
)

const (
	defaultTempoURL = "http://127.0.0.1:3200"
	explainTimeout  = 30 * time.Second
	// tempoRetention is how long Tempo keeps a trace; a run that ended longer
	// ago than this has expired rather than not yet been ingested.
	tempoRetention = 7 * 24 * time.Hour
	anyRoot        = "<any root>"
)

// now is the clock explain reads; tests pin it. An injected clock (not
// testing/synctest) is right here because the time it is compared against is
// persisted (run_end in events.jsonl).
var now = time.Now

var traceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

// tempoURL picks the Tempo base URL: the flag, else the env value, else the
// local default.
func tempoURL(flagVal, envVal string) string {
	u := flagVal
	if u == "" {
		u = envVal
	}
	if u == "" {
		u = defaultTempoURL
	}
	return strings.TrimRight(u, "/")
}

// eventRec is the subset of an events.jsonl record explain reads.
type eventRec struct {
	Kind    string `json:"kind"`
	Time    string `json:"time"`
	Root    string `json:"root"`
	TraceID string `json:"trace_id"`
	at      time.Time
}

// readEvents returns the records of events.jsonl.1 then events.jsonl, oldest
// first. A missing file reads as empty; lines that do not parse are skipped.
// Lines have no length cap (bufio.Scanner would stop at 64 KiB).
func readEvents(path string) []eventRec {
	var recs []eventRec
	for _, p := range []string{path + ".1", path} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		br := bufio.NewReader(f)
		for {
			line, err := br.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				var r eventRec
				if json.Unmarshal(line, &r) == nil {
					r.at, _ = time.Parse(time.RFC3339Nano, r.Time)
					recs = append(recs, r)
				}
			}
			if err != nil {
				break
			}
		}
		_ = f.Close()
	}
	return recs
}

func exitErr(code int, format string, a ...any) error {
	return ExitCodeError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

func workspaceExplainCmd() *cobra.Command {
	var asJSON, partial bool
	var topFlag, tempoFlag string
	cmd := &cobra.Command{
		Use:   "explain [<trace-id>] [--json] [--top N] [--partial] [--tempo-url URL]",
		Short: "Report where a pn run spent its time, from its Tempo trace",
		Long: `Report where a pn run spent its time, from its trace in Tempo.

With no trace id it explains the newest 'pn workspace update' run recorded for
the current workspace root in ${XDG_STATE_HOME:-$HOME/.local/state}/pn/events.jsonl
(and events.jsonl.1). If no workspace root resolves, it uses the newest run
overall. pn workspace upgrade and doctor --fix record no run; pass the trace id
(PN_TRACE_HINT=1 or -v prints it).

The trace is read from $PG_TEMPO_URL, else http://127.0.0.1:3200 (--tempo-url
overrides both). --json prints the report as JSON (steps keep start order; the
text sorts them). --top N keeps the N slowest builds (default 10). --partial
analyzes a trace that has no pn.verb span (a run still going or killed).

Exit codes (a non-zero exit prints nothing on stdout; the message is on stderr):
  0   report printed
  2   usage error (bad trace id, bad --top, unknown flag, too many arguments)
  3   nothing to explain: no matching run, no trace id recorded, an unknown or
      expired trace, or a run with no run_end and no spans in Tempo
  4   the run has no run_end, or no run recorded it, but Tempo has some of its
      spans (still running or killed); pass --partial to analyze them
  5   Tempo unreachable, answered with an error, or returned an unreadable trace
  75  the run ended under 7 days ago but Tempo has not ingested it yet; retry in
      a minute (75 is EX_TEMPFAIL, pn's "retry later" code)`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return ExitCodeError{Code: 2, Msg: "explain takes at most one trace id"}
			}
			if len(args) == 1 && !traceIDPattern.MatchString(args[0]) {
				return exitErr(2, "trace id must be 32 hex characters, got %q", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			top, err := strconv.Atoi(topFlag)
			if err != nil || top < 1 {
				return exitErr(2, "--top must be a positive integer, got %q", topFlag)
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			return explainRun(cmd, id, top, asJSON, partial, tempoURL(tempoFlag, os.Getenv("PG_TEMPO_URL")))
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return ExitCodeError{Code: 2, Msg: err.Error()}
	})
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	cmd.Flags().StringVar(&topFlag, "top", "10", "keep the N slowest builds")
	cmd.Flags().BoolVar(&partial, "partial", false, "analyze a trace that has no pn.verb span")
	cmd.Flags().StringVar(&tempoFlag, "tempo-url", "", "Tempo base URL (default $PG_TEMPO_URL, else "+defaultTempoURL+")")
	return cmd
}

func explainRun(cmd *cobra.Command, id string, top int, asJSON, partial bool, base string) error {
	path := eventlog.DefaultPath()
	recs := readEvents(path)
	header := ""
	if id == "" {
		root, rerr := resolveWorkspaceRoot("")
		rootLabel := root
		if rerr != nil {
			root, rootLabel = "", anyRoot
		}
		var starts []eventRec
		for _, r := range recs {
			if r.Kind == "run_start" {
				starts = append(starts, r)
			}
		}
		var chosen *eventRec
		for i := len(starts) - 1; i >= 0; i-- {
			if root == "" || starts[i].Root == root {
				chosen = &starts[i]
				break
			}
		}
		if chosen == nil {
			return exitErr(3, "no update run with a trace id for %s in %s; pass a trace id", rootLabel, path)
		}
		if chosen.TraceID == "" {
			return exitErr(3, "the newest update run for %s (%s) recorded no trace id (telemetry was off: enable it in ~/.config/pn/telemetry.toml or pass --otel-spool-dir); pass an older trace id from %s",
				rootLabel, rfcUTC(chosen.at), path)
		}
		id = chosen.TraceID
		header = fmt.Sprintf("Explaining the newest 'pn workspace update' run for %s: started %s (%s ago)",
			rootLabel, rfcUTC(chosen.at), now().Sub(chosen.at).Round(time.Second))
		if newest := starts[len(starts)-1]; root != "" && newest.Root != root && newest.at.After(chosen.at) {
			other := newest.Root
			if other == "" {
				other = "<unknown root>"
			}
			note := fmt.Sprintf("a newer run exists for %s at %s", other, rfcUTC(newest.at))
			if newest.TraceID != "" {
				note += ": pn workspace explain " + newest.TraceID
			}
			header += " (" + note + ")"
		}
	} else {
		header = "Explaining trace " + id
	}

	body, status, err := fetchTrace(cmd, base, id)
	if err != nil {
		return exitErr(5, "cannot read trace %s from %s: %v; is Tempo running? set PG_TEMPO_URL or --tempo-url", id, base, err)
	}
	var spans []traceexplain.Span
	switch {
	case status == http.StatusNotFound:
		return noTrace(recs, id, path, false)
	case status != http.StatusOK:
		return exitErr(5, "cannot read trace %s from %s: Tempo answered %d; is Tempo running? set PG_TEMPO_URL or --tempo-url", id, base, status)
	default:
		spans, err = traceexplain.ParseTempoV2(body)
		if err != nil {
			return exitErr(5, "cannot read trace %s from %s: %v; is Tempo running? set PG_TEMPO_URL or --tempo-url", id, base, err)
		}
		if len(spans) == 0 {
			return noTrace(recs, id, path, false)
		}
	}
	rep, err := traceexplain.Analyze(id, spans, top, partial)
	if errors.Is(err, traceexplain.ErrIncompleteTrace) {
		return noTrace(recs, id, path, true)
	}
	if err != nil {
		return exitErr(5, "cannot read trace %s from %s: %v; is Tempo running? set PG_TEMPO_URL or --tempo-url", id, base, err)
	}

	var out bytes.Buffer
	if asJSON {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return exitErr(5, "cannot read trace %s from %s: %v; is Tempo running? set PG_TEMPO_URL or --tempo-url", id, base, err)
		}
		out.Write(b)
		out.WriteByte('\n')
	} else if err := traceexplain.RenderText(&out, rep, header); err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(out.Bytes())
	return err
}

func rfcUTC(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func fetchTrace(cmd *cobra.Command, base, id string) ([]byte, int, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, explainTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v2/traces/"+id, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return b, resp.StatusCode, nil
}

// noTrace maps "Tempo has no analyzable trace for this id" to an exit code,
// using the run's own records. hasSpans is true when Tempo returned spans but
// no pn.verb span (and --partial is unset).
func noTrace(recs []eventRec, id, path string, hasSpans bool) error {
	var started bool
	var ended time.Time
	var haveEnd bool
	for _, r := range recs {
		if r.TraceID != id {
			continue
		}
		switch r.Kind {
		case "run_start":
			started = true
		case "run_end":
			if !haveEnd || r.at.After(ended) {
				ended, haveEnd = r.at, true
			}
		}
	}
	switch {
	case haveEnd && now().Sub(ended) >= tempoRetention:
		return exitErr(3, "trace %s has expired from Tempo (it keeps up to 7 days; the run ended %s)", id, rfcUTC(ended))
	case haveEnd:
		msg := "trace %s is not in Tempo yet (the run ended %s); retry in a minute; if it persists, run 'pn workspace doctor'"
		if hasSpans {
			msg += " or pass --partial"
		}
		return exitErr(75, msg, id, rfcUTC(ended))
	case started && hasSpans:
		return exitErr(4, "run %s has no run_end: it is still running or was killed; pass --partial to analyze what was recorded", id)
	case started:
		return exitErr(3, "run %s has no run_end and none of its spans are in Tempo: it is still running, or was killed before exporting; nothing to explain yet", id)
	case hasSpans:
		return exitErr(4, "trace %s has no pn.verb span in Tempo and no pn run in %s recorded it; pass --partial to analyze what was recorded", id, path)
	default:
		return exitErr(3, "trace %s is not in Tempo and no pn run in %s recorded it; check the id, or pass --tempo-url for another Tempo", id, path)
	}
}
