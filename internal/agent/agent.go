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
	"sync"
	"time"
)

//go:embed prompt.md
var systemPrompt string

// Agent works on the cell's pending tasks, one at a time.
type Agent struct {
	API *API
	// Model is the model gateway (E-80); nil without a key to it: tasks
	// then wait for an agent that has one.
	Model *Model
	Log   *slog.Logger
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
	if _, err := operations(); err != nil {
		return err
	}
	if a.Model == nil {
		a.Log.Warn("no key to the model gateway (Secret cell-agent, key modelKey): tasks wait for an agent that has one")
		<-ctx.Done()
		return nil
	}
	waiting := false
	for {
		ready, err := a.ready(ctx)
		if err == nil && ready != !waiting {
			waiting = !ready
			if waiting {
				a.Log.Warn("no model provider set (PUT /v0/models): tasks wait")
			}
		}
		if err == nil && ready {
			err = a.abandon(ctx)
			if err == nil {
				err = a.Once(ctx)
			}
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

// ready tells whether the cell has a model provider: without one, the
// gateway serves no model and a task would only fail.
func (a *Agent) ready(ctx context.Context) (bool, error) {
	var v struct {
		Configured bool `json:"configured"`
	}
	err := a.API.JSON(ctx, http.MethodGet, "/v0/models", "", nil, &v)
	return v.Configured, err
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
		return "tools: " + err.Error(), true
	}
	msgs := []message{{Role: "system", Text: systemPrompt}, {Role: "user", Text: brief(t)}}
	cuts := 0
	// ponytail: a fixed turn limit, a token budget per task if costs need a cap
	for range 40 {
		reply, stop, err := a.Model.complete(ctx, msgs, tools)
		if err != nil {
			return "model: " + modelError(err), true
		}
		answer := strings.TrimSpace(reply.Text)
		// A turn cut at the token limit or by a filter may hold half a call,
		// half an answer, or reasoning without its signature: it is dropped
		// and the model, told so, does it again. A second cut in a row would
		// likely repeat. Calls on a turn that ended as "stop" are whole; some
		// OpenAI-compatible servers end every turn so.
		if stop != "stop" && (stop != "tool_calls" || len(reply.Calls) == 0) {
			if cuts++; cuts == 2 {
				return fmt.Sprintf("stopped before an answer (%s, twice in a row). %s", stop, answer), true
			}
			msgs = append(msgs, message{Role: "user", Text: fmt.Sprintf(cutNote, stop)})
			continue
		}
		cuts = 0
		msgs = append(msgs, reply)
		if len(reply.Calls) == 0 {
			return answer, false
		}
		// The calls of one turn are independent: the model made them before
		// it saw any result. Their results go back in the order of the calls.
		results := make([]string, len(reply.Calls))
		var wg sync.WaitGroup
		for i, c := range reply.Calls {
			wg.Go(func() { results[i] = run(ctx, tools, c) })
		}
		wg.Wait()
		for i, c := range reply.Calls {
			msgs = append(msgs, message{Role: "tool", CallID: c.ID, Text: results[i]})
		}
	}
	return "stopped before an answer (40 model calls)", true
}

// cutNote tells the model that its last turn was cut.
const cutNote = "Your last turn was cut off (%s) and dropped: none of its tool calls ran and its text is lost. Do it again in smaller steps."

// run runs one tool call. A failure goes back to the model as the result,
// marked "ERROR ", so it can correct itself.
func run(ctx context.Context, tools []tool, c toolCall) string {
	i := slices.IndexFunc(tools, func(t tool) bool { return t.Name == c.Name })
	if i < 0 {
		return "ERROR no tool " + c.Name
	}
	out, err := tools[i].Run(ctx, c.Args)
	if err != nil {
		return "ERROR " + err.Error()
	}
	return out
}

// brief is the task as the model reads it.
func brief(t task) string {
	switch t.Kind {
	case "package":
		return fmt.Sprintf("Task %s: package this Compose file as app %q and propose installing it.\n\n```yaml\n%s\n```", t.Name, t.App, t.Input)
	case "ask":
		if t.App == "" {
			return fmt.Sprintf("Task %s: an admin asks about the cell:\n\n%s", t.Name, t.Input)
		}
		return fmt.Sprintf("Task %s: an admin asks about app %q:\n\n%s", t.Name, t.App, t.Input)
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
