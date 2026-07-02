// Package pjira is a generic, tenant-agnostic Atlassian Jira client + model.
// It MUST NOT import any pg-pr package, hard-code any tenant string, or run
// any OS-specific command (see the package guardrails test).
package pjira

// AuthState is the result of an auth-status check.
type AuthState string

const (
	AuthOK              AuthState = "OK"
	AuthMissing         AuthState = "MISSING"
	AuthUnauthenticated AuthState = "UNAUTHENTICATED"
	AuthForbidden       AuthState = "FORBIDDEN"
	AuthError           AuthState = "ERROR"
)

// User is a normalized Atlassian user (reporter, assignee, comment/changelog author).
type User struct {
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

// ChangelogEntry is one status transition extracted from a Jira changelog.
// ID is the Jira changelog-history id (stable per history entry); it lets
// consumers build collision-free per-event identifiers without re-deriving one
// from the timestamp.
type ChangelogEntry struct {
	ID     string `json:"id"`
	Field  string `json:"field"`
	From   string `json:"from"`
	To     string `json:"to"`
	Author User   `json:"author"`
	At     string `json:"at"` // RFC3339
}

// Comment is one issue comment with its body flattened from ADF to plain text.
// ID is the Jira comment id (stable per comment), carried so consumers can build
// collision-free per-event identifiers.
type Comment struct {
	ID      string `json:"id"`
	Author  User   `json:"author"`
	Body    string `json:"body"`
	Created string `json:"created"` // RFC3339
}

// Issue is the unified normalized issue returned by GetIssue and Search.
type Issue struct {
	Key       string           `json:"key"`
	Summary   string           `json:"summary"`
	Status    string           `json:"status"`
	IssueType string           `json:"issuetype"`
	Labels    []string         `json:"labels"`
	URL       string           `json:"url"`
	Priority  string           `json:"priority,omitempty"`
	Project   string           `json:"project,omitempty"`
	Created   string           `json:"created,omitempty"`
	Updated   string           `json:"updated,omitempty"`
	Reporter  *User            `json:"reporter,omitempty"`
	Assignee  *User            `json:"assignee,omitempty"`
	Changelog []ChangelogEntry `json:"changelog,omitempty"`
	Comments  []Comment        `json:"comments,omitempty"`
}

// SearchResult is the search envelope: mapped items, an authoritative truncation
// flag, and the token to fetch the next page (empty when last/complete).
type SearchResult struct {
	Items         []Issue `json:"items"`
	Truncated     bool    `json:"truncated"`
	NextPageToken string  `json:"next_page_token,omitempty"`
}
