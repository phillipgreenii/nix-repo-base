package jira

import (
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
	Summary   string   `json:"summary"`
	Labels    []string `json:"labels"`
	Created   string   `json:"created"`
	Updated   string   `json:"updated"`
	Status    struct{ Name string } `json:"status"`
	IssueType struct{ Name string } `json:"issuetype"`
	Priority  struct{ Name string } `json:"priority"`
	Project   struct{ Key string } `json:"project"`
	Reporter  *rawUser `json:"reporter"`
	Assignee  *rawUser `json:"assignee"`
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
