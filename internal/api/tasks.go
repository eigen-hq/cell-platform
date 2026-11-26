package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// TaskHeader names the task an agent's call belongs to. Agents work only
// in tasks: the API records each of their calls as a step of the task
// before it serves the call, so every agent step is in the change log.
const TaskHeader = "X-Cell-Task"

// maxSteps keeps a runaway agent from growing a task without end.
const maxSteps = 200

// taskFree are the calls an agent makes outside a task: finding, claiming
// and finishing tasks, and whether there is a model to work with.
var taskFree = map[string]bool{
	"GET /v0/whoami": true, "GET /v0/tasks": true, "GET /v0/tasks/{name}": true,
	"POST /v0/tasks/{name}/claim": true, "POST /v0/tasks/{name}/finish": true, "GET /v0/models": true,
}

type taskKey struct{}

// taskRef is the task an agent's request works in.
type taskRef struct{ Name, App string }

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
		ref = taskRef{Name: t.Name, App: t.Spec.App}
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
	Steps      []stepView    `json:"steps"`
	// Changes the agent proposed in the task (only for a single task).
	Changes []string `json:"changes,omitempty"`
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
	for _, st := range t.Status.Steps {
		v.Steps = append(v.Steps, stepView{At: st.At.Time, Call: st.Call})
	}
	return v
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	var list cellv1.TaskList
	if err := s.Kube.List(r.Context(), &list); err != nil {
		return err
	}
	q := r.URL.Query()
	out := []taskView{}
	for i := range list.Items {
		v := viewTask(&list.Items[i])
		if (q.Get("app") == "" || v.App == q.Get("app")) && (q.Get("phase") == "" || v.Phase == q.Get("phase")) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	t, err := s.task(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
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

// createTask asks an agent for work: diagnose an installed app, or package
// a Compose file as a new app.
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
	default:
		return errorf(http.StatusBadRequest, "kind must be diagnose or package")
	}
	t := &cellv1.Task{ObjectMeta: metav1.ObjectMeta{GenerateName: req.App + "-" + req.Kind + "-"},
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
	now := metav1.NewTime(s.now())
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
}

// finishTask records the agent's answer and ends the task.
func (s *Server) finishTask(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var req finishTaskRequest
	if err := readJSON(r, &req); err != nil {
		return err
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
		return s.Kube.Status().Update(r.Context(), t)
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, viewTask(t))
	return nil
}
