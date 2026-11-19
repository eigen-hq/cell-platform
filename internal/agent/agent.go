// Package agent is the ops agent: it takes tasks from the platform API and
// works on them with a language model whose only tools are calls of that
// API. It has no rights of its own in the cluster; whatever it wants
// changed it proposes as a ChangeRequest, which an admin approves.
package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

//go:embed prompt.md
var systemPrompt string

// Agent works on the cell's pending tasks, one at a time.
type Agent struct {
	API *API
	// Model is nil without a model key: tasks then wait for an agent that
	// has one.
	Model   *anthropic.Client
	ModelID string
	Log     *slog.Logger
	// Poll is how often the agent looks for new tasks.
	Poll time.Duration
}

// task is what the agent reads of a task.
type task struct {
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	App       string    `json:"app"`
	Input     string    `json:"input"`
	Phase     string    `json:"phase"`
	Assignee  string    `json:"assignee"`
	CreatedAt time.Time `json:"createdAt"`
}

// Run works on tasks until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if a.Model == nil {
		a.Log.Warn("no model key (Secret cell-agent-llm, key apiKey): tasks wait for an agent that has one")
		<-ctx.Done()
		return nil
	}
	for {
		err := a.abandon(ctx)
		if err == nil {
			err = a.Once(ctx)
		}
		if err != nil {
			a.Log.Error("tasks", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.Poll):
		}
	}
}

// abandon ends the tasks of this agent that are still running between two
// polls: an earlier run of the agent left them, or the answer to a claim
// got lost. The conversation with the model is gone; an admin can ask again.
func (a *Agent) abandon(ctx context.Context) error {
	var running []task
	if err := a.API.JSON(ctx, http.MethodGet, "/v0/tasks?phase=Running", "", nil, &running); err != nil {
		return err
	}
	for _, t := range running {
		if t.Assignee == a.API.Username {
			if err := a.finish(ctx, t.Name, "interrupted: the agent lost the task (restart or lost answer of the API); ask again", true); err != nil {
				return err
			}
		}
	}
	return nil
}

// Once works on every pending task, oldest first.
func (a *Agent) Once(ctx context.Context) error {
	var pending []task
	if err := a.API.JSON(ctx, http.MethodGet, "/v0/tasks?phase=Pending", "", nil, &pending); err != nil {
		return err
	}
	slices.SortFunc(pending, func(x, y task) int { return x.CreatedAt.Compare(y.CreatedAt) })
	for _, t := range pending {
		if err := a.Take(ctx, t.Name); err != nil && !errors.Is(err, errTaken) {
			return err
		}
	}
	return nil
}

var errTaken = errors.New("another agent took the task")

// Take claims the named task, works on it and finishes it.
func (a *Agent) Take(ctx context.Context, name string) error {
	code, body, err := a.API.Do(ctx, http.MethodPost, "/v0/tasks/"+url.PathEscape(name)+"/claim", "", nil)
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		return fmt.Errorf("claim task %s: %w: %s", name, errTaken, body)
	}
	if code != http.StatusOK {
		return fmt.Errorf("claim task %s: HTTP %d: %s", name, code, body)
	}
	var t task
	if err := json.Unmarshal(body, &t); err != nil {
		return fmt.Errorf("claim task %s: %w", name, err)
	}
	a.Log.Info("working", "task", t.Name, "kind", t.Kind, "app", t.App)
	result, failed := a.work(ctx, t)
	if err := a.finish(ctx, t.Name, result, failed); err != nil {
		return err
	}
	a.Log.Info("done", "task", t.Name, "failed", failed)
	return nil
}

// finish ends a task. It retries while the API is unreachable or failing,
// to keep the answer; abandon ends the task otherwise.
func (a *Agent) finish(ctx context.Context, name, result string, failed bool) error {
	delay := time.Second
	for {
		code, body, err := a.API.Do(ctx, http.MethodPost, "/v0/tasks/"+url.PathEscape(name)+"/finish", "",
			map[string]any{"result": result, "failed": failed})
		if err == nil && code == http.StatusOK {
			return nil
		}
		if err == nil && code < http.StatusInternalServerError {
			return fmt.Errorf("finish task %s: HTTP %d: %s", name, code, body)
		}
		a.Log.Warn("finish task, retrying", "task", name, "status", code, "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(2*delay, time.Minute)
	}
}

// work lets the model work on a claimed task and returns its answer.
func (a *Agent) work(ctx context.Context, t task) (string, bool) {
	tools, err := a.tools(t.Name)
	if err != nil {
		return "agent: " + err.Error(), true
	}
	runner := a.Model.Beta.Messages.NewToolRunner(tools, anthropic.BetaToolRunnerParams{
		BetaMessageNewParams: anthropic.BetaMessageNewParams{
			Model:     a.ModelID,
			MaxTokens: 16000,
			System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
			Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(brief(t)))},
			// Every turn sends the whole conversation; the cache keeps that cheap.
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
			OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortHigh},
		},
		// ponytail: a fixed turn limit, a token budget per task if costs need a cap
		MaxIterations: 40,
		DisableThread: true,
	})
	msg, err := runner.RunToCompletion(ctx)
	if err != nil {
		return "model: " + err.Error(), true
	}
	var text []string
	for _, b := range msg.Content {
		if tb, ok := b.AsAny().(anthropic.BetaTextBlock); ok {
			text = append(text, tb.Text)
		}
	}
	answer := strings.TrimSpace(strings.Join(text, "\n"))
	if msg.StopReason != anthropic.BetaStopReasonEndTurn {
		return fmt.Sprintf("stopped before an answer (%s). %s", msg.StopReason, answer), true
	}
	return answer, false
}

// brief is the task as the model reads it.
func brief(t task) string {
	switch t.Kind {
	case "package":
		return fmt.Sprintf("Task %s: package this Compose file as app %q and propose installing it.\n\n```yaml\n%s\n```", t.Name, t.App, t.Input)
	default:
		return fmt.Sprintf("Task %s: app %q needs attention. The operator reports:\n\n%s\n\nFind the cause and propose the repair.", t.Name, t.App, t.Input)
	}
}

// JSON calls the API and decodes a successful answer into out.
func (c *API) JSON(ctx context.Context, method, path, task string, in, out any) error {
	code, body, err := c.Do(ctx, method, path, task, in)
	if err != nil {
		return err
	}
	if code >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, code, body)
	}
	return json.Unmarshal(body, out)
}
