package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// TaskHeader names the task an agent's call belongs to. Agents work only
// in tasks: the API records each of their calls as a step of the task
// before it serves the call, so every agent step is in the change log.
const TaskHeader = "X-Cell-Task"

// maxSteps keeps a runaway agent from growing a task without end.
const maxSteps = 200

// taskFree are the calls an agent makes outside a task: finding, claiming
// and finishing tasks, recording their transcripts, and whether there is a
// model to work with.
var taskFree = map[string]bool{
	"GET /v0/whoami": true, "GET /v0/tasks": true, "GET /v0/tasks/{name}": true,
	"POST /v0/tasks/{name}/claim": true, "POST /v0/tasks/{name}/finish": true, "GET /v0/models": true,
	"GET /v0/tasks/{name}/items": true, "POST /v0/tasks/{name}/items": true,
}

type taskKey struct{}

// taskRef is the task an agent's request works in.
type taskRef struct{ Name, Kind, App string }

// taskOf is the task the request's agent works in (zero for people).
func taskOf(ctx context.Context) taskRef {
	ref, _ := ctx.Value(taskKey{}).(taskRef)
	return ref
}

// inTask records an agent's call as a step of the task it names and
// returns the request's context with the task. A call that cannot be
// recorded is refused.
func (s *Server) inTask(r *http.Request, c *Caller) (context.Context, error) {
	name := r.Header.Get(TaskHeader)
	if name == "" {
		return nil, errorf(http.StatusBadRequest, "an agent works in a task: name it in header %s", TaskHeader)
	}
	call := r.Method + " " + r.URL.RequestURI()
	if len(call) > 1024 {
		call = call[:1024]
	}
	backoff := wait.Backoff{Steps: 10, Duration: 10 * time.Millisecond, Factor: 1.5, Jitter: 0.5}
	var ref taskRef
	err := retry.RetryOnConflict(backoff, func() error {
		t, err := s.task(r.Context(), name)
		if err != nil {
			return err
		}
		ref = taskRef{Name: t.Name, Kind: t.Spec.Kind, App: t.Spec.App}
		if t.Status.Phase != cellv1.RunRunning || t.Status.Assignee != c.Username {
			return errorf(http.StatusConflict, "task %s is not running for you (phase %s, assignee %q)", name, t.Status.Phase, t.Status.Assignee)
		}
		if len(t.Status.Steps) >= maxSteps {
			return errorf(http.StatusConflict, "task %s has %d steps, the most a task can have: finish it", name, maxSteps)
		}
		t.Status.Steps = append(t.Status.Steps, cellv1.TaskStep{At: metav1.NewTime(s.now()), Call: call})
		return s.Kube.Status().Update(r.Context(), t)
	})
	if err != nil {
		return nil, err
	}
	return context.WithValue(r.Context(), taskKey{}, ref), nil
}

func (s *Server) task(ctx context.Context, name string) (*cellv1.Task, error) {
	var t cellv1.Task
	if err := s.Kube.Get(ctx, client.ObjectKey{Name: name}, &t); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, errorf(http.StatusNotFound, "no task %q", name)
		}
		return nil, err
	}
	return &t, nil
}

type stepView struct {
	At   time.Time `json:"at"`
	Call string    `json:"call"`
}

type taskView struct {
	Name       string        `json:"name"`
	Kind       string        `json:"kind"`
	App        string        `json:"app"`
	Input      string        `json:"input"`
	Requester  requesterView `json:"requester"`
	Phase      string        `json:"phase"`
	Assignee   string        `json:"assignee"`
	CreatedAt  time.Time     `json:"createdAt"`
	StartedAt  *time.Time    `json:"startedAt"`
	FinishedAt *time.Time    `json:"finishedAt"`
	Result     string        `json:"result"`
	Reply      *replyView    `json:"reply"`
	Steps      []stepView    `json:"steps"`
	// Changes the agent proposed in the task (only for a single task).
	Changes []string `json:"changes,omitempty"`
}

type replyView struct {
	By   string    `json:"by"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

func viewTask(t *cellv1.Task) taskView {
	v := taskView{Name: t.Name, Kind: t.Spec.Kind, App: t.Spec.App, Input: t.Spec.Input,
		Requester: requesterView{Kind: t.Spec.Requester.Kind, Name: t.Spec.Requester.Name},
		Phase:     string(t.Status.Phase), Assignee: t.Status.Assignee, CreatedAt: t.CreationTimestamp.Time,
		StartedAt: timePtr(t.Status.StartedAt), FinishedAt: timePtr(t.Status.FinishedAt), Result: t.Status.Result,
		Steps: []stepView{}}
	if v.Phase == "" {
		v.Phase = string(cellv1.RunPending)
	}
	if r := t.Status.Reply; r != nil {
		v.Reply = &replyView{By: r.By, At: r.At.Time, Text: r.Text}
	}
	for _, st := range t.Status.Steps {
		v.Steps = append(v.Steps, stepView{At: st.At.Time, Call: st.Call})
	}
	return v
}

// agentSees tells whether the caller may see task: everyone but an agent,
// and an agent the tasks it takes and the ones it holds.
func agentSees(ctx context.Context, c *Caller, t *cellv1.Task) bool {
	a := agentOf(ctx)
	return a == nil || t.Status.Assignee == c.Username || a.Spec.Takes(t.Spec.Kind, t.Spec.App)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var list cellv1.TaskList
	if err := s.Kube.List(r.Context(), &list); err != nil {
		return err
	}
	q := r.URL.Query()
	out := []taskView{}
	for i := range list.Items {
		v := viewTask(&list.Items[i])
		if (q.Get("app") == "" || v.App == q.Get("app")) && (q.Get("phase") == "" || v.Phase == q.Get("phase")) && agentSees(r.Context(), c, &list.Items[i]) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request, c *Caller) error {
	t, err := s.task(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if !agentSees(r.Context(), c, t) {
		return mayTake(agentOf(r.Context()), t.Spec.Kind, t.Spec.App)
	}
	v := viewTask(t)
	var changes cellv1.ChangeRequestList
	if err := s.Kube.List(r.Context(), &changes); err != nil {
		return err
	}
	v.Changes = []string{}
	for _, cr := range changes.Items {
		if cr.Spec.Task == t.Name {
			v.Changes = append(v.Changes, cr.Name)
		}
	}
	sort.Strings(v.Changes)
	writeJSON(w, http.StatusOK, v)
	return nil
}

type createTaskRequest struct {
	Kind  string `json:"kind"`
	App   string `json:"app"`
	Input string `json:"input"`
}

// maxAsk bounds a person's request: a request, not a document.
const maxAsk = 4000

// createTask asks an agent for work: diagnose an installed app, package
// a Compose file as a new app, or do what a person asks, about one app or
// the cell.
func (s *Server) createTask(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var req createTaskRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	switch req.Kind {
	case "diagnose":
		if _, err := s.app(r.Context(), req.App); err != nil {
			return err
		}
	case "package":
		if req.Input == "" {
			return errorf(http.StatusBadRequest, "a package task needs the Compose file as input")
		}
		if _, err := s.app(r.Context(), req.App); err == nil {
			return errorf(http.StatusConflict, "app %q is already installed", req.App)
		}
	case "ask":
		if strings.TrimSpace(req.Input) == "" {
			return errorf(http.StatusBadRequest, "an ask task needs the request as input")
		}
		if n := utf8.RuneCountInString(req.Input); n > maxAsk {
			return errorf(http.StatusBadRequest, "the request has %d characters, at most %d", n, maxAsk)
		}
		if req.App != "" {
			if _, err := s.app(r.Context(), req.App); err != nil {
				return err
			}
		}
	default:
		return errorf(http.StatusBadRequest, "kind must be diagnose, package or ask")
	}
	prefix := req.App + "-" + req.Kind + "-"
	if req.App == "" {
		prefix = req.Kind + "-"
	}
	t := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix},
		Spec: cellv1.TaskSpec{Kind: req.Kind, App: req.App, Input: req.Input, Requester: cellv1.Requester{Kind: "Human", Name: c.Username}}}
	if err := s.Kube.Create(r.Context(), t); err != nil {
		if apierrors.IsInvalid(err) {
			return errorf(http.StatusBadRequest, "%v", err)
		}
		return err
	}
	writeJSON(w, http.StatusCreated, viewTask(t))
	return nil
}

// claimTask gives a pending task to the calling agent.
func (s *Server) claimTask(w http.ResponseWriter, r *http.Request, c *Caller) error {
	if c.Role != RoleAgent {
		return errorf(http.StatusForbidden, "tasks are for agents; you are %s", c.Role)
	}
	t, err := s.task(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	if t.Status.Phase != "" && t.Status.Phase != cellv1.RunPending {
		return errorf(http.StatusConflict, "task %s is %s", t.Name, t.Status.Phase)
	}
	// A reply goes to the agent that holds the conversation (E-99).
	if t.Status.Assignee != "" && t.Status.Assignee != c.Username {
		return errorf(http.StatusConflict, "task %s waits for %s", t.Name, t.Status.Assignee)
	}
	now := metav1.NewTime(s.now())
	if err := mayTake(agentOf(r.Context()), t.Spec.Kind, t.Spec.App); err != nil {
		if t.Status.Assignee != c.Username {
			return err
		}
		// A reply waits for this agent, which no longer takes such tasks
		// since: the task fails, so the reply waits for nobody and the agent
		// goes on with its other work.
		t.Status.Phase, t.Status.FinishedAt = cellv1.RunFailed, &now
		t.Status.Result = "The reply was not taken: " + err.Error() + ". Ask again in a new task."
		if err := s.Kube.Status().Update(r.Context(), t); err != nil && !apierrors.IsConflict(err) {
			return err
		}
		return errorf(http.StatusConflict, "task %s: %v", t.Name, err)
	}
	t.Status.Phase, t.Status.Assignee, t.Status.StartedAt = cellv1.RunRunning, c.Username, &now
	// The resource version makes two agents claiming at once safe: one wins.
	if err := s.Kube.Status().Update(r.Context(), t); err != nil {
		if apierrors.IsConflict(err) {
			return errorf(http.StatusConflict, "task %s was just claimed", t.Name)
		}
		return err
	}
	writeJSON(w, http.StatusOK, viewTask(t))
	return nil
}

type finishTaskRequest struct {
	Result string `json:"result"`
	Failed bool   `json:"failed"`
	// Waiting: the result is a question to the person, whose reply the
	// agent needs to go on.
	Waiting bool `json:"waiting"`
}

// finishTask records the agent's answer and ends the task.
func (s *Server) finishTask(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var req finishTaskRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	if req.Failed && req.Waiting {
		return errorf(http.StatusBadRequest, "a task fails or waits, not both")
	}
	if len(req.Result) > 16384 {
		req.Result = req.Result[:16384]
	}
	var t *cellv1.Task
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		if t, err = s.task(r.Context(), r.PathValue("name")); err != nil {
			return err
		}
		if t.Status.Phase != cellv1.RunRunning || t.Status.Assignee != c.Username {
			return errorf(http.StatusConflict, "task %s is not running for you (phase %s, assignee %q)", t.Name, t.Status.Phase, t.Status.Assignee)
		}
		now := metav1.NewTime(s.now())
		t.Status.Phase, t.Status.Result, t.Status.FinishedAt = cellv1.RunSucceeded, req.Result, &now
		if req.Failed {
			t.Status.Phase = cellv1.RunFailed
		}
		if req.Waiting {
			t.Status.Phase = cellv1.RunWaitingForInput
		}
		return s.Kube.Status().Update(r.Context(), t)
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewTask(t))
	return nil
}

type replyRequest struct {
	Text string `json:"text"`
}

// replyTask hands a person's reply to the agent that holds the task's
// conversation: the answer to its question, or a follow-up to its answer
// (E-99). The task is pending again, for that agent only; it reads the
// conversation back from the transcript and goes on.
func (s *Server) replyTask(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var req replyRequest
	if err := readJSON(r, &req); err != nil {
		return err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return errorf(http.StatusBadRequest, "a reply needs text")
	}
	if n := utf8.RuneCountInString(text); n > maxAsk {
		return errorf(http.StatusBadRequest, "the reply has %d characters, at most %d", n, maxAsk)
	}
	ts, err := s.transcripts()
	if err != nil {
		return err
	}
	var t *cellv1.Task
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		if t, err = s.task(r.Context(), r.PathValue("name")); err != nil {
			return err
		}
		switch t.Status.Phase {
		case cellv1.RunSucceeded, cellv1.RunFailed, cellv1.RunWaitingForInput:
		default:
			return errorf(http.StatusConflict, "task %s is %s: reply when the agent has answered or asked", t.Name, viewTask(t).Phase)
		}
		if len(t.Status.Steps) >= maxSteps {
			return errorf(http.StatusConflict, "task %s has %d steps, the most a task can have: start a new task", t.Name, maxSteps)
		}
		if err := s.holds(r.Context(), t); err != nil {
			return err
		}
		key, err := transcriptOf(t)
		if err != nil {
			return err
		}
		items, err := ts.Items(r.Context(), key)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return errorf(http.StatusConflict, "task %s has no transcript to go on from: start a new task", t.Name)
		}
		now := metav1.NewTime(s.now())
		t.Status.Phase, t.Status.FinishedAt = cellv1.RunPending, nil
		t.Status.Reply = &cellv1.TaskReply{By: c.Username, At: now, Text: text}
		return s.Kube.Status().Update(r.Context(), t)
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewTask(t))
	return nil
}

// holds refuses a reply that no agent could take: the task's agent is gone,
// being deleted, made again (another UID) or no longer takes such tasks.
func (s *Server) holds(ctx context.Context, t *cellv1.Task) error {
	name, uid, ok := cell.AgentOf(t.Status.Assignee)
	if !ok {
		return errorf(http.StatusConflict, "task %s was not worked on by an agent: start a new task", t.Name)
	}
	var a cellv1.Agent
	err := s.Kube.Get(ctx, client.ObjectKey{Name: name}, &a)
	if client.IgnoreNotFound(err) != nil {
		return err
	}
	if err != nil || string(a.UID) != uid || !a.DeletionTimestamp.IsZero() || !a.Spec.Takes(t.Spec.Kind, t.Spec.App) {
		return errorf(http.StatusConflict, "agent %s that worked on task %s is gone or no longer takes it: start a new task", name, t.Name)
	}
	return nil
}
