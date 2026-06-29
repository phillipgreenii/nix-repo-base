package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client talks to an Atlassian Jira tenant via basic auth. It holds no tenant
// default: BaseURL/Email/Token are supplied by the caller (config).
type Client struct {
	BaseURL string
	Email   string
	Token   string
	HTTP    *http.Client
}

// NewClient constructs a Client. BaseURL trailing slashes are trimmed.
func NewClient(baseURL, email, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Email:   email,
		Token:   token,
		HTTP:    &http.Client{Timeout: defaultTimeout},
	}
}

func (c *Client) basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Email+":"+c.Token))
}

func (c *Client) browseURL(key string) string { return c.BaseURL + "/browse/" + url.PathEscape(key) }

// rawUser is the Atlassian user shape; nil-safe mapping to *User.
type rawUser struct {
	EmailAddress string `json:"emailAddress"`
	AccountID    string `json:"accountId"`
	DisplayName  string `json:"displayName"`
}

func (u *rawUser) toUser() *User {
	if u == nil || (u.EmailAddress == "" && u.AccountID == "" && u.DisplayName == "") {
		return nil
	}
	return &User{Email: u.EmailAddress, AccountID: u.AccountID, DisplayName: u.DisplayName}
}

// rawFields is the subset of Atlassian issue fields we map.
type rawFields struct {
	Summary   string                `json:"summary"`
	Labels    []string              `json:"labels"`
	Created   string                `json:"created"`
	Updated   string                `json:"updated"`
	Status    struct{ Name string } `json:"status"`
	IssueType struct{ Name string } `json:"issuetype"`
	Priority  struct{ Name string } `json:"priority"`
	Project   struct{ Key string }  `json:"project"`
	Reporter  *rawUser              `json:"reporter"`
	Assignee  *rawUser              `json:"assignee"`
}

func (c *Client) mapIssue(key string, f rawFields) Issue {
	labels := f.Labels
	if labels == nil {
		labels = []string{}
	}
	return Issue{
		Key:       key,
		Summary:   f.Summary,
		Status:    f.Status.Name,
		IssueType: f.IssueType.Name,
		Labels:    labels,
		URL:       c.browseURL(key),
		Priority:  f.Priority.Name,
		Project:   f.Project.Key,
		Created:   f.Created,
		Updated:   f.Updated,
		Reporter:  f.Reporter.toUser(),
		Assignee:  f.Assignee.toUser(),
	}
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", c.basicAuth())
	req.Header.Set("Accept", "application/json")
	return c.HTTP.Do(req)
}

// ExpandOpts selects optional per-item enrichment for Search. Changelog maps to
// Atlassian expand=changelog; Comments adds the `comment` FIELD (not a Jira
// expand — sending expand=comments returns nothing).
type ExpandOpts struct {
	Changelog bool
	Comments  bool
}

type rawChangeItem struct {
	Field      string `json:"field"`
	FromString string `json:"fromString"`
	ToString   string `json:"toString"`
}
type rawHistory struct {
	ID      string          `json:"id"`
	Author  rawUser         `json:"author"`
	Created string          `json:"created"`
	Items   []rawChangeItem `json:"items"`
}
type rawComment struct {
	ID      string          `json:"id"`
	Author  rawUser         `json:"author"`
	Created string          `json:"created"`
	Body    json.RawMessage `json:"body"`
}

// searchFields embeds the shared rawFields and adds the comment list, which is
// present only when Search requests the `comment` field. GetIssue never
// requests it, so its embedded rawFields stays comment-free.
type searchFields struct {
	rawFields
	Comment struct {
		Comments []rawComment `json:"comments"`
	} `json:"comment"`
}

// toUserOrEmpty maps a changelog/comment author, returning a non-nil *User even
// when the source fields are empty (changelog/comment authors are values).
func (u *rawUser) toUserOrEmpty() *User {
	if u == nil {
		return &User{}
	}
	if su := u.toUser(); su != nil {
		return su
	}
	return &User{}
}

// Search fetches the first page of a JQL search. It preserves the SP1 contract
// exactly (no nextPageToken sent); for multi-page collection use SearchAll.
func (c *Client) Search(ctx context.Context, jql string, limit int, exp ExpandOpts) (*SearchResult, error) {
	return c.SearchPage(ctx, jql, limit, exp, "")
}

func (c *Client) SearchPage(ctx context.Context, jql string, limit int, exp ExpandOpts, pageToken string) (*SearchResult, error) {
	if strings.TrimSpace(jql) == "" {
		return nil, fmt.Errorf("jira: empty jql")
	}
	fields := []string{"summary", "status", "issuetype", "labels", "priority", "project", "created", "updated", "reporter", "assignee"}
	if exp.Comments {
		fields = append(fields, "comment")
	}
	body := map[string]any{"jql": jql, "maxResults": limit, "fields": fields}
	if pageToken != "" {
		body["nextPageToken"] = pageToken
	}
	if exp.Changelog {
		body["expand"] = "changelog"
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/rest/api/3/search/jql", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("jira: search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jira: search: status %s", resp.Status)
	}
	var raw struct {
		Issues []struct {
			Key       string       `json:"key"`
			Fields    searchFields `json:"fields"`
			Changelog struct {
				Histories []rawHistory `json:"histories"`
			} `json:"changelog"`
		} `json:"issues"`
		NextPageToken string `json:"nextPageToken"`
		IsLast        *bool  `json:"isLast"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("jira: search: decode: %w", err)
	}
	items := make([]Issue, 0, len(raw.Issues))
	for _, is := range raw.Issues {
		if is.Key == "" {
			return nil, fmt.Errorf("jira: search: issue missing key")
		}
		iss := c.mapIssue(is.Key, is.Fields.rawFields)
		if exp.Changelog {
			for _, h := range is.Changelog.Histories {
				for _, it := range h.Items {
					if it.Field != "status" {
						continue
					}
					iss.Changelog = append(iss.Changelog, ChangelogEntry{
						ID:    h.ID,
						Field: it.Field, From: it.FromString, To: it.ToString,
						Author: *h.Author.toUserOrEmpty(), At: h.Created,
					})
				}
			}
		}
		if exp.Comments {
			for _, cm := range is.Fields.Comment.Comments {
				iss.Comments = append(iss.Comments, Comment{
					ID:     cm.ID,
					Author: *cm.Author.toUserOrEmpty(), Body: FlattenADF(cm.Body), Created: cm.Created,
				})
			}
		}
		items = append(items, iss)
	}
	truncated := raw.NextPageToken != "" || (raw.IsLast != nil && !*raw.IsLast)
	return &SearchResult{Items: items, Truncated: truncated, NextPageToken: raw.NextPageToken}, nil
}

// DefaultMaxSearchPages bounds SearchAll so a runaway query cannot loop forever.
// At Atlassian's ~100-item per-page ceiling this is up to ~10k issues — generous
// for any window-bounded collector.
const DefaultMaxSearchPages = 100

// SearchAll follows nextPageToken across pages, concatenating items, until the
// last page or maxPages is reached (maxPages <= 0 falls back to the default cap).
// On full completion it returns Truncated=false and an empty NextPageToken; on a
// cap-hit it returns Truncated=true and the first unfetched token.
func (c *Client) SearchAll(ctx context.Context, jql string, limit int, exp ExpandOpts, maxPages int) (*SearchResult, error) {
	if maxPages <= 0 {
		maxPages = DefaultMaxSearchPages
	}
	all := make([]Issue, 0)
	token := ""
	for page := 0; page < maxPages; page++ {
		res, err := c.SearchPage(ctx, jql, limit, exp, token)
		if err != nil {
			return nil, err
		}
		all = append(all, res.Items...)
		if res.NextPageToken == "" {
			return &SearchResult{Items: all, Truncated: false}, nil
		}
		token = res.NextPageToken
	}
	return &SearchResult{Items: all, Truncated: true, NextPageToken: token}, nil
}

// GetIssue fetches one issue via GET /rest/api/3/issue/<key>.
func (c *Client) GetIssue(ctx context.Context, key string) (*Issue, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("jira: empty issue key")
	}
	endpoint := c.BaseURL + "/rest/api/3/issue/" + url.PathEscape(key) +
		"?fields=summary,status,issuetype,labels,priority,project,created,updated,reporter,assignee"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("jira: get issue %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("jira: issue %s not found", key)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("jira: get issue %s: status %s", key, resp.Status)
	}
	var raw struct {
		Key    string    `json:"key"`
		Fields rawFields `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("jira: decode issue %s: %w", key, err)
	}
	iss := c.mapIssue(raw.Key, raw.Fields)
	return &iss, nil
}

// AuthStatus performs a live credential check via GET /rest/api/3/myself.
// 401 -> Unauthenticated (Atlassian returns 401 for both invalid and expired
// tokens, so there is deliberately no EXPIRED state), 403 -> Forbidden,
// 2xx -> OK, anything else (incl. transport error) -> Error.
func (c *Client) AuthStatus(ctx context.Context) (AuthState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return AuthError, err
	}
	resp, err := c.do(req)
	if err != nil {
		return AuthError, nil
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode/100 == 2:
		return AuthOK, nil
	case resp.StatusCode == http.StatusUnauthorized:
		return AuthUnauthenticated, nil
	case resp.StatusCode == http.StatusForbidden:
		return AuthForbidden, nil
	default:
		return AuthError, nil
	}
}
