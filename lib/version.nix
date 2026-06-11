# Version helper library
# Provides git hash extraction and install-metadata derivation generation.
#
# Version strings are computed at flake-eval time from the source flake's
# `lastModifiedDate`, git revision, and (for dirty trees) `narHash`. See
# mkVersion for the exact format and why the narHash digest is needed.
let
  mkGitHash = gitRev: if gitRev != null then builtins.substring 0 7 gitRev else "dev";

  mkSrcDigest =
    srcs:
    let
      list = if builtins.isList srcs then srcs else [ srcs ];
    in
    builtins.substring 0 8 (
      builtins.hashString "sha256" (builtins.concatStringsSep ":" (map (s: "${s}") list))
    );

  # Format: YYYYMMDD-<7-char-hash> for a clean checkout (e.g. "20260430-abc1234").
  #
  # For a dirty working tree (the common case during local development, where
  # repos are injected via `--override-input <name> git+file://<clone>`), Nix
  # freezes both `rev` and `lastModified` at the HEAD commit and exposes only
  # `dirtyRev` (= "<rev>-dirty"). The clean format above would therefore be
  # IDENTICAL for every uncommitted edit, so `nvd` reports "No version or
  # selection state changes" even though the source rebuilt — losing the
  # per-input change attribution this metadata exists to provide.
  #
  # To fix that, append a short digest of `narHash` — which tracks the actual
  # working-tree content and changes on every edit — whenever the source is not
  # a clean commit. Committing the input collapses the version back to the
  # stable YYYYMMDD-<hash> form (and `narHash` is deterministic, so identical
  # content yields an identical version with no spurious churn).
  mkVersion =
    flakeSelf:
    let
      date = builtins.substring 0 8 (toString flakeSelf.lastModifiedDate);
      hash = mkGitHash (flakeSelf.rev or flakeSelf.dirtyRev or null);
      # `rev` is present only for a clean commit; its absence means a dirty git
      # tree (or a non-git `path:` source).
      isClean = flakeSelf ? rev;
      dirtySuffix =
        if isClean || !(flakeSelf ? narHash) then
          ""
        else
          "-dirty-${builtins.substring 0 8 (builtins.hashString "sha256" flakeSelf.narHash)}";
    in
    "${date}-${hash}${dirtySuffix}";
in
{
  # Extract short git hash from flake self reference
  # Usage: gitHash = mkGitHash (self.rev or self.dirtyRev or null);
  # Returns: 7-character hash string, or "dev" if no git info available
  inherit mkGitHash;

  # Build a version string from a flake self reference.
  # Usage: version = mkVersion self;
  inherit mkVersion;

  # Compute an 8-char digest for one or more store paths (or strings).
  # Each element is coerced to its string representation (store-path coercion
  # for derivations/paths); inputs are expected to be store paths, which
  # contain no ":" themselves.
  # Usage: digest = mkSrcDigest src;           # single path/derivation
  #        digest = mkSrcDigest [ src1 src2 ]; # multiple paths (joined with ":")
  # Returns: first8(sha256(colon-joined string representations))
  # NOTE: this is NOT a NAR content hash; it is a hash of the store-path strings.
  inherit mkSrcDigest;

  # Returns a home-manager module that installs a small JSON metadata file
  # for the given repo. The derivation name embeds the version so nvd shows
  # a version bump on every commit, mirroring what it shows for packages.
  #
  # Output path: $out/share/pn/<name>-install-metadata.json
  # JSON content: { name, version, lastModified }
  #
  # Usage (in a repo's flake.nix, outside eachDefaultSystem):
  #   homeModules.install-metadata =
  #     phillipgreenii-nix-base.lib.mkInstallMetadata { flakeSelf = self; name = "my-repo"; };
  mkInstallMetadata =
    { flakeSelf, name }:
    let
      version = mkVersion flakeSelf;
    in
    { pkgs, ... }:
    {
      home.packages = [
        (pkgs.writeTextFile {
          name = "${name}-install-metadata-${version}";
          destination = "/share/pn/${name}-install-metadata.json";
          text = builtins.toJSON {
            inherit name version;
            lastModified = toString flakeSelf.lastModifiedDate;
          };
        })
      ];
    };
}
