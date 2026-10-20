package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/spf13/cobra"
)

const workdirProbeTimeout = 2 * time.Second
const workdirResponseLimit = 32 << 20

// workdirCommandResponse is the desktop IPC contract, containing either an
// inventory or completed cleanup results across explicitly selected profiles.
type workdirCommandResponse struct {
	Operations []daemon.WorkdirJob        `json:"operations"`
	Reports    []daemon.WorkdirReport     `json:"reports"`
	Result     daemon.WorkdirActionResult `json:"result"`
}

var daemonWorkdirsCmd = &cobra.Command{
	Use: "workdirs", Short: "Manage local work directories", Args: cobra.NoArgs,
	RunE: runDaemonWorkdirs,
}

func init() {
	flags := daemonWorkdirsCmd.Flags()
	flags.Bool("all-profiles", false, "Explicitly inspect all local daemon profiles")
	flags.String("action", "list", "list, reclaim, delete, start-delete, start-reclaim, or operations")
	flags.String("target-profile", "", "Profile selected for deletion")
	flags.String("id", "", "Opaque directory ID returned by list")
	daemonCmd.AddCommand(daemonWorkdirsCmd)
}

// runDaemonWorkdirs is deliberately unavailable to managed agent executions:
// only the local owner may inspect other profiles or borrow their credentials.
func runDaemonWorkdirs(cmd *cobra.Command, _ []string) error {
	if inDaemonManagedExecutionContext() {
		return errors.New("workdir management is unavailable inside a managed task")
	}
	all, _ := cmd.Flags().GetBool("all-profiles")
	if !all {
		return errors.New("workdir management requires explicit --all-profiles")
	}
	action, _ := cmd.Flags().GetString("action")
	if action != "list" && action != "reclaim" && action != "delete" && action != "start-delete" && action != "start-reclaim" && action != "operations" {
		return errors.New("unknown workdir action")
	}
	target, _ := cmd.Flags().GetString("target-profile")
	id, _ := cmd.Flags().GetString("id")
	if (action == "delete" || action == "start-delete") && id == "" {
		return errors.New("directory ID is required")
	}
	roots, err := enumerateWorkspaceRoots(false)
	if err != nil {
		return err
	}
	response := workdirCommandResponse{Operations: []daemon.WorkdirJob{}, Reports: []daemon.WorkdirReport{}, Result: daemon.WorkdirActionResult{Problems: []daemon.WorkdirResult{}}}
	matched := false
	seen := map[string]bool{}
	for _, root := range roots {
		if (action == "delete" || action == "start-delete") && root.Profile != target {
			continue
		}
		matched = true
		cfg, loadErr := cli.LoadCLIConfigForProfile(root.Profile)
		token := ""
		if loadErr == nil {
			token = cfg.Token
		}
		// A short authenticated probe avoids waiting the full mutation deadline
		// on offline/old daemons. No destructive fallback runs in this CLI process.
		online := workdirDaemonOnline(cmd.Context(), root.Profile)
		request := daemon.WorkdirRequest{Profile: root.Profile, Root: root.Root, Action: action, ID: id}
		if action == "operations" || action == "start-delete" || action == "start-reclaim" {
			var jobs daemon.WorkdirJobResponse
			if !online || token == "" {
				err = errors.New("Start or update this profile's daemon to enable background cleanup")
			} else {
				err = callWorkdirDaemon(cmd.Context(), root.Profile, token, request, &jobs)
				if err == nil && (jobs.Operations == nil || (action != "operations" && len(jobs.Operations) != 1)) {
					err = errors.New("Local daemon did not acknowledge background cleanup; update it before retrying")
				}
			}
			if err != nil {
				if action == "start-delete" {
					return err
				}
				response.Result.Problems = append(response.Result.Problems, daemon.WorkdirResult{Path: root.Root, Reason: err.Error()})
			} else {
				response.Operations = append(response.Operations, jobs.Operations...)
			}
			continue
		}
		if action == "list" {
			var report daemon.WorkdirReport
			var requestErr error
			if online && token != "" {
				requestErr = callWorkdirDaemon(cmd.Context(), root.Profile, token, request, &report)
				if requestErr == nil && (report.Profile != root.Profile || report.Root != root.Root || report.Directories == nil) {
					requestErr = errors.New("Local daemon returned an invalid directory inventory")
				}
			} else {
				requestErr = errors.New("Start or update this profile's daemon to enable cleanup")
			}
			if requestErr != nil {
				report, err = daemon.ScanWorkDirectories(root.Root, root.Profile)
				if err != nil {
					response.Result.Problems = append(response.Result.Problems, daemon.WorkdirResult{Path: root.Root, Reason: err.Error()})
					continue
				}
				if len(report.Directories) > 0 {
					report.Problems = append(report.Problems, daemon.WorkdirResult{Path: root.Root, Reason: requestErr.Error()})
				}
				if token != "" {
					client := daemon.NewClient(resolveDaemonServerURL(cmd, root.Profile))
					client.SetToken(token)
					ctx, cancel := context.WithTimeout(cmd.Context(), workdirProbeTimeout)
					daemon.ResolveWorkDirectoryTasks(ctx, &report, client)
					cancel()
				}
			}
			report.ServerURL = resolveDaemonServerURL(cmd, root.Profile)
			unique := make([]daemon.WorkDirectory, 0, len(report.Directories))
			for _, directory := range report.Directories {
				// A script source can be registered by multiple profiles. Prefer the
				// first entry here; a later online report replaces an offline owner.
				if !seen[directory.Path] {
					seen[directory.Path] = true
					unique = append(unique, directory)
					continue
				}
				if report.Online {
					for i := range response.Reports {
						if response.Reports[i].Online {
							continue
						}
						for j, old := range response.Reports[i].Directories {
							if old.Path == directory.Path {
								response.Reports[i].Directories = append(response.Reports[i].Directories[:j], response.Reports[i].Directories[j+1:]...)
								unique = append(unique, directory)
								break
							}
						}
					}
				}
			}
			report.Directories = unique
			response.Reports = append(response.Reports, report)
		} else {
			var result daemon.WorkdirActionResult
			if !online || token == "" {
				err = errors.New("Start or update this profile's daemon to enable cleanup")
			} else {
				err = callWorkdirDaemon(cmd.Context(), root.Profile, token, request, &result)
			}
			if err == nil && (action == "delete" || action == "start-delete") && result.Removed != 1 {
				err = errors.New("Local daemon did not confirm directory deletion; refresh before retrying")
			}
			if err != nil {
				if action == "delete" {
					return err
				}
				response.Result.Problems = append(response.Result.Problems, daemon.WorkdirResult{Path: root.Root, Reason: err.Error()})
				continue
			}
			response.Result.Removed += result.Removed
			response.Result.Skipped += result.Skipped
			response.Result.ReclaimedBytes += result.ReclaimedBytes
			response.Result.Problems = append(response.Result.Problems, result.Problems...)
		}
	}
	if !matched {
		return errors.New("selected profile no longer exists")
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}

// workdirDaemonOnline probes only the selected local profile; the authenticated
// operation additionally verifies profile/root identity before touching files.
func workdirDaemonOnline(ctx context.Context, profile string) bool {
	ctx, cancel := context.WithTimeout(ctx, workdirProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", resolveHealthPort(profile)), nil)
	if err != nil {
		return false
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var health daemon.HealthResponse
	return response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, workdirResponseLimit)).Decode(&health) == nil && health.Profile == profile
}

// callWorkdirDaemon never follows redirects with a profile credential. Errors
// contain only the local endpoint response, not config data or command args.
func callWorkdirDaemon[T daemon.WorkdirReport | daemon.WorkdirActionResult | daemon.WorkdirJobResponse](ctx context.Context, profile, token string, input daemon.WorkdirRequest, output *T) error {
	timeout := daemon.WorkdirOperationTimeout
	if input.Action == "operations" || input.Action == "start-delete" || input.Action == "start-reclaim" {
		timeout = workdirProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/workdirs", resolveHealthPort(profile)), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("Local daemon is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, workdirRequestErrorLimit))
		return fmt.Errorf("local cleanup rejected (%d): %s", response.StatusCode, message)
	}
	return json.NewDecoder(io.LimitReader(response.Body, workdirResponseLimit)).Decode(output)
}

const workdirRequestErrorLimit = 4096
