# Unit tests for lib/version.nix:mkVersion (a set of { expr; expected; } cases).
# Run via `lib.runTests` (wired into flake `checks.version-lib`).
let
  version = import ./version.nix;
  inherit (version) mkVersion;
  inherit (version) mkSrcDigest;
  sha8 = s: builtins.substring 0 8 (builtins.hashString "sha256" s);

  fullRev = "a41345da335be446172465681f16b43f895a0723";
  shortRev = "a41345d";
  digest = nar: builtins.substring 0 8 (builtins.hashString "sha256" nar);

  # Minimal stand-ins for a flake's `self`, covering the cases mkVersion sees.
  cleanSelf = {
    lastModifiedDate = "20260604225706";
    rev = fullRev;
    narHash = "sha256-AAA";
  };
  dirtySelfA = {
    lastModifiedDate = "20260604225706";
    dirtyRev = "${fullRev}-dirty";
    narHash = "sha256-AAA";
  };
  dirtySelfB = dirtySelfA // {
    narHash = "sha256-BBB";
  };
  nonGitSelf = {
    lastModifiedDate = "20260604225706";
    narHash = "sha256-AAA";
  };
in
{
  # A clean checkout keeps the original stable YYYYMMDD-<short-rev> form.
  testCleanVersion = {
    expr = mkVersion cleanSelf;
    expected = "20260604-${shortRev}";
  };

  # A dirty tree appends a content digest so nvd can attribute the change.
  testDirtyVersionAppendsDigest = {
    expr = mkVersion dirtySelfA;
    expected = "20260604-${shortRev}-dirty-${digest "sha256-AAA"}";
  };

  # Editing a dirty tree changes narHash, hence the version (the whole point).
  testDirtyVersionTracksContent = {
    expr = mkVersion dirtySelfA != mkVersion dirtySelfB;
    expected = true;
  };

  # Identical dirty content is deterministic (no spurious churn).
  testDirtyVersionStableForSameContent = {
    expr = mkVersion dirtySelfA == mkVersion dirtySelfA;
    expected = true;
  };

  # A non-git source (no rev/dirtyRev) still gets a content-tracked version.
  testNonGitVersion = {
    expr = mkVersion nonGitSelf;
    expected = "20260604-dev-dirty-${digest "sha256-AAA"}";
  };

  # Single source: digest is first8(sha256) of the (stringified) source.
  testSrcDigestSingle = {
    expr = mkSrcDigest "src-a";
    expected = sha8 "src-a";
  };
  # A single path equals the singleton list of that path.
  testSrcDigestSingleEqualsSingleton = {
    expr = mkSrcDigest "src-a" == mkSrcDigest [ "src-a" ];
    expected = true;
  };
  # Multiple sources are joined with ":" before hashing.
  testSrcDigestListConcat = {
    expr = mkSrcDigest [
      "a"
      "b"
    ];
    expected = sha8 "a:b";
  };
  # Order-sensitive (callers pass a stable, ordered list).
  testSrcDigestOrderSensitive = {
    expr =
      mkSrcDigest [
        "a"
        "b"
      ] != mkSrcDigest [
        "b"
        "a"
      ];
    expected = true;
  };
  # Content change changes the digest.
  testSrcDigestTracksContent = {
    expr = mkSrcDigest "a" != mkSrcDigest "b";
    expected = true;
  };
}
