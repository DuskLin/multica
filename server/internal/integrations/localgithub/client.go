// Package localgithub reads explicitly delivered GitHub PRs with a local gh login.
package localgithub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout       = 15 * time.Second
	maxSnapshotClockSkew = 5 * time.Minute
)

var pullRequestURL = regexp.MustCompile(`https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[1-9][0-9]*\b`)

// Reference identifies one PR independently of a parent directory or Git HEAD.
type Reference struct {
	URL    string `json:"url"`
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Number int32  `json:"number"`
}

// Extract returns distinct explicit PR URLs in delivery order, across repositories.
func Extract(text string) []Reference {
	refs := make([]Reference, 0)
	seen := make(map[string]bool)
	for _, raw := range pullRequestURL.FindAllString(text, -1) {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
		number, err := strconv.ParseInt(parts[3], 10, 32)
		if err != nil || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			continue
		}
		key := strings.ToLower(raw)
		if !seen[key] {
			seen[key] = true
			refs = append(refs, Reference{URL: raw, Owner: parts[0], Repo: parts[1], Number: int32(number)})
		}
	}
	return refs
}

// PullRequest contains authenticated GitHub metadata, without credentials.
type PullRequest struct {
	Number       int32      `json:"number"`
	URL          string     `json:"html_url"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Draft        bool       `json:"draft"`
	MergedAt     *time.Time `json:"merged_at"`
	ClosedAt     *time.Time `json:"closed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Additions    int32      `json:"additions"`
	Deletions    int32      `json:"deletions"`
	ChangedFiles int32      `json:"changed_files"`
	Head         struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	User struct {
		Login     string `json:"login"`
		AvatarURL string `json:"avatar_url"`
	} `json:"user"`
}

// Status returns the shared PR state, distinguishing closed from merged.
func (pr PullRequest) Status() string {
	if pr.MergedAt != nil {
		return "merged"
	}
	if pr.State == "closed" {
		return "closed"
	}
	if pr.Draft {
		return "draft"
	}
	return "open"
}

// Client runs only the configured GitHub CLI; callers explicitly opt into its login.
type Client struct {
	Executable string
}

// Fetch reads one PR using argument-separated CLI invocation and a bounded deadline.
// It never changes GitHub state, runs a shell, or depends on a Git working directory.
func (c Client) Fetch(ctx context.Context, ref Reference) (PullRequest, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", ref.Owner, ref.Repo, ref.Number)
	data, err := exec.CommandContext(ctx, c.Executable, "api", "--hostname", "github.com", endpoint).Output()
	if err != nil {
		return PullRequest{}, fmt.Errorf("read GitHub PR %s: %w", ref.URL, err)
	}
	var pr PullRequest
	if err := json.Unmarshal(data, &pr); err != nil {
		return PullRequest{}, fmt.Errorf("decode GitHub PR %s: %w", ref.URL, err)
	}
	if err := pr.Validate(ref); err != nil {
		return PullRequest{}, err
	}
	return pr, nil
}

// Validate checks an untrusted snapshot against the server-selected PR identity.
func (pr PullRequest) Validate(ref Reference) error {
	if pr.Number != ref.Number || !strings.EqualFold(pr.URL, ref.URL) || strings.TrimSpace(pr.Title) == "" ||
		pr.CreatedAt.IsZero() || pr.UpdatedAt.Before(pr.CreatedAt) ||
		pr.UpdatedAt.After(time.Now().Add(maxSnapshotClockSkew)) ||
		(pr.State != "open" && pr.State != "closed") ||
		(pr.MergedAt != nil && pr.State != "closed") ||
		(pr.MergedAt != nil && (pr.MergedAt.Before(pr.CreatedAt) || pr.MergedAt.After(pr.UpdatedAt))) ||
		(pr.ClosedAt != nil && (pr.ClosedAt.Before(pr.CreatedAt) || pr.ClosedAt.After(pr.UpdatedAt))) ||
		pr.Additions < 0 || pr.Deletions < 0 || pr.ChangedFiles < 0 {
		return fmt.Errorf("invalid GitHub PR metadata for %s", ref.URL)
	}
	return nil
}
