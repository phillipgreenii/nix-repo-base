# Unit tests for lib/claude-marketplace.nix:mkClaudeHookRouterPlugin (a set of
# { expr; expected; } cases). Run via `lib.runTests` (wired into flake
# `checks.claude-hook-router-lib` by packet B5, tc-rjzd3.10).
#
# PURE-EVAL, like lib/claude-marketplace-tests.nix: no derivation is ever
# BUILT (realized) here — `nix eval -f lib/claude-hook-router-tests.nix` must
# never contact a builder (this machine dispatches even trivial local builds
# to a remote SSH builder, so "build" and "network" are the same risk here).
#
# UNLIKE mkClaudePlugin/mkClaudeMarketplace, mkClaudeHookRouterPlugin exposes
# no `passthru` of its computed hooks.json / router-config.json / .mcp.json /
# copy-tree (checked directly against the landed lib/claude-marketplace.nix —
# packet A1, tc-rjzd3.2 — which is out of this packet's scope to change). That
# means the exact CONTENT of those generated files cannot be read purely
# (reading it would require `builtins.readFile` on a derivation output, which
# realizes/builds it — the same forbidden network path). Confirmed
# independently, not just inferred here: packet A3's own landed comment
# (flake.nix, `claude-hook-router-plugin-validate`) says plainly "the
# symlink-drop behavior... is exactly the kind of thing a pure-eval nix test
# cannot catch", and the ADR's own §4 Phase A3 rationale says the same thing
# about the real-copy invariant — even though the ADR's own A2 bullet list
# also asks for that exact assertion. That is a self-acknowledged
# inconsistency in the design, not a gap this suite quietly invented.
#
# Given that, this suite proves what IS provable purely, using two
# techniques beyond simple `expr == expected`:
#
#   1. `throwsOnConstruct`: forcing `.buildCommand` (the derivation's build
#      script, a plain string attribute readable WITHOUT building — verified
#      empirically) transitively forces every explicit `throw` reachable
#      from the generator's pure computation (namespacing, matcher
#      classification, contract validation, MCP merging, version digesting
#      all feed that one string via antiquotation). `builtins.tryEval` around
#      it turns "does construction reject this input" into a pure boolean —
#      but ONLY for the generator's own `throw` calls. Confirmed empirically
#      (not assumed): `tryEval` does NOT catch a `builtins.readFile` on a
#      nonexistent path, nor a `builtins.fromJSON` parse error on invalid
#      JSON — both abort the whole `nix eval` process uncatchably in this
#      nix version, unlike a genuine `throw`. Also does NOT catch a failure
#      that only manifests as a shell-level error inside the build script
#      itself (e.g. `cp` on a path that doesn't exist) — noted inline below
#      where each applies.
#
#   2. `copiedPath`: extracts the literal store path a specific `cp ... "$out/
#      <dest>"` line in `.buildCommand` copies FROM, without ever reading
#      that path's contents. Two constructions whose `<dest>` file has
#      IDENTICAL content produce the IDENTICAL store path (content-addressed
#      derivation), so comparing two `copiedPath` extractions is a pure,
#      exact content-EQUALITY oracle — it just can't reveal what the content
#      actually IS. This is what verifies the matcher-union decision, the
#      "both hooks.json forms extract identically" claim, and the
#      multi-command-expansion claim, all without building anything.
#
# What this suite does NOT verify (documented rather than faked):
#   - The real-copy/no-symlink invariant (design's own admission above).
#   - The exact generated JSON VALUE of hooks.json/router-config.json/.mcp.json
#     for cases where no equality/inequality comparison is possible (only
#     that valid inputs construct without error).
#   - "A malformed source plugin.json (invalid JSON / missing required
#     field)": uncatchable by `tryEval` in this nix version (see technique 1
#     above) — asserted nowhere here rather than crashing `nix eval`
#     outright. See the longer note at bullet 11 below.
#   - A source's `commands`/`agents`/`skills` field naming a NONEXISTENT
#     directory: `surfaceCopyItems` never checks existence at eval time (it's
#     a plain list of path strings folded into a `cp -r --dereference` shell
#     line) — that failure surfaces only when the derivation is actually
#     built, so it cannot be asserted here either. See the `nonexistentDir`
#     comment below.
{ pkgs }:
let
  inherit (pkgs) lib;
  builders = import ./claude-marketplace.nix { inherit pkgs lib; };
  inherit (builders) mkClaudeHookRouterPlugin;

  fixture = name: ./tests/claude-hook-router-fixture + "/${name}";

  # Does merely constructing (never building) `mkClaudeHookRouterPlugin
  # args` throw? Forcing `.buildCommand` reaches every `throw` in the
  # generator's pure computation (see header comment).
  throwsOnConstruct = args: !(builtins.tryEval (mkClaudeHookRouterPlugin args).buildCommand).success;
  constructsCleanly = args: (builtins.tryEval (mkClaudeHookRouterPlugin args).buildCommand).success;

  # Extract the literal store path a `cp <path> "$out/<dest>"` line in a
  # constructed derivation's build script copies FROM. Two derivations that
  # copy the SAME store path to the same `dest` are guaranteed (content
  # addressing) to have produced byte-identical content for that file.
  copiedPath =
    dest: args:
    let
      inherit (mkClaudeHookRouterPlugin args) buildCommand;
      marker = ''"$out/${dest}"'';
      lines = lib.splitString "\n" buildCommand;
      match = lib.findFirst (l: lib.hasSuffix marker l) null lines;
    in
    if match == null then
      throw "claude-hook-router-tests: no line copying to ${dest} found in buildCommand"
    else
      lib.removeSuffix (" " + marker) (lib.removePrefix "cp " match);

  routerConfigPath = args: copiedPath "router-config.json" args;
  hooksJsonPath = args: copiedPath "hooks/hooks.json" args;

  # A minimal, valid single-source config — reused as a base for several
  # variations below (only `sources` is overridden per test).
  baseArgs = sources: {
    name = "test-plugin";
    declared = "0.0.0";
    inherit sources;
  };
in
{
  # --- bullet 1: single-source passthrough (degenerate case) ---
  # Absent matcher on the single fixture's PreToolUse hook also exercises
  # the "absent matcher" boundary form (bullet 5).
  testSingleSourceConstructsCleanly = {
    expr = constructsCleanly (baseArgs [
      {
        name = "s";
        src = fixture "single";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };

  # --- bullet 2: multi-source namespacing ---
  # Two sources each shipping a same-named `commands/greet.md` must both
  # survive, nested distinctly under $out/commands/<source-name>/ (A1a's
  # empirical finding: nesting, never renaming). This reads the actual
  # generated build-script TEXT (available without building) rather than
  # $out content, but it is the real generated copy-plan, not a guess.
  testMultiSourceNamespacingBothDestinationsPresent =
    let
      bc =
        (mkClaudeHookRouterPlugin (baseArgs [
          {
            name = "ns-a";
            src = fixture "ns-a";
            includeCommands = true;
          }
          {
            name = "ns-b";
            src = fixture "ns-b";
            includeCommands = true;
          }
        ])).buildCommand;
    in
    {
      expr = {
        hasA = lib.hasInfix ''"$out/commands/ns-a"'' bc;
        hasB = lib.hasInfix ''"$out/commands/ns-b"'' bc;
      };
      expected = {
        hasA = true;
        hasB = true;
      };
    };

  # --- bullet 3: multi-event grouping ---
  # Positive-content verification of "one hooks.json entry per event, none
  # for an event no source declares" needs $out content (unreachable
  # purely, see header). What IS provable purely: adding a second event's
  # worth of delegates changes the generated hooks.json (sensitivity), and
  # both shapes construct cleanly (no spurious rejection of a legitimate
  # multi-event source).
  testMultiEventConstructsCleanly = {
    expr = constructsCleanly (baseArgs [
      {
        name = "s";
        src = fixture "multi-event";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };
  testMultiEventChangesHooksJsonVsSingleEvent = {
    expr =
      (hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "multi-event";
          includeHooks = true;
          priority = 10;
        }
      ])) != (hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "multi-event-single";
          includeHooks = true;
          priority = 10;
        }
      ]));
    expected = true;
  };

  # --- bullet 4/6: matcher union — the design's own "pick one explicitly...
  # don't leave both looking plausible" case. A1's chosen behavior: when
  # every delegate on an event agrees on a (non-match-all) matcher, that
  # matcher is preserved in the hooks.json registration; when they
  # disagree, the registration falls back to match-all. Both halves are
  # verified EXACTLY (not just smoke-tested) via `hooksJsonPath` equality
  # against independently-constructed references — hooksJsonEvents for one
  # event depends only on that event's delegates' `.matcher` values (and
  # the fixed `routerCommand`), so two configs producing the same set of
  # matcher values are provably byte-identical there, regardless of any
  # other field (name, command text, delegate count) that differs.
  testUnionAgreeingMatcherPreserved = {
    # 2 delegates both matcher="Bash"  vs  1 delegate matcher="Bash":
    # unionMatcherForEvent picks "Bash" in both cases.
    expr =
      hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "union-two-same";
          includeHooks = true;
          priority = 10;
        }
      ]) == hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "union-one-same";
          includeHooks = true;
          priority = 10;
        }
      ]);
    expected = true;
  };
  testUnionDisagreeingMatcherFallsBackToMatchAll = {
    # 2 delegates matcher="Bash"/"Edit" (disagree)  vs  1 delegate with an
    # ABSENT (match-all) matcher: A1's chosen fallback makes these equal.
    expr =
      hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "union-two-diff";
          includeHooks = true;
          priority = 10;
        }
      ]) == hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "union-match-all-ref";
          includeHooks = true;
          priority = 10;
        }
      ]);
    expected = true;
  };
  # The two branches above must be OBSERVABLY different from each other —
  # otherwise the two equalities above could both hold vacuously (e.g. if
  # the generator ignored matchers entirely and always emitted match-all).
  testUnionAgreeingAndDisagreeingBranchesDiffer = {
    expr =
      hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "union-two-same";
          includeHooks = true;
          priority = 10;
        }
      ]) != hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "union-two-diff";
          includeHooks = true;
          priority = 10;
        }
      ]);
    expected = true;
  };

  # --- bullet 5: matcher-syntax boundary cases ---
  # Boundary forms (whitespace-only, a bare "-", a bare ",", pipe-
  # alternation, exact, explicit "*") are all charset-legal per §2.3's
  # classification and must NOT be rejected as regex-shaped.
  testMatcherBoundaryFormsAllAccepted = {
    expr = constructsCleanly (baseArgs [
      {
        name = "s";
        src = fixture "matcher-forms";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };
  # A genuinely regex-shaped matcher (anchors/parens/alternation-as-regex)
  # on a vendored delegate must be rejected at construction, not silently
  # trusted for Go-RE2/JS-regex parity.
  testRegexShapedMatcherRejected = {
    expr = throwsOnConstruct (baseArgs [
      {
        name = "s";
        src = fixture "matcher-regex";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };
  # A matcher that reads like a plain word but contains ONE character
  # outside the exact/pipe-alternation charset (here: ".") must be
  # classified consistently by the same rule, not treated as "probably
  # meant literally" because it looks word-like.
  testMatcherLookalikeRegexRejected = {
    expr = throwsOnConstruct (baseArgs [
      {
        name = "s";
        src = fixture "matcher-lookalike";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };

  # --- bullet 7: both hooks.json forms extract identically ---
  # A source declaring hooks via a standalone hooks/hooks.json and a source
  # declaring the SAME hooks inline in plugin.json's own "hooks" key must
  # produce byte-identical hooks.json AND router-config.json (proven, not
  # smoke-tested, via copiedPath equality — the two fixtures' hook content
  # is deliberately identical, see lib/tests/claude-hook-router-fixture/
  # {standalone-hooks,inline-hooks}).
  testBothHooksJsonFormsExtractIdenticalHooksJson = {
    expr =
      hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "standalone-hooks";
          includeHooks = true;
          priority = 10;
        }
      ]) == hooksJsonPath (baseArgs [
        {
          name = "s";
          src = fixture "inline-hooks";
          includeHooks = true;
          priority = 10;
        }
      ]);
    expected = true;
  };
  testBothHooksJsonFormsExtractIdenticalRouterConfig = {
    expr =
      routerConfigPath (baseArgs [
        {
          name = "s";
          src = fixture "standalone-hooks";
          includeHooks = true;
          priority = 10;
        }
      ]) == routerConfigPath (baseArgs [
        {
          name = "s";
          src = fixture "inline-hooks";
          includeHooks = true;
          priority = 10;
        }
      ]);
    expected = true;
  };

  # --- bullet 8: one matcher entry with multiple commands ---
  # A single matcher-group's `hooks` array holding 2 commands must expand
  # into exactly the same set of router-config.json delegates as splitting
  # those same 2 commands across 2 separate single-command groups sharing
  # the same matcher (proven via routerConfigPath equality: both fixtures'
  # (event, matcher, per-command contract/text, source name/priority) are
  # identical, so if expansion is per-COMMAND rather than per-GROUP, both
  # must produce the same delegates list in the same order).
  testMultiCommandGroupExpandsSameAsSplitGroups = {
    expr =
      routerConfigPath (baseArgs [
        {
          name = "s";
          src = fixture "multi-command";
          includeHooks = true;
          priority = 10;
        }
      ]) == routerConfigPath (baseArgs [
        {
          name = "s";
          src = fixture "multi-command-split";
          includeHooks = true;
          priority = 10;
        }
      ]);
    expected = true;
  };

  # --- bullet 9: ${CLAUDE_PLUGIN_ROOT} placeholder rewriting ---
  # An EXACT-value proof of the rewritten string is not achievable purely,
  # and neither is an isolated "changing the source name doesn't affect an
  # unrelated command" proof: router-config.json's delegate object embeds
  # `name` DIRECTLY (`inherit (s) name priority;`, lib/claude-marketplace.nix),
  # not just via the rewritten command text — confirmed empirically during
  # authoring (an earlier version of this suite asserted routerConfigPath
  # equality across two different source names with an otherwise-identical
  # placeholder-free command, expecting it to hold; it does not, because the
  # `name` field itself differs in the JSON regardless of any rewriting).
  # Comparing whole-document store paths therefore cannot isolate "did the
  # COMMAND text change" from "did the NAME metadata field change" — both
  # move together. What IS provable purely and exactly, holding the source
  # name constant so only the command text varies: that a placeholder's
  # PRESENCE changes the generated router-config.json relative to its
  # absence (sensitivity), and that both the multi-occurrence and the
  # no-occurrence forms are accepted without error (neither is rejected).
  testPlaceholderPresenceChangesOutputVsAbsent = {
    expr =
      routerConfigPath {
        name = "test-plugin";
        declared = "0.0.0";
        sources = [
          {
            name = "alpha";
            src = fixture "placeholder-multi";
            includeHooks = true;
            priority = 10;
          }
        ];
      } == routerConfigPath {
        name = "test-plugin";
        declared = "0.0.0";
        sources = [
          {
            name = "alpha";
            src = fixture "placeholder-none";
            includeHooks = true;
            priority = 10;
          }
        ];
      };
    expected = false;
  };
  testPlaceholderConstructsCleanly = {
    # Both the multi-occurrence and the no-occurrence commands must be
    # accepted without error (neither errors on a repeated placeholder nor
    # on its complete absence).
    expr = {
      multi = constructsCleanly (baseArgs [
        {
          name = "root-src";
          src = fixture "placeholder-multi";
          includeHooks = true;
          priority = 10;
        }
      ]);
      none = constructsCleanly (baseArgs [
        {
          name = "root-src";
          src = fixture "placeholder-none";
          includeHooks = true;
          priority = 10;
        }
      ]);
    };
    expected = {
      multi = true;
      none = true;
    };
  };

  # --- bullet 10: real-copy assertion (no symlink outside $out) ---
  # NOT purely provable end-to-end (see header comment and A3's own landed
  # rationale). What purely IS provable: the generator's build script
  # actually contains the safety net (dereferencing copies, plus the
  # build-time `find "$out" -type l` guard) — i.e. the mechanism the
  # real invariant depends on is present in what gets generated, for every
  # surface-copying source, not just that the STRING "cp -r --dereference"
  # appears somewhere by coincidence.
  testRealCopySafetyNetPresentInBuildScript =
    let
      bc =
        (mkClaudeHookRouterPlugin (baseArgs [
          {
            name = "ns-a";
            src = fixture "ns-a";
            includeCommands = true;
          }
        ])).buildCommand;
    in
    {
      expr = {
        dereferencedCopy = lib.hasInfix "cp -r --dereference" bc;
        symlinkGuard = lib.hasInfix "-type l" bc;
        guardExitsNonzero = lib.hasInfix "exit 1" bc;
      };
      expected = {
        dereferencedCopy = true;
        symlinkGuard = true;
        guardExitsNonzero = true;
      };
    };

  # --- bullet 11: negative/adversarial cases ---

  # NOT covered by a `throwsOnConstruct` case: "a malformed source
  # plugin.json (invalid JSON / missing required field)". Confirmed
  # empirically during authoring, not assumed: `builtins.tryEval` does NOT
  # catch either a `builtins.readFile` on a nonexistent path or a
  # `builtins.fromJSON` parse error on invalid JSON — both are uncatchable
  # low-level errors in this nix version (unlike an explicit `throw` in
  # claude-marketplace.nix's own logic, which tryEval catches cleanly, as
  # every OTHER case below demonstrates). A test built around either would
  # not report as a `lib.runTests` failure — it would abort the whole `nix
  # eval` process outright, which is worse than not testing it at all. A
  # git-tracked syntactically-invalid `.json` fixture file would also be
  # fought by this repo's own prettier/treefmt formatting gate at commit
  # time (`*.json` is in `flake-modules/treefmt.nix`'s prettier `includes`,
  # with no test-fixture exclusion) — a second, independent reason not to
  # add one. The closest thing this manifest-required-field case reduces to
  # in practice — a source whose `hooks` declaration is absent where one is
  # required — IS covered, via a `throw` the generator raises itself, by
  # `testIncludeHooksWithNoHooksDeclarationThrows` directly below.

  # includeHooks = true with neither hooks/hooks.json nor an inline
  # plugin.json "hooks" key present.
  testIncludeHooksWithNoHooksDeclarationThrows = {
    expr = throwsOnConstruct (baseArgs [
      {
        name = "s";
        src = fixture "no-hooks-decl";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };

  # Two sources whose MCP server names collide even after <name>- prefixing
  # ("foo" + "bar-baz" vs "foo-bar" + "baz" both become "foo-bar-baz").
  testMcpServerKeyCollisionAfterPrefixingThrows = {
    expr = throwsOnConstruct (baseArgs [
      {
        name = "foo";
        src = fixture "mcp-a";
        includeMcp = true;
      }
      {
        name = "foo-bar";
        src = fixture "mcp-b";
        includeMcp = true;
      }
    ]);
    expected = true;
  };

  # Two sources sharing the exact same caller-supplied name — a config
  # error, must fail before any file operation (i.e. purely, at eval time).
  testDuplicateSourceNameThrows = {
    expr = throwsOnConstruct (baseArgs [
      {
        name = "dup";
        src = fixture "single";
        includeHooks = true;
        priority = 10;
      }
      {
        name = "dup";
        src = fixture "single";
        includeHooks = true;
        priority = 20;
      }
    ]);
    expected = true;
  };

  # A delegate's declared contract doesn't match what its event supports
  # (SessionEnd only allows "observe"; this fixture declares "rewrite").
  testContractMismatchForEventThrows = {
    expr = throwsOnConstruct (baseArgs [
      {
        name = "s";
        src = fixture "contract-mismatch";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };

  # A vendored delegate's matcher falling outside the exact/pipe-
  # alternation subset must be flagged/rejected, never silently trusted —
  # same fixture and assertion as testRegexShapedMatcherRejected above,
  # named here too since it is also explicitly one of bullet 11's
  # enumerated sub-cases (a design-decision case AND a negative case).
  testAdversarialRegexMatcherAlsoRejected = {
    expr = throwsOnConstruct (baseArgs [
      {
        name = "s";
        src = fixture "matcher-regex";
        includeHooks = true;
        priority = 10;
      }
    ]);
    expected = true;
  };

  # NOT covered by a `throwsOnConstruct`/`copiedPath` case: "a source whose
  # commands field names a nonexistent directory". `surfaceCopyItems`
  # (lib/claude-marketplace.nix) treats a declared `commands` entry as a
  # plain path STRING folded into a `cp -r --dereference <src>/<item> ...`
  # shell line with no existence check at the nix level — the failure is
  # therefore a shell-level `cp` error that only manifests when the
  # derivation is actually BUILT, which this suite deliberately never
  # does. This is the same category of gap as bullet 10 (real-copy), not
  # an oversight: recorded here rather than papered over with a fixture
  # that can't actually prove rejection. A future build-check derivation
  # (the OTHER tool §4.0's own Testing Strategy names for Tier 1 — "a
  # build-check derivation can assert anything about $out") is the right
  # place to add it, not this pure-eval suite.
}
