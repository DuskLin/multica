package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/localgithub"
)

const (
	prSyncTestIssue  = "01900000-0000-7000-8000-000000000001"
	prSyncOtherIssue = "01900000-0000-7000-8000-000000000002"
)

func prSyncTestPage() localgithub.SyncPage {
	return localgithub.SyncPage{Issues: []localgithub.SyncIssue{{
		IssueID: prSyncTestIssue, Revision: 1, TaskCount: 2,
		References: localgithub.Extract("https://github.com/team/service/pull/1 https://github.com/team/web/pull/2"),
	}}}
}

func TestPRSyncCompleteReportsAndRetry(t *testing.T) {
	for _, scenario := range []string{"success", "unreadable", "report fails", "unauthorized", "unsupported", "invalid response", "pagination"} {
		t.Run(scenario, func(t *testing.T) {
			reads, reports, pages := 0, 0, 0
			fail := true
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fake-token" {
					t.Error("missing daemon authentication")
				}
				if r.URL.Path != "/api/daemon/runtimes/fake-runtime/pull-requests/sync" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Method == http.MethodGet {
					pages++
					if scenario == "unauthorized" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if scenario == "unsupported" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					if scenario == "invalid response" {
						io.WriteString(w, `{}`)
						return
					}
					page := prSyncTestPage()
					if scenario == "pagination" {
						if r.URL.Query().Get("after") == "" {
							page.NextCursor = prSyncTestIssue
						} else {
							page.Issues[0].IssueID = prSyncOtherIssue
						}
					}
					json.NewEncoder(w).Encode(page)
					return
				}
				reports++
				var report localgithub.SyncReport
				if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
					t.Error(err)
				}
				if len(report.PullRequests) != 2 {
					t.Error("partial PR delivery submitted")
				}
				if scenario == "report fails" && fail {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client := NewClient(server.URL)
			client.SetToken("fake-token")
			fetch := func(_ context.Context, ref localgithub.Reference) (localgithub.PullRequest, error) {
				reads++
				if scenario == "unreadable" && fail && ref.Repo == "web" {
					return localgithub.PullRequest{}, errors.New("fake gh permission failure")
				}
				return localgithub.PullRequest{URL: ref.URL, Number: ref.Number}, nil
			}
			cursor := ""
			err := syncPRDeliveries(context.Background(), client, "fake-runtime", &cursor, fetch)
			wantError := scenario != "success" && scenario != "pagination"
			if (err != nil) != wantError {
				t.Fatalf("sync error = %v", err)
			}
			if scenario == "unauthorized" || scenario == "unsupported" || scenario == "invalid response" {
				if reads != 0 || reports != 0 {
					t.Fatal("read GitHub despite rejected candidate response")
				}
				return
			}
			if reads != 2 {
				t.Fatalf("GitHub reads = %d; shared PRs should be cached", reads)
			}
			if scenario == "unreadable" && reports != 0 {
				t.Fatal("uploaded incomplete delivery")
			}
			if scenario == "pagination" && (pages != 2 || reports != 2) {
				t.Fatalf("pages=%d reports=%d", pages, reports)
			}
			if scenario == "unreadable" || scenario == "report fails" {
				fail = false
				if err := syncPRDeliveries(context.Background(), client, "fake-runtime", &cursor, fetch); err != nil {
					t.Fatal(err)
				}
				if reads != 4 {
					t.Fatal("retry reused stale PR snapshots")
				}
			}
		})
	}
}

func TestPRSyncRejectsMalformedCandidates(t *testing.T) {
	for _, scenario := range []string{"missing list", "invalid issue", "invalid cursor", "wrong repo", "duplicate PR", "unexpected host", "no tasks", "duplicate issue", "too many PRs"} {
		t.Run(scenario, func(t *testing.T) {
			page := prSyncTestPage()
			switch scenario {
			case "missing list":
				page.Issues = nil
			case "invalid issue":
				page.Issues[0].IssueID = "bad"
			case "invalid cursor":
				page.NextCursor = "bad"
			case "wrong repo":
				page.Issues[0].References[0].Repo = "private"
			case "duplicate PR":
				page.Issues[0].References[1] = page.Issues[0].References[0]
			case "unexpected host":
				page.Issues[0].References[0].URL = "https://evil.example/team/repo/pull/1"
			case "no tasks":
				page.Issues[0].TaskCount = 0
			case "duplicate issue":
				page.Issues = append(page.Issues, page.Issues[0])
			case "too many PRs":
				page.Issues[0].References = make([]localgithub.Reference, localgithub.MaxSyncPullRequests+1)
			}
			if err := validatePRSyncPage(page); err == nil {
				t.Fatal("accepted malformed candidate")
			}
		})
	}
}

func TestPRSyncExplicitWorkspaceOnly(t *testing.T) {
	d := &Daemon{cfg: Config{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Disabled must return immediately without HTTP, executable lookup, or credentials.
	d.prSyncLoop(context.Background())
	d.cfg.PRSyncWorkspaceID = prSyncTestIssue
	d.workspaces = map[string]*workspaceState{prSyncOtherIssue: {runtimeIDs: []string{"other-runtime"}}}
	if d.prSyncRuntimeID() != "" {
		t.Fatal("selected another workspace runtime")
	}
	d.workspaces[prSyncTestIssue] = &workspaceState{runtimeIDs: []string{"own-runtime"}}
	if d.prSyncRuntimeID() != "own-runtime" {
		t.Fatal("did not select opted-in runtime")
	}
}

func TestPRSyncConfigPrecedence(t *testing.T) {
	t.Setenv("MULTICA_DAEMON_PR_SYNC_WORKSPACE_ID", prSyncTestIssue)
	t.Setenv("MULTICA_DAEMON_PR_SYNC_GH_PATH", "/fake/gh")
	workspace, executable, err := resolvePRSyncConfig(prSyncOtherIssue, "/unused/gh")
	if err != nil || workspace != prSyncTestIssue || executable != "/fake/gh" {
		t.Fatalf("config=%s %s %v", workspace, executable, err)
	}
	t.Setenv("MULTICA_DAEMON_PR_SYNC_WORKSPACE_ID", "")
	workspace, _, err = resolvePRSyncConfig(prSyncTestIssue, "/fake/gh")
	if err != nil || workspace != "" {
		t.Fatal("empty env did not disable sync")
	}
	t.Setenv("MULTICA_DAEMON_PR_SYNC_WORKSPACE_ID", "invalid")
	if _, _, err = resolvePRSyncConfig("", ""); err == nil {
		t.Fatal("invalid workspace accepted")
	}
}

func TestPRSyncCancellationResumesWithoutUploadingPartialDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(prSyncTestPage())
			return
		}
		reports++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cursor := ""
	err := syncPRDeliveries(ctx, NewClient(server.URL), "runtime", &cursor, func(_ context.Context, ref localgithub.Reference) (localgithub.PullRequest, error) {
		cancel()
		return localgithub.PullRequest{}, context.Canceled
	})
	if !errors.Is(err, context.Canceled) || reports != 0 || cursor != "" {
		t.Fatalf("canceled delivery: error=%v reports=%d cursor=%q", err, reports, cursor)
	}
}

// TestPRSyncBudgetResumesAfterCompletedDelivery uses virtual time, never a real wait.
func TestPRSyncBudgetResumesAfterCompletedDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := NewClient("http://sync.test")
		reports := 0
		client.client.Transport = startTaskTransport(func(req *http.Request) (*http.Response, error) {
			body := ""
			status := http.StatusNoContent
			if req.Method == http.MethodGet {
				page := prSyncTestPage()
				second := page.Issues[0]
				second.IssueID = prSyncOtherIssue
				if req.URL.Query().Get("after") == "" {
					page.Issues = append(page.Issues, second)
				} else {
					if req.URL.Query().Get("after") != prSyncTestIssue {
						t.Fatal("resume skipped unfinished issue")
					}
					page.Issues = []localgithub.SyncIssue{second}
				}
				data, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				body, status = string(data), http.StatusOK
			} else {
				reports++
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		cursor := ""
		slow := true
		fetch := func(_ context.Context, ref localgithub.Reference) (localgithub.PullRequest, error) {
			if slow {
				time.Sleep(prSyncSweepBudget)
				slow = false
			}
			return localgithub.PullRequest{URL: ref.URL}, nil
		}
		if err := syncPRDeliveries(context.Background(), client, "runtime", &cursor, fetch); err != nil {
			t.Fatal(err)
		}
		if reports != 1 || cursor != prSyncTestIssue {
			t.Fatalf("reports=%d cursor=%s", reports, cursor)
		}
		if err := syncPRDeliveries(context.Background(), client, "runtime", &cursor, fetch); err != nil {
			t.Fatal(err)
		}
		if reports != 2 || cursor != "" {
			t.Fatalf("resume reports=%d cursor=%s", reports, cursor)
		}
	})
}

// TestPRSyncHealth never mistakes an unstarted or disabled loop for a successful scan.
func TestPRSyncHealth(t *testing.T) {
	d := &Daemon{}
	if got := d.prSyncSnapshot(); got.Phase != "disabled" {
		t.Fatalf("disabled: %+v", got)
	}
	d.cfg.PRSyncWorkspaceID = prSyncTestIssue
	if got := d.prSyncSnapshot(); got.Phase != "waiting" {
		t.Fatalf("waiting: %+v", got)
	}
	for _, phase := range []prSyncPhase{prSyncSyncing, prSyncError, prSyncIdle} {
		d.setPRSyncPhase(phase)
		if got := d.prSyncSnapshot(); got.Phase != phase || got.WorkspaceID != prSyncTestIssue {
			t.Fatalf("phase %s: %+v", phase, got)
		}
	}
	t.Setenv("MULTICA_DAEMON_PR_SYNC_WORKSPACE_ID", "")
	if got := d.prSyncSnapshot(); !got.EnvironmentManaged {
		t.Fatal("explicit empty environment override must lock desktop controls")
	}
}

// TestPRSyncRestartFencesClaims validates the final admission boundary, not a sampled health count.
func TestPRSyncRestartFencesClaims(t *testing.T) {
	for _, scenario := range []string{"idle", "active task", "in-flight claim", "updating", "unready", "no token", "browser", "wrong profile"} {
		t.Run(scenario, func(t *testing.T) {
			d := &Daemon{cfg: Config{Profile: "desktop-test"}, client: NewClient("http://sync.test"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			d.client.SetToken("fake-token")
			d.ready.Store(true)
			cancelled := false
			d.cancelFunc = func() { cancelled = true }
			request := httptest.NewRequest(http.MethodPost, "/pr-sync/restart", nil)
			request.Header.Set("Authorization", "Bearer fake-token")
			request.Header.Set("X-Multica-Profile", d.cfg.Profile)
			switch scenario {
			case "active task":
				d.activeTasks.Store(1)
			case "in-flight claim":
				d.claimsInFlight = 1
			case "updating":
				d.updating.Store(true)
			case "unready":
				d.ready.Store(false)
			case "no token":
				request.Header.Del("Authorization")
			case "browser":
				request.Header.Set("Origin", "https://foreign.test")
			case "wrong profile":
				request.Header.Set("X-Multica-Profile", "another-profile")
			}
			recorder := httptest.NewRecorder()
			d.prSyncRestartHandler().ServeHTTP(recorder, request)
			if scenario == "idle" {
				if recorder.Code != http.StatusAccepted || !cancelled || !d.pauseClaims || d.RestartBinary() == "" {
					t.Fatalf("restart not fenced: status=%d cancelled=%v paused=%v", recorder.Code, cancelled, d.pauseClaims)
				}
			} else if recorder.Code < http.StatusBadRequest || cancelled || d.pauseClaims {
				t.Fatalf("unsafe restart in %s: status=%d cancelled=%v paused=%v", scenario, recorder.Code, cancelled, d.pauseClaims)
			}
		})
	}
}
