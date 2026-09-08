package pjira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
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
	Duedate   *string               `json:"duedate"`
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
		Duedate:   f.Duedate,
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
		return nil, fmt.Errorf("pjira: empty jql")
	}
	fields := []string{"summary", "status", "issuetype", "labels", "priority", "project", "created", "updated", "duedate", "reporter", "assignee"}
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
		return nil, fmt.Errorf("pjira: search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("pjira: search: status %s", resp.Status)
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
		return nil, fmt.Errorf("pjira: search: decode: %w", err)
	}
	items := make([]Issue, 0, len(raw.Issues))
	for _, is := range raw.Issues {
		if is.Key == "" {
			return nil, fmt.Errorf("pjira: search: issue missing key")
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
		return nil, fmt.Errorf("pjira: empty issue key")
	}
	endpoint := c.BaseURL + "/rest/api/3/issue/" + url.PathEscape(key) +
		"?fields=summary,status,issuetype,labels,priority,project,created,updated,duedate,reporter,assignee"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("pjira: get issue %s: %w", key, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("pjira: issue %s not found", key)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("pjira: get issue %s: status %s", key, resp.Status)
	}
	var raw struct {
		Key    string    `json:"key"`
		Fields rawFields `json:"fields"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("pjira: decode issue %s: %w", key, err)
	}
	iss := c.mapIssue(raw.Key, raw.Fields)
	return &iss, nil
}

// rawErrorBody is Atlassian's validation-error shape returned by write
// endpoints (POST/PUT) on failure: top-level errorMessages plus a per-field
// errors map. CreateIssue decodes this so an invalid project/issue-type is
// diagnosable in the returned error, not swallowed behind a bare status code.
type rawErrorBody struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

// decodeJiraError best-effort decodes an Atlassian error body into a
// ": "-prefixed detail suffix; it returns "" when the body doesn't parse as
// one (or carries no messages), so callers can always append its result.
func decodeJiraError(body io.Reader) string {
	var e rawErrorBody
	if err := json.NewDecoder(body).Decode(&e); err != nil {
		return ""
	}
	parts := append([]string{}, e.ErrorMessages...)
	keys := make([]string, 0, len(e.Errors))
	for k := range e.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+": "+e.Errors[k])
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, "; ")
}

// CreateIssue creates a new issue via POST /rest/api/3/issue and returns its
// key and browse URL. A non-empty Description is encoded to ADF (EncodeADFText)
// before being sent; an empty one omits the description field entirely rather
// than sending an empty document.
func (c *Client) CreateIssue(ctx context.Context, req CreateIssueRequest) (*CreateIssueResult, error) {
	project := strings.TrimSpace(req.Project)
	issueType := strings.TrimSpace(req.IssueType)
	summary := strings.TrimSpace(req.Summary)
	if project == "" {
		return nil, fmt.Errorf("pjira: empty project")
	}
	if issueType == "" {
		return nil, fmt.Errorf("pjira: empty issue type")
	}
	if summary == "" {
		return nil, fmt.Errorf("pjira: empty summary")
	}
	fields := map[string]any{
		"project":   map[string]string{"key": project},
		"issuetype": map[string]string{"name": issueType},
		"summary":   summary,
	}
	if desc := strings.TrimSpace(req.Description); desc != "" {
		fields["description"] = EncodeADFText(desc)
	}
	reqBody, err := json.Marshal(map[string]any{"fields": fields})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/rest/api/3/issue", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("pjira: create issue: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("pjira: create issue: unauthenticated")
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("pjira: create issue: status %s%s", resp.Status, decodeJiraError(resp.Body))
	}
	var raw struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("pjira: create issue: decode: %w", err)
	}
	if raw.Key == "" {
		return nil, fmt.Errorf("pjira: create issue: response missing key")
	}
	return &CreateIssueResult{Key: raw.Key, URL: c.browseURL(raw.Key)}, nil
}

// rawTransition is one available transition from
// GET /rest/api/3/issue/{key}/transitions -- the id to POST back, plus the
// destination status name (`to.name`) that Transition matches its `to`
// argument against.
type rawTransition struct {
	ID string `json:"id"`
	To struct {
		Name string `json:"name"`
	} `json:"to"`
}

// Transition moves an issue to a target workflow state by name, via the
// standard two-step Jira Cloud transition dance: GET
// /rest/api/3/issue/{key}/transitions resolves the target state name to a
// transition id (matched case-insensitively against each candidate's
// destination status name), then POST the same endpoint with
// {"transition":{"id":...}} executes it. A target state that matches no
// available transition is a distinct, clearly-classified error -- never
// folded into the issue's own "not found" classification, which is returned
// separately (and first, since resolving transitions requires the issue to
// exist).
func (c *Client) Transition(ctx context.Context, key, to string) (*TransitionResult, error) {
	key = strings.TrimSpace(key)
	to = strings.TrimSpace(to)
	if key == "" {
		return nil, fmt.Errorf("pjira: empty issue key")
	}
	if to == "" {
		return nil, fmt.Errorf("pjira: empty target state")
	}
	endpoint := c.BaseURL + "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"

	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	getResp, err := c.do(getReq)
	if err != nil {
		return nil, fmt.Errorf("pjira: transition %s: list transitions: %w", key, err)
	}
	defer func() { _ = getResp.Body.Close() }()
	if getResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("pjira: issue %s not found", key)
	}
	if getResp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("pjira: transition %s: unauthenticated", key)
	}
	if getResp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("pjira: transition %s: list transitions: status %s%s", key, getResp.Status, decodeJiraError(getResp.Body))
	}
	var raw struct {
		Transitions []rawTransition `json:"transitions"`
	}
	if err := json.NewDecoder(getResp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("pjira: transition %s: decode transitions: %w", key, err)
	}
	id := ""
	for _, t := range raw.Transitions {
		if strings.EqualFold(t.To.Name, to) {
			id = t.ID
			break
		}
	}
	if id == "" {
		return nil, fmt.Errorf("pjira: transition %s: no transition to state %q available", key, to)
	}

	reqBody, err := json.Marshal(map[string]any{"transition": map[string]string{"id": id}})
	if err != nil {
		return nil, err
	}
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := c.do(postReq)
	if err != nil {
		return nil, fmt.Errorf("pjira: transition %s: %w", key, err)
	}
	defer func() { _ = postResp.Body.Close() }()
	if postResp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("pjira: issue %s not found", key)
	}
	if postResp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("pjira: transition %s: unauthenticated", key)
	}
	if postResp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("pjira: transition %s: status %s%s", key, postResp.Status, decodeJiraError(postResp.Body))
	}
	return &TransitionResult{Key: key, To: to}, nil
}

// AuthStatus performs a live credential check via GET /rest/api/3/myself.
// 401 -> Unauthenticated (Atlassian returns 401 for both invalid and expired
// tokens, so there is deliberately no EXPIRED state), 403 -> Forbidden,
// 2xx -> OK, any non-auth status -> Error. A transport failure also yields
// Error, but the underlying error is now RETURNED rather than discarded (bead
// pg2-yfjm7) so callers can distinguish "server said no" from "couldn't reach
// the server" and surface the cause.
func (c *Client) AuthStatus(ctx context.Context) (AuthState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return AuthError, err
	}
	resp, err := c.do(req)
	if err != nil {
		return AuthError, err
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
