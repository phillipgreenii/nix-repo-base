{
  mkBashScript,
  pkgs,
}:
mkBashScript {
  name = "gogate";
  src = ./.;
  description = "Sequential Go validation gate (fmt/build/vet/test) with fixed output truncation";
  public = true;
  # go/gofmt are resolved from the ambient PATH only, never bundled as
  # runtimeDeps: a runtimeDep is appended via `--suffix`, and forcing a
  # nix-pinned toolchain onto PATH here would run every gated project against
  # a Go version that may disagree with its own go.mod / devShell toolchain.
  # Needed only for the check derivation's bats run (mocked go/gofmt stubs
  # are prepended ahead of these in every test, per the bash-scripting
  # skill's mocking convention), so this is testDeps, not runtimeDeps.
  testDeps = [
    pkgs.go
  ];
  batsJobs = 4;
}
