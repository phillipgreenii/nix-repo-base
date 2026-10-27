## How to work this bead

- Analyze the results recorded in this bead. Do NOT re-run `pg-go-mutate` or
  `pg-go-mutate-tui`.
- Decide which tests would close the gaps. A row no assertion can kill (a no-op
  or equivalent mutant) needs no test; say so.
- File one new bead per fix with `--deps "discovered-from:<this bead's id>"`
  (never a blocking edge). Its acceptance criteria name each missed path
  (`file:line` and the behavior) and the test(s) that must exist.
- Close this bead with a reason that lists the fix beads filed, or that says
  why no fix is needed.
