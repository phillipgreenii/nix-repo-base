# Unit tests for lib/version.nix:mkVersion (a set of { expr; expected; } cases).
# Run via `lib.runTests` (wired into flake `checks.version-lib`).
let
  version = import ./version.nix;
  inherit (version) mkVersion;
  inherit (version) mkSrcDigest;
  fullRev = "a41345da335be446172465681f16b43f895a0723";
  shortRev = "a41345d";
  digest = nar: builtins.substring 0 8 (builtins.hashString "sha256" nar);

  # Sources are store-path-like values, never bare strings (bead pg2-6mrm7). A
  # `{ outPath = ...; }` attrset stands in for a lib.cleanSource/derivation
  # result: it satisfies the content-addressed contract and coerces to its
  # outPath, so these digests match the documented first8(sha256(...)) format.
  srcA = {
    outPath = "src-a";
  };
  srcOnlyA = {
    outPath = "a";
  };
  srcOnlyB = {
    outPath = "b";
  };

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
    expr = mkSrcDigest srcA;
    expected = digest "src-a";
  };
  # A single source equals the singleton list of that source.
  testSrcDigestSingleEqualsSingleton = {
    expr = mkSrcDigest srcA == mkSrcDigest [ srcA ];
    expected = true;
  };
  # Multiple sources are joined with ":" before hashing.
  # The ":" separator is an intentional format pin (matches the bare-hash form).
  testSrcDigestListConcat = {
    expr = mkSrcDigest [
      srcOnlyA
      srcOnlyB
    ];
    expected = digest "a:b";
  };
  # Order-sensitive (callers pass a stable, ordered list).
  testSrcDigestOrderSensitive = {
    expr =
      mkSrcDigest [
        srcOnlyA
        srcOnlyB
      ] != mkSrcDigest [
        srcOnlyB
        srcOnlyA
      ];
    expected = true;
  };
  # Content change changes the digest.
  testSrcDigestTracksContent = {
    expr = mkSrcDigest srcOnlyA != mkSrcDigest srcOnlyB;
    expected = true;
  };

  # A context-free bare string is rejected: it is not a store reference, so it
  # would silently drop content tracking (bead pg2-6mrm7).
  testSrcDigestThrowsOnBareString = {
    expr = (builtins.tryEval (mkSrcDigest "src-a")).success;
    expected = false;
  };
  # A store-path STRING with context (as produced by builtins.path or by
  # coercing a path) IS accepted, and equals the digest of the path it came from
  # — this is the endorsed builtins.path pattern in lib/claude-marketplace.nix.
  testSrcDigestAcceptsStorePathString = {
    expr = mkSrcDigest "${./fixtures/digest-src}" == mkSrcDigest ./fixtures/digest-src;
    expected = true;
  };
  # A raw path source is accepted (the isPath branch) and yields an 8-char digest.
  testSrcDigestAcceptsPath = {
    expr = builtins.stringLength (mkSrcDigest ./fixtures/digest-src);
    expected = 8;
  };
  # Path stability: the digest of a path derives ONLY from that path's own store
  # representation, so it is a pure function of the source content — it does not
  # pick up any flake-level hash (bead pg2-6mrm7).
  testSrcDigestPathStableFromContentOnly = {
    expr = mkSrcDigest ./fixtures/digest-src == digest "${./fixtures/digest-src}";
    expected = true;
  };
}
