package localgithub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExtractAcrossRepositories covers multiple nested repos and URL deduplication.
func TestExtractAcrossRepositories(t *testing.T) {
	refs := Extract("https://github.com/team/service/pull/42 " +
		"https://github.com/team/web/pull/42 " +
		"https://github.com/TEAM/service/pull/42/files " +
		"https://github.com/team/service/pull/99999999999999999 " +
		"https://example.com/team/service/pull/1")
	if len(refs) != 2 || refs[0].Repo != "service" || refs[1].Repo != "web" || refs[1].Number != 42 {
		t.Fatalf("distinct PRs = %+v", refs)
	}
}

// TestFetch uses an explicit fake CLI; it never resolves the user's gh command.
func TestFetch(t *testing.T) {
	const response = `{"number":42,"html_url":"https://github.com/team/service/pull/42","title":"Delivered fix","state":"closed","merged_at":"2026-10-13T12:00:00Z","created_at":"2026-10-12T12:00:00Z","updated_at":"2026-10-13T12:00:00Z"}`
	executable := filepath.Join(t.TempDir(), "fake-gh")
	script := "#!/bin/sh\n[ \"$1\" = api ] && [ \"$2\" = --hostname ] && [ \"$3\" = github.com ] && [ \"$4\" = repos/team/service/pulls/42 ] || exit 2\nprintf '%s' '" + response + "'\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ref := Extract("https://github.com/team/service/pull/42")[0]
	pr, err := (Client{Executable: executable}).Fetch(context.Background(), ref)
	if err != nil || pr.Status() != "merged" {
		t.Fatalf("fetch = %+v, %v", pr, err)
	}
	ref.Number++
	if _, err := (Client{Executable: executable}).Fetch(context.Background(), ref); err == nil {
		t.Fatal("CLI failure was accepted")
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' '{}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := (Client{Executable: executable}).Fetch(context.Background(), ref); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid metadata error = %v", err)
	}
}

// TestStatus distinguishes an unmerged closure from a successful merge.
func TestStatus(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		pr   PullRequest
		want string
	}{
		{PullRequest{State: "open"}, "open"},
		{PullRequest{State: "open", Draft: true}, "draft"},
		{PullRequest{State: "closed"}, "closed"},
		{PullRequest{State: "closed", MergedAt: &now}, "merged"},
	} {
		if got := test.pr.Status(); got != test.want {
			t.Errorf("status = %s, want %s", got, test.want)
		}
	}
}
