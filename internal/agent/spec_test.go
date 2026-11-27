package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/agent/agenttest"
)

// The agent works with what its Agent gives it (E-97): only the tools it
// names and its instructions after the prompt. A name that is no tool of
// the API fails the task instead of leaving the tool out unseen.
func TestAgentSpec(t *testing.T) {
	for _, tc := range []struct {
		tools  []string
		result string
	}{
		{[]string{"getApp", "appLogs"}, "Fine."},
		{[]string{"getApp", "readMinds"}, "tools: the agent's Agent names readMinds, which is no tool of the API"},
	} {
		pending := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: "board-diagnose-1"},
			Spec: cellv1.TaskSpec{Kind: "diagnose", App: "board", Requester: cellv1.Requester{Kind: "Operator", Name: "cell-operator"}}}
		ops := &cellv1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "ops", UID: "ops-1"},
			Spec: cellv1.AgentSpec{Kinds: []string{"diagnose"}, Apps: []string{"board"}, Tools: tc.tools, Instructions: "Answer in one word."}}
		c := newCell(t, func(conv agenttest.Conversation) (agenttest.Turn, error) {
			if err := want(slices.Equal(conv.Tools, []string{"appLogs", "getApp", "answer", "ask"}) && strings.HasSuffix(conv.System, "\n\n## Instructions of this cell\n\nAnswer in one word.\n"),
				"tools %q, prompt ends %q", conv.Tools, conv.System[max(0, len(conv.System)-80):]); err != nil {
				return agenttest.Turn{}, err
			}
			return agenttest.Turn{Text: "Fine."}, nil
		}, pending, ops)
		if err := c.agent.Once(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got, _ := c.task(t, "board-diagnose-1"); got.Status.Result != tc.result {
			t.Errorf("tools %q: task %+v", tc.tools, got.Status)
		}
	}
}
