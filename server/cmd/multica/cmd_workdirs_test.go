package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/spf13/cobra"
)

func TestWorkdirCLIRejectsManagedTaskAndImplicitProfileAccess(t *testing.T) {
	t.Setenv(cli.TaskConfigRootEnv, t.TempDir())
	command := &cobra.Command{}
	command.Flags().Bool("all-profiles", true, "")
	if err := runDaemonWorkdirs(command, nil); err == nil {
		t.Fatal("managed task accessed owner profiles")
	}
	t.Setenv(cli.TaskConfigRootEnv, "")
	command = &cobra.Command{}
	command.Flags().Bool("all-profiles", false, "")
	if err := runDaemonWorkdirs(command, nil); err == nil {
		t.Fatal("implicit all-profile access accepted")
	}
}

func TestWorkdirCLIUsesAuthenticatedLocalEndpointWithoutRedirects(t *testing.T) {
	// The task config root isolates profile state without reading user credentials.
	t.Setenv(cli.TaskConfigRootEnv, t.TempDir())
	const profile = "workdir-test"
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(daemon.HealthResponse{Status: "running", PID: os.Getpid(), Profile: profile})
	})
	mux.HandleFunc("/workdirs", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing profile authentication")
		}
		var request daemon.WorkdirRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Profile != profile || request.Root != "/managed" {
			t.Errorf("scope not sent: %+v", request)
		}
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	_, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishDaemonPort(profile, daemonPortRecord{Port: port, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	var result daemon.WorkdirActionResult
	err = callWorkdirDaemon(context.Background(), profile, "test-token", daemon.WorkdirRequest{Profile: profile, Root: "/managed", Action: "reclaim"}, &result)
	if err == nil || redirected {
		t.Fatalf("redirect handling: error=%v forwarded=%v", err, redirected)
	}
}
