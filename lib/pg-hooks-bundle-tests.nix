# Eval tests for the per-clone hook bundle options of flake-modules/pre-commit.nix
# (spec docs/superpowers/specs/2026-10-01-per-clone-hook-bundle-design.md 4.2, 5.1,
# 5.2; plan Task 3, bead pg2-pla9d.7). Consumed by `lib.runTests`.
#
# Each fixture evaluates the REAL pre-commit and devshell flake modules (the same
# files consumers import) under flake-parts with one set of option values, then the
# tests read the result. Nothing is built: "fails evaluation" is
# `builtins.tryEval` over a forced `drvPath`, and bundle contents are read from
# `legacyPackages.<system>.pgHooksBundle.bundleData`, the evaluation-time view of the
# very values written into the bundle's JSON files. Every failure test has a
# CONTROL (the same fixture minus the offending line must evaluate), so a fixture
# that is broken for an unrelated reason cannot make a "fails" test pass.
{
  pkgs,
  inputs,
  system,
}:
let
  inherit (pkgs) lib;

  evalFixture =
    {
      precommit ? { },
    }:
    let
      evaluated = inputs.flake-parts.lib.evalFlakeModule { inherit inputs; } {
        imports = [
          (import ../flake-modules/pre-commit.nix inputs)
          ../flake-modules/devshell.nix
        ];
        systems = [ system ];
        perSystem = _: {
          _module.args.pkgs = pkgs;
        };
        phillipgreenii.pre-commit = precommit;
      };
    in
    evaluated.config.flake;

  # True when forcing the derivation path of `value` succeeds.
  evaluates = value: (builtins.tryEval (builtins.seq value.drvPath true)).success;

  bundleOf = fixture: fixture.legacyPackages.${system}.pgHooksBundle;
  installerOf = fixture: fixture.packages.${system}.install-pre-commit-hooks;

  plain = evalFixture { };
  enabled = evalFixture { precommit.bundle.enable = true; };

  withHook =
    hook:
    evalFixture {
      precommit = {
        bundle.enable = true;
        extraHooks.fixture-hook = {
          enable = true;
          name = "fixture-hook";
          pass_filenames = false;
        }
        // hook;
      };
    };

  withFixers =
    fixers:
    evalFixture {
      precommit = {
        bundle.enable = true;
        inherit fixers;
      };
    };

  fixerNamesOf = fixture: map (fixer: fixer.name) (bundleOf fixture).bundleData.fixers;
  defaultOrder = [
    "treefmt"
    "statix"
    "treefmt-final"
    "trailing-whitespace"
    "end-of-file-fixer"
  ];
  mine = {
    name = "mine";
    command = "true";
  };
in
{
  "test bundle disabled renders no pg-hooks-bundle and no new files" = {
    expr = {
      packages = builtins.attrNames plain.packages.${system};
      hasBundlePackage = plain.packages.${system} ? pg-hooks-bundle;
      checks = lib.filter (lib.hasPrefix "pg-hooks") (builtins.attrNames (plain.checks.${system} or { }));
      # The legacy installer fragments are still the devShell hook.
      legacyShellHook =
        lib.hasInfix "git-hooks.nix: updating"
          plain.devShells.${system}.default.shellHook;
    };
    expected = {
      packages = [
        "fix-lint"
        "install-pre-commit-hooks"
      ];
      hasBundlePackage = false;
      checks = [ ];
      legacyShellHook = true;
    };
  };

  "test bundle enabled renders packages.pg-hooks-bundle and evaluates" = {
    expr = {
      packaged = enabled.packages.${system} ? pg-hooks-bundle;
      evaluates = evaluates enabled.packages.${system}.pg-hooks-bundle;
      installer = evaluates (installerOf enabled);
    };
    expected = {
      packaged = true;
      evaluates = true;
      installer = true;
    };
  };

  "test bundle and commitTimeShim both enabled fails evaluation" = {
    expr = {
      shimAlone = evaluates (
        installerOf (evalFixture {
          precommit.commitTimeShim.enable = true;
        })
      );
      both =
        let
          both = evalFixture {
            precommit = {
              bundle.enable = true;
              commitTimeShim.enable = true;
            };
          };
        in
        {
          installer = evaluates (installerOf both);
          bundle = evaluates (bundleOf both);
          preCommitCheck = evaluates both.checks.${system}.pre-commit;
        };
    };
    expected = {
      shimAlone = true;
      both = {
        installer = false;
        bundle = false;
        preCommitCheck = false;
      };
    };
  };

  "test a fixer attached to a prek stage fails evaluation" = {
    expr = {
      control = evaluates (bundleOf (withFixers [ mine ]));
      attached = evaluates (
        bundleOf (withFixers [
          (
            mine
            // {
              stages = [ "pre-commit" ];
            }
          )
        ])
      );
    };
    expected = {
      control = true;
      attached = false;
    };
  };

  "test a prek hook whose entry runs pg-hooks fix fails evaluation" = {
    expr = {
      control = evaluates (
        bundleOf (withHook {
          entry = "true";
        })
      );
      pgHooksFix = evaluates (
        bundleOf (withHook {
          entry = "pg-hooks fix";
        })
      );
      pgHooksFixWithPath = evaluates (
        bundleOf (withHook {
          entry = "/nix/store/x-pg-hooks/bin/pg-hooks   fix --all";
        })
      );
      preCommitFix = evaluates (
        bundleOf (withHook {
          entry = "sh -c 'pre-commit-fix'";
        })
      );
    };
    expected = {
      control = true;
      pgHooksFix = false;
      pgHooksFixWithPath = false;
      preCommitFix = false;
    };
  };

  "test an unknown stage fails evaluation" = {
    expr = {
      control = evaluates (
        bundleOf (withHook {
          entry = "true";
          stages = [ "pre-push" ];
        })
      );
      unknown = evaluates (
        bundleOf (withHook {
          entry = "true";
          stages = [ "not-a-stage" ];
        })
      );
    };
    expected = {
      control = true;
      unknown = false;
    };
  };

  "test fixers defaults are the shared order" = {
    expr = fixerNamesOf enabled;
    expected = defaultOrder;
  };

  "test fixers anchors: an after=statix insert lands right after statix" = {
    expr = {
      after = fixerNamesOf (withFixers [
        (
          mine
          // {
            after = "statix";
          }
        )
      ]);
      before = fixerNamesOf (withFixers [
        (
          mine
          // {
            before = "statix";
          }
        )
      ]);
      unanchored = fixerNamesOf (withFixers [ mine ]);
      declaredOrderKept = fixerNamesOf (withFixers [
        (
          mine
          // {
            name = "first";
            after = "statix";
          }
        )
        (
          mine
          // {
            name = "second";
            after = "statix";
          }
        )
      ]);
      chained = fixerNamesOf (withFixers [
        (
          mine
          // {
            name = "tail";
            after = "mine";
          }
        )
        (
          mine
          // {
            after = "statix";
          }
        )
      ]);
    };
    expected = {
      after = [
        "treefmt"
        "statix"
        "mine"
        "treefmt-final"
        "trailing-whitespace"
        "end-of-file-fixer"
      ];
      before = [
        "treefmt"
        "mine"
        "statix"
        "treefmt-final"
        "trailing-whitespace"
        "end-of-file-fixer"
      ];
      unanchored = defaultOrder ++ [ "mine" ];
      declaredOrderKept = [
        "treefmt"
        "statix"
        "first"
        "second"
        "treefmt-final"
        "trailing-whitespace"
        "end-of-file-fixer"
      ];
      chained = [
        "treefmt"
        "statix"
        "mine"
        "tail"
        "treefmt-final"
        "trailing-whitespace"
        "end-of-file-fixer"
      ];
    };
  };

  "test fixers anchors that cannot resolve fail evaluation" = {
    expr = {
      unknownAnchor = evaluates (
        bundleOf (withFixers [
          (
            mine
            // {
              after = "nope";
            }
          )
        ])
      );
      bothAnchors = evaluates (
        bundleOf (withFixers [
          (
            mine
            // {
              after = "statix";
              before = "treefmt";
            }
          )
        ])
      );
      duplicateName = evaluates (
        bundleOf (withFixers [
          (
            mine
            // {
              name = "statix";
            }
          )
        ])
      );
      cycle = evaluates (
        bundleOf (withFixers [
          (
            mine
            // {
              name = "a";
              after = "b";
            }
          )
          (
            mine
            // {
              name = "b";
              after = "a";
            }
          )
        ])
      );
    };
    expected = {
      unknownAnchor = false;
      bothAnchors = false;
      duplicateName = false;
      cycle = false;
    };
  };

  "test fixers.json default entries carry mode and includes" = {
    expr =
      let
        byName = lib.listToAttrs (
          map (fixer: lib.nameValuePair fixer.name fixer) (bundleOf enabled).bundleData.fixers
        );
      in
      {
        statix = {
          inherit (byName.statix) mode includes;
          isStatixFix = lib.hasSuffix "/bin/statix fix" byName.statix.command;
        };
        treefmt = {
          inherit (byName.treefmt) mode includes;
        };
        keys = builtins.attrNames byName.statix;
      };
    expected = {
      statix = {
        mode = "per-file";
        includes = [ "*.nix" ];
        isStatixFix = true;
      };
      treefmt = {
        mode = "files";
        includes = [ ];
      };
      keys = [
        "command"
        "excludes"
        "includes"
        "mode"
        "name"
      ];
    };
  };

  "test stages.json is the union of default_stages and per-hook stages" = {
    expr =
      let
        fixture = withHook {
          entry = "true";
          stages = [
            "pre-push"
            "pre-rebase"
          ];
          description = "Fixture hook.\nSecond line is dropped.";
        };
        stages = (bundleOf fixture).bundleData.stages;
        idsAt = stage: map (entry: entry.id) stages.${stage};
        fixtureEntry = lib.findFirst (e: e.id == "fixture-hook") null stages.pre-push;
        treefmtEntry = lib.findFirst (e: e.id == "treefmt") null stages.pre-commit;
      in
      {
        names = builtins.attrNames stages;
        fixtureOnlyAtItsStages = {
          preCommit = lib.elem "fixture-hook" (idsAt "pre-commit");
          prePush = lib.elem "fixture-hook" (idsAt "pre-push");
          preRebase = lib.elem "fixture-hook" (idsAt "pre-rebase");
        };
        defaultStageHasBaseHooks = lib.elem "treefmt" (idsAt "pre-commit");
        reasonFromDescription = fixtureEntry.reason;
        reasonFallsBackToId = treefmtEntry.reason != "";
        entryKeys = builtins.attrNames fixtureEntry;
        # meta.json lists the same stages.
        metaStages = (bundleOf fixture).bundleData.meta.stages;
      };
    expected = {
      names = [
        "pre-commit"
        "pre-push"
        "pre-rebase"
      ];
      fixtureOnlyAtItsStages = {
        preCommit = false;
        prePush = true;
        preRebase = true;
      };
      defaultStageHasBaseHooks = true;
      reasonFromDescription = "Fixture hook.";
      reasonFallsBackToId = true;
      entryKeys = [
        "id"
        "reason"
      ];
      metaStages = [
        "pre-commit"
        "pre-push"
        "pre-rebase"
      ];
    };
  };

  "test stages.json reason falls back to the hook id" = {
    expr =
      let
        stages =
          (bundleOf (withHook {
            entry = "true";
          })).bundleData.stages;
      in
      (lib.findFirst (e: e.id == "fixture-hook") null stages.pre-commit).reason;
    expected = "fixture-hook";
  };

  "test meta.json stampPaths equals the option" = {
    expr = {
      default = (bundleOf enabled).bundleData.meta.stampPaths;
      set =
        (bundleOf (evalFixture {
          precommit = {
            bundle.enable = true;
            stampPaths = [
              "flake-modules/pre-commit.nix"
              "modules/pg-git-check-identity"
            ];
          };
        })).bundleData.meta.stampPaths;
    };
    expected = {
      default = [ ];
      set = [
        "flake-modules/pre-commit.nix"
        "modules/pg-git-check-identity"
      ];
    };
  };

  "test devShell shellHook performs no install when bundle.enable" = {
    expr = {
      enabled = enabled.devShells.${system}.default.shellHook;
      disabledInstalls = lib.hasInfix "prek install" plain.devShells.${system}.default.shellHook;
    };
    expected = {
      enabled = "";
      disabledInstalls = true;
    };
  };

  "test install-pre-commit-hooks execs pg-hooks-install with the bundle when bundle.enable" = {
    expr =
      let
        # hasInfix matches by regex, which refuses a string that refers to a store
        # path, so compare on the text with its string context dropped.
        noCtx = builtins.unsafeDiscardStringContext;
        text = noCtx (installerOf enabled).text;
        plainText = noCtx (installerOf plain).text;
        bundlePath = noCtx "${bundleOf enabled}";
      in
      {
        execsInstaller =
          lib.hasInfix "exec " text
          && lib.hasInfix "/bin/pg-hooks-install --bundle ${bundlePath} \"$@\"" text;
        # None of the legacy fragments or a hooksPath write.
        noHooksPathWrite = !(lib.hasInfix "core.hooksPath" text);
        noLegacyInstall = !(lib.hasInfix "git-hooks.nix: updating" text);
        # Control: without bundle.enable the legacy installer is unchanged.
        plainIsLegacy = !(lib.hasInfix "pg-hooks-install" plainText);
      };
    expected = {
      execsInstaller = true;
      noHooksPathWrite = true;
      noLegacyInstall = true;
      plainIsLegacy = true;
    };
  };
}
