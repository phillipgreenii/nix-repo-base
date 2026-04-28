# Version helper library
# Provides git hash extraction for build-time version generation
#
# The actual version string (yy.mm.dd.seconds-hash) is computed at build time
# using the `date` command, which correctly handles all date/time complexity.
{
  # Extract short git hash from flake self reference
  # Usage: gitHash = mkGitHash (self.rev or self.dirtyRev or null);
  # Returns: 7-character hash string, or "dev" if no git info available
  mkGitHash = gitRev: if gitRev != null then builtins.substring 0 7 gitRev else "dev";
}
