# Version helper library
# Provides git hash extraction and install-metadata derivation generation.
#
# The actual version string (yy.mm.dd.seconds-hash) is computed at build time
# using the `date` command, which correctly handles all date/time complexity.
let
  mkGitHash = gitRev: if gitRev != null then builtins.substring 0 7 gitRev else "dev";

  # Format: YYYYMMDD-<7-char-hash>  (e.g. "20260430-abc1234")
  mkVersion =
    flakeSelf:
    let
      date = builtins.substring 0 8 (toString flakeSelf.lastModifiedDate);
      hash = mkGitHash (flakeSelf.rev or flakeSelf.dirtyRev or null);
    in
    "${date}-${hash}";
in
{
  # Extract short git hash from flake self reference
  # Usage: gitHash = mkGitHash (self.rev or self.dirtyRev or null);
  # Returns: 7-character hash string, or "dev" if no git info available
  inherit mkGitHash;

  # Build a version string from a flake self reference.
  # Usage: version = mkVersion self;
  inherit mkVersion;

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
