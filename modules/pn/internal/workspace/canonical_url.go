package workspace

import (
	"strings"
)

// canonicalURL normalizes a remote URL to the form "host/path-without-.git"
// for comparison purposes. It handles all URL forms that appear in real
// workspace configs and Nix flake inputs:
//
//   - github:owner/repo[/branch]       → github.com/owner/repo
//   - https://host/path[.git]           → host/path
//   - ssh://git@host[:port]/path[.git]  → host/path
//   - git@host:path[.git]               → host/path
//   - git+ssh://git@host/path[.git]     → host/path (strip git+ prefix)
//   - git+https://host/path[.git]       → host/path (strip git+ prefix)
//   - path:./... or path:/...           → "" (local; never matches a remote)
//
// The canonical form is lowercase with the scheme, port, leading user@, .git
// suffix, and leading path slash removed.
func canonicalURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	s := rawURL

	// Handle path: scheme — local URLs never match a remote.
	if strings.HasPrefix(s, "path:") {
		return ""
	}

	// Strip git+ prefix from git+ssh:// and git+https://.
	if strings.HasPrefix(s, "git+") {
		s = s[len("git+"):]
	}

	// Handle github: shorthand: "github:owner/repo[/extra]" → "github.com/owner/repo".
	if strings.HasPrefix(s, "github:") {
		spec := strings.TrimPrefix(s, "github:")
		// Drop any trailing /branch component (github:owner/repo/branch form).
		// We want just owner/repo.
		parts := strings.SplitN(spec, "/", 3)
		if len(parts) >= 2 {
			spec = parts[0] + "/" + parts[1]
		}
		return strings.ToLower("github.com/" + spec)
	}

	// Handle ssh:// scheme: ssh://git@host[:port]/path[.git]
	if strings.HasPrefix(s, "ssh://") {
		s = strings.TrimPrefix(s, "ssh://")
		// Strip user@ prefix.
		if idx := strings.Index(s, "@"); idx != -1 {
			s = s[idx+1:]
		}
		// Strip port: host:port/path → host/path.
		s = stripPort(s)
		// Strip leading slash from path (ssh:// has host/path already).
		s = strings.TrimPrefix(s, "/")
		return normalizePath(s)
	}

	// Handle https:// scheme: https://host/path[.git]
	if strings.HasPrefix(s, "https://") {
		s = strings.TrimPrefix(s, "https://")
		return normalizePath(s)
	}

	// Handle git@host:path[.git] (SCP-like syntax).
	if idx := strings.Index(s, "@"); idx != -1 && !strings.Contains(s[:idx], "/") {
		// s is of the form "git@host:path"
		s = s[idx+1:] // drop "git@"
		// Replace the first ":" with "/" to get "host/path".
		s = strings.Replace(s, ":", "/", 1)
		return normalizePath(s)
	}

	// Fallback: return lowercased as-is without .git suffix.
	return normalizePath(s)
}

// normalizePath lowercases the string, strips a trailing .git suffix,
// and strips double .git (e.g. repo.git.git).
func normalizePath(s string) string {
	s = strings.ToLower(s)
	// Strip trailing .git
	s = strings.TrimSuffix(s, ".git")
	// Handle double .git edge case
	s = strings.TrimSuffix(s, ".git")
	// Strip trailing slash.
	s = strings.TrimSuffix(s, "/")
	return s
}

// stripPort removes the ":port" segment from "host:port/path", returning
// "host/path". If there is no port, s is returned unchanged.
func stripPort(s string) string {
	// Find the first colon.
	colonIdx := strings.Index(s, ":")
	if colonIdx == -1 {
		return s
	}
	// Check if what follows the colon (until the next /) is all digits (a port).
	afterColon := s[colonIdx+1:]
	slashIdx := strings.Index(afterColon, "/")
	var portPart string
	if slashIdx == -1 {
		portPart = afterColon
	} else {
		portPart = afterColon[:slashIdx]
	}
	// If portPart is numeric, it's a port — strip it.
	isPort := len(portPart) > 0
	for _, c := range portPart {
		if c < '0' || c > '9' {
			isPort = false
			break
		}
	}
	if isPort {
		host := s[:colonIdx]
		rest := afterColon
		if slashIdx != -1 {
			rest = afterColon[slashIdx:]
		} else {
			rest = ""
		}
		return host + rest
	}
	return s
}

// displayURL returns one URL string for display purposes from a RepoConfig:
//   - If the toml uses the single-url form, return that URL.
//   - Else (multi-remote form), return the origin remote's URL when one
//     exists, otherwise the first remote's URL.
//
// This was previously called canonicalURL; renamed to avoid confusion with the
// new string-normalizer canonicalURL function used by edge discovery.
func displayURL(r RepoConfig) string {
	if r.URL != "" {
		return r.URL
	}
	for _, rm := range r.Remotes {
		if rm.Name == "origin" {
			return rm.URL
		}
	}
	if len(r.Remotes) > 0 {
		return r.Remotes[0].URL
	}
	return ""
}
