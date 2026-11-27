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
	"unicode/utf8"
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
	// Reply is a person's reply to the agent's answer or question: the
	// agent goes on from the task's transcript (E-99).
	Reply *struct {
		By   string `json:"by"`
		Text string `json:"text"`
	} `json:"reply"`
}

// answer ends a task: the model's answer, why the task failed, or the
// model's question to the person, whose reply it needs to go on (E-99).
type answer struct {
	Result  string `json:"result"`
	Failed  bool   `json:"failed"`
	Waiting bool   `json:"waiting"`
}

func failed(why string) answer { return answer{Result: why, Failed: true} }

// Run works on tasks until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if _, err := operations(); err != nil {
		return err
	}
	if a.Model == nil {
		a.Log.Warn("no key to the model gateway (Secret cell-agent-<name>, key modelKey): tasks wait for an agent that has one")
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
// got lost. The transcript keeps what was said; an admin can reply to go
// on from there.
func (a *Agent) abandon(ctx context.Context) error {
	var running []task
	if err := a.API.JSON(ctx, http.MethodGet, "/v0/tasks?phase=Running", "", nil, &running); err != nil {
		return err
	}
	for _, t := range running {
		if t.Assignee == a.API.Username {
			if err := a.finish(ctx, t.Name, failed("interrupted: the agent lost the task (restart or lost answer of the API); reply to go on or ask again")); err != nil {
				return err
			}
		}
	}
	return nil
}

// Once works on every pending task, oldest first, but for replies to
// another agent.
func (a *Agent) Once(ctx context.Context) error {
	var pending []task
	if err := a.API.JSON(ctx, http.MethodGet, "/v0/tasks?phase=Pending", "", nil, &pending); err != nil {
		return err
	}
	slices.SortFunc(pending, func(x, y task) int { return x.CreatedAt.Compare(y.CreatedAt) })
	for _, t := range pending {
		if t.Assignee != "" && t.Assignee != a.API.Username {
			continue
		}
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
	ans := a.work(ctx, t)
	if err := a.finish(ctx, t.Name, ans); err != nil {
		return err
	}
	a.Log.Info("done", "task", t.Name, "failed", ans.Failed, "waiting", ans.Waiting)
	return nil
}

// finish ends a task. It retries while the API is unreachable or failing,
// to keep the answer; abandon ends the task otherwise.
func (a *Agent) finish(ctx context.Context, name string, ans answer) error {
	return a.send(ctx, "/v0/tasks/"+url.PathEscape(name)+"/finish", ans, http.StatusOK, 0)
}

// work lets the model work on a claimed task and returns its answer.
func (a *Agent) work(ctx context.Context, t task) answer {
	// Read for every task: a change to the Agent counts from the next one.
	var me struct {
		Agent *struct {
			Tools        []string `json:"tools"`
			Instructions string   `json:"instructions"`
		} `json:"agent"`
	}
	if err := a.API.JSON(ctx, http.MethodGet, "/v0/whoami", "", nil, &me); err != nil {
		return failed("whoami: " + err.Error())
	}
	if me.Agent == nil {
		return failed("whoami: the API names no Agent for this account")
	}
	tools, err := a.tools(t.Name, me.Agent.Tools)
	if err != nil {
		return failed("tools: " + err.Error())
	}
	prompt := systemPrompt
	if me.Agent.Instructions != "" {
		prompt = strings.TrimRight(prompt, "\n") + "\n\n## Instructions of this cell\n\n" + me.Agent.Instructions + "\n"
	}
	tr := &transcript{a: a, path: "/v0/tasks/" + url.PathEscape(t.Name) + "/items"}
	said, err := a.conversation(ctx, t, tr)
	if err != nil {
		return failed(err.Error())
	}
	msgs := append([]message{{Role: "system", Text: prompt}}, said...)
	cuts := 0
	// ponytail: a fixed turn limit, a token budget per task if costs need a cap
	for range 40 {
		reply, stop, err := a.Model.complete(ctx, msgs, tools)
		if err != nil {
			return failed("model: " + modelError(err))
		}
		text := strings.TrimSpace(reply.Text)
		// A turn cut at the token limit or by a filter may hold half a call,
		// half an answer, or reasoning without its signature: it is dropped
		// and the model, told so, does it again. A second cut in a row would
		// likely repeat. Calls on a turn that ended as "stop" are whole; some
		// OpenAI-compatible servers end every turn so.
		if stop != "stop" && (stop != "tool_calls" || len(reply.Calls) == 0) {
			if cuts++; cuts == 2 {
				return failed(fmt.Sprintf("stopped before an answer (%s, twice in a row). %s", stop, text))
			}
			note := message{Role: "user", Text: fmt.Sprintf(cutNote, stop)}
			msgs = append(msgs, note)
			if err := tr.add(ctx, note); err != nil {
				return failed(err.Error())
			}
			continue
		}
		cuts = 0
		msgs = append(msgs, reply)
		if err := tr.add(ctx, reply); err != nil {
			return failed(err.Error())
		}
		if len(reply.Calls) == 0 {
			return answer{Result: text}
		}
		// The calls of one turn are independent: the model made them before
		// it saw any result. Their results go back in the order of the calls.
		results := make([]string, len(reply.Calls))
		var wg sync.WaitGroup
		for i, c := range reply.Calls {
			wg.Go(func() { results[i] = run(ctx, tools, c) })
		}
		wg.Wait()
		outputs := make([]message, len(reply.Calls))
		var asked []string
		for i, c := range reply.Calls {
			outputs[i] = message{Role: "tool", CallID: c.ID, Text: results[i]}
			if q, err := question(c.Args); c.Name == ask.Name && err == nil {
				asked = append(asked, q)
			}
		}
		msgs = append(msgs, outputs...)
		if err := tr.add(ctx, outputs...); err != nil {
			return failed(err.Error())
		}
		// The task waits for the person's reply, after the other calls of
		// the turn ran.
		if len(asked) > 0 {
			return answer{Result: strings.Join(asked, "\n\n"), Waiting: true}
		}
	}
	return failed("stopped before an answer (40 model calls)")
}

// conversation is what was said in task before the model's next turn: the
// brief of a new task, or the transcript and the person's reply (E-99).
// The transcript holds what was said; tr goes on after it.
func (a *Agent) conversation(ctx context.Context, t task, tr *transcript) ([]message, error) {
	if t.Reply == nil {
		first := message{Role: "user", Text: brief(t)}
		return []message{first}, tr.add(ctx, first)
	}
	var items []struct {
		Item item `json:"item"`
	}
	if err := a.API.JSON(ctx, http.MethodGet, tr.path, "", nil, &items); err != nil {
		return nil, fmt.Errorf("transcript: %w", err)
	}
	if len(items) == 0 {
		return nil, errors.New("transcript: empty, nothing to go on from; ask again")
	}
	said := make([]item, len(items))
	for i, it := range items {
		said[i] = it.Item
	}
	msgs := messagesOf(said)
	// A call without its output was cut off by a restart of the agent:
	// providers refuse a call without a result.
	// ponytail: the outputs go last, where a cut leaves them; a gap further
	// up would need the transcript rewritten
	answered := map[string]bool{}
	for _, m := range msgs {
		answered[m.CallID] = true
	}
	var more []message
	for _, m := range msgs {
		for _, c := range m.Calls {
			if !answered[c.ID] {
				more = append(more, message{Role: "tool", CallID: c.ID, Text: interruptedCall})
			}
		}
	}
	more = append(more, message{Role: "user", Name: t.Reply.By, Text: t.Reply.Text})
	tr.next = len(items)
	return append(msgs, more...), tr.add(ctx, more...)
}

// interruptedCall is the output of a call that a restart cut off.
const interruptedCall = "ERROR interrupted: this call may or may not have run; check before you repeat it"

// ask is the model's way to ask the person a question it cannot go on
// without (E-99). Every agent has it: it reaches nothing but the person.
var ask = tool{
	Name: "ask",
	Description: "Ask the person who gave you the task one question you cannot go on without: information or a decision only they have. " +
		"Your turn ends after this call's turn and the task waits for their reply, which comes as the next message. " +
		"Never ask to approve a change: propose it, an admin approves it.",
	Params: map[string]any{"type": "object", "required": []string{"question"}, "properties": map[string]any{
		"question": map[string]any{"type": "string", "description": "The question, in plain text, at most 2000 characters."}}},
	Run: func(_ context.Context, args json.RawMessage) (string, error) {
		if _, err := question(args); err != nil {
			return "", err
		}
		return "Asked; the reply comes as the next message.", nil
	},
}

// question is the question of a call of ask.
func question(args json.RawMessage) (string, error) {
	var in struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	q := strings.TrimSpace(in.Question)
	if q == "" {
		return "", errors.New("ask needs a question")
	}
	if n := utf8.RuneCountInString(q); n > 2000 {
		return "", fmt.Errorf("the question has %d characters, at most 2000", n)
	}
	return q, nil
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
