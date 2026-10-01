package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0cv/herdr-mobile-relay/internal/herdr"
)

func TestAgentNotReadyOutcomeDependsOnOperation(t *testing.T) {
	for _, action := range []string{"submit_prompt", "agent_start"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			record := filepath.Join(dir, "calls")
			t.Setenv("HERDR_TEST_ARGS", record)
			bin := writeScript(t, dir, "herdr", "#!/bin/sh\n"+
				"printf '%s\\n' \"$*\" >> \"$HERDR_TEST_ARGS\"\n"+
				"if [ \"$1 $2\" = \"agent start\" ]; then\n"+
				"  printf '%s' '{\"error\":{\"code\":\"agent_not_ready\",\"message\":\"agent reviewer is blocked during startup and is not ready for prompts\"}}' >&2\n"+
				"  exit 1\n"+
				"fi\n"+
				"if [ \"$1 $2\" = \"agent prompt\" ]; then\n"+
				"  printf '%s' '{\"error\":{\"code\":\"agent_not_ready\",\"message\":\"agent pane-1 is not an active named agent\"}}' >&2\n"+
				"  exit 1\n"+
				"fi\n")
			client := herdr.NewClient(bin, filepath.Join(dir, "herdr.sock"))
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			wantPhase := "not_started"
			wantMessage := "Agent is not ready to receive prompts; review the pane before retrying"
			if action == "agent_start" {
				lifecycle := &Lifecycle{herdr: client}
				err = lifecycle.startKindAgent(ctx, "claude", "reviewer", "pane-1")
				wantPhase = "dispatched_unknown"
				wantMessage = "Command may have executed; review the agent before retrying"
			} else {
				err = client.Prompt(ctx, "pane-1", "hello")
			}
			if err == nil {
				t.Fatal("expected agent_not_ready error")
			}
			if !errors.Is(err, herdr.ErrDispatchedUnknown) || errors.Is(err, herdr.ErrNotStarted) {
				t.Fatalf("error = %v, want started subprocess boundary", err)
			}
			d := &Dispatcher{logger: testLogger()}
			result := d.failErr("request-1", action, "pane-1", err)
			if result.OK || result.Phase != wantPhase || result.Error != wantMessage {
				t.Fatalf("result = %+v, want phase %q and message %q", result, wantPhase, wantMessage)
			}
			data, _ := result.Data.(map[string]any)
			if action == "submit_prompt" {
				if data["code"] != "agent_not_ready" || data["dispatched_unknown"] == true {
					t.Fatalf("data = %+v, want prompt refusal without uncertain dispatch", data)
				}
				partial := d.failErr("request-2", action, "pane-1", partiallyApplied("earlier input was delivered", err))
				partialData, _ := partial.Data.(map[string]any)
				if partial.Phase != "dispatched_unknown" || partialData["dispatched_unknown"] != true {
					t.Fatalf("result = %+v, want partial input to outrank prompt refusal", partial)
				}
			} else if data["dispatched_unknown"] != true || data["code"] != nil {
				t.Fatalf("data = %+v, want startup retry guard rather than prompt refusal", data)
			}
			calls, readErr := os.ReadFile(record)
			if readErr != nil {
				t.Fatalf("read Herdr invocations: %v", readErr)
			}
			if count := strings.Count(string(calls), "\n"); count != 1 {
				t.Fatalf("Herdr invocations = %q, want one dispatch without fallback or retry", calls)
			}
		})
	}
}

// A pane whose agent label was only reported through `pane report-agent` is
// never accepted by `agent prompt`; the dispatcher must deliver the prompt on
// the raw pane surface (text, then Enter) instead of dropping it.
func TestPromptFallsBackToPaneForUnsupportedAgent(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "calls")
	t.Setenv("HERDR_TEST_ARGS", record)
	bin := writeScript(t, dir, "herdr", "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> \"$HERDR_TEST_ARGS\"\n"+
		"if [ \"$1 $2\" = \"agent prompt\" ]; then\n"+
		"  printf '%s' '{\"error\":{\"code\":\"agent_not_ready\",\"message\":\"agent w1:p1 is not an active named agent\"}}' >&2\n"+
		"  exit 1\n"+
		"fi\n"+
		"if [ \"$1 $2\" = \"agent explain\" ]; then\n"+
		"  printf '%s' '{\"error\":{\"code\":\"agent_explain_unavailable\",\"message\":\"agent target w1:p1 does not have a detected agent label\"}}' >&2\n"+
		"  exit 1\n"+
		"fi\n")
	state := NewState(testLogger())
	state.CommitInventory([]*AgentState{{PaneID: "w1:p1", Status: "working"}}, state.RevisionCounter())
	d := NewDispatcher(herdr.NewClient(bin, filepath.Join(dir, "herdr.sock")), state, nil, testLogger())
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	result := d.Handle(context.Background(), map[string]any{
		"action": "submit_prompt", "request_id": "fallback", "pane_id": "w1:p1", "text": "hello",
	})
	if !result.OK || result.Phase != "completed" {
		t.Fatalf("result = %+v, want completed fallback delivery", result)
	}
	calls, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read Herdr invocations: %v", err)
	}
	got := string(calls)
	for _, want := range []string{
		"agent prompt w1:p1 hello",
		"agent explain w1:p1",
		"pane send-text w1:p1 hello",
		"pane send-keys w1:p1 Enter",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Herdr invocations = %q, want %q", got, want)
		}
	}
}

// The same refusal on a pane Herdr can still explain (a detected agent sits
// behind the label) keeps the refusal: no raw input is written.
func TestPromptRefusalWithoutFallbackForDetectedAgent(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "calls")
	t.Setenv("HERDR_TEST_ARGS", record)
	bin := writeScript(t, dir, "herdr", "#!/bin/sh\n"+
		"printf '%s\\n' \"$*\" >> \"$HERDR_TEST_ARGS\"\n"+
		"if [ \"$1 $2\" = \"agent prompt\" ]; then\n"+
		"  printf '%s' '{\"error\":{\"code\":\"agent_not_ready\",\"message\":\"agent w1:p1 is not an active named agent\"}}' >&2\n"+
		"  exit 1\n"+
		"fi\n"+
		"if [ \"$1 $2\" = \"agent explain\" ]; then\n"+
		"  printf '%s' '{\"result\":{\"agent\":\"claude\",\"state\":\"idle\"}}'\n"+
		"  exit 0\n"+
		"fi\n")
	state := NewState(testLogger())
	state.CommitInventory([]*AgentState{{PaneID: "w1:p1", Status: "working"}}, state.RevisionCounter())
	d := NewDispatcher(herdr.NewClient(bin, filepath.Join(dir, "herdr.sock")), state, nil, testLogger())
	t.Cleanup(func() { _ = d.Close(context.Background()) })

	result := d.Handle(context.Background(), map[string]any{
		"action": "submit_prompt", "request_id": "refusal", "pane_id": "w1:p1", "text": "hello",
	})
	if result.OK || result.Phase != "not_started" ||
		result.Error != "Agent is not ready to receive prompts; review the pane before retrying" {
		t.Fatalf("result = %+v, want the unchanged not-ready refusal", result)
	}
	calls, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read Herdr invocations: %v", err)
	}
	if strings.Contains(string(calls), "pane send") {
		t.Fatalf("fallback wrote input for a detected agent: %q", calls)
	}
}
