package api

import (
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// What an admin looks at when an app is in trouble: its pods, their logs
// and the events in its namespace. Read-only; viewers read it too (E-46),
// although logs can hold anything an app prints.

type exitView struct {
	Reason   string    `json:"reason"`
	ExitCode int32     `json:"exitCode"`
	Message  string    `json:"message,omitempty"`
	At       time.Time `json:"at"`
}

type containerView struct {
	Name     string    `json:"name"`
	Image    string    `json:"image"`
	Ready    bool      `json:"ready"`
	Restarts int32     `json:"restarts"`
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
	Message  string    `json:"message,omitempty"`
	LastExit *exitView `json:"lastExit"`
}

type podView struct {
	Name       string          `json:"name"`
	Phase      string          `json:"phase"`
	Reason     string          `json:"reason,omitempty"`
	Message    string          `json:"message,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	Containers []containerView `json:"containers"`
}

func viewPod(p *corev1.Pod) podView {
	v := podView{Name: p.Name, Phase: string(p.Status.Phase), Reason: p.Status.Reason, Message: p.Status.Message,
		CreatedAt: p.CreationTimestamp.Time, Containers: []containerView{}}
	for _, cs := range append(append([]corev1.ContainerStatus{}, p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...) {
		c := containerView{Name: cs.Name, Image: cs.Image, Ready: cs.Ready, Restarts: cs.RestartCount}
		switch st := cs.State; {
		case st.Running != nil:
			c.State = "running"
		case st.Waiting != nil:
			c.State, c.Reason, c.Message = "waiting", st.Waiting.Reason, st.Waiting.Message
		case st.Terminated != nil:
			c.State, c.Reason, c.Message = "terminated", st.Terminated.Reason, st.Terminated.Message
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			c.LastExit = &exitView{Reason: t.Reason, ExitCode: t.ExitCode, Message: t.Message, At: t.FinishedAt.Time}
		}
		v.Containers = append(v.Containers, c)
	}
	return v
}

// pods are the app's pods, newest first.
func (s *Server) pods(r *http.Request) ([]corev1.Pod, string, error) {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return nil, "", err
	}
	ns := "app-" + app.Name
	var list corev1.PodList
	if err := s.Kube.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		return nil, "", err
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[j].CreationTimestamp.Before(&list.Items[i].CreationTimestamp)
	})
	return list.Items, ns, nil
}

func (s *Server) listPods(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	pods, _, err := s.pods(r)
	if err != nil {
		return err
	}
	out := []podView{}
	for i := range pods {
		out = append(out, viewPod(&pods[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// appLogs returns the end of a container's log as text.
func (s *Server) appLogs(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	pods, ns, err := s.pods(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	tail := int64(200)
	if v := q.Get("tail"); v != "" {
		if tail, err = strconv.ParseInt(v, 10, 64); err != nil || tail < 1 || tail > 5000 {
			return errorf(http.StatusBadRequest, "tail must be a number from 1 to 5000")
		}
	}
	previous := false
	if v := q.Get("previous"); v != "" {
		if previous, err = strconv.ParseBool(v); err != nil {
			return errorf(http.StatusBadRequest, "previous must be true or false")
		}
	}
	var pod *corev1.Pod
	for i := range pods {
		if q.Get("pod") == "" || pods[i].Name == q.Get("pod") {
			pod = &pods[i]
			break
		}
	}
	if pod == nil {
		if q.Get("pod") != "" {
			return errorf(http.StatusNotFound, "app %s has no pod %q", r.PathValue("name"), q.Get("pod"))
		}
		return errorf(http.StatusNotFound, "app %s has no pods", r.PathValue("name"))
	}
	container := q.Get("container")
	if container == "" && len(pod.Spec.Containers) > 0 {
		container = pod.Spec.Containers[0].Name
	}
	limit := int64(512 << 10)
	stream, err := s.Pods.Pods(ns).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: container, TailLines: &tail, Previous: previous, LimitBytes: &limit,
	}).Stream(r.Context())
	if err != nil {
		return errorf(http.StatusBadRequest, "logs of %s/%s: %v", pod.Name, container, err)
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Cell-Pod", pod.Name)
	_, err = io.Copy(w, stream)
	return err
}

type eventView struct {
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Object  string    `json:"object"`
	Message string    `json:"message"`
	Count   int32     `json:"count"`
	Last    time.Time `json:"last"`
}

// appEvents are the newest events in the app's namespace.
func (s *Server) appEvents(w http.ResponseWriter, r *http.Request, _ *Caller) error {
	app, err := s.app(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	var list corev1.EventList
	if err := s.Kube.List(r.Context(), &list, client.InNamespace("app-"+app.Name)); err != nil {
		return err
	}
	out := []eventView{}
	for _, e := range list.Items {
		last := e.LastTimestamp.Time
		switch {
		case e.Series != nil && !e.Series.LastObservedTime.IsZero():
			last = e.Series.LastObservedTime.Time
		case last.IsZero() && !e.EventTime.IsZero():
			last = e.EventTime.Time
		case last.IsZero():
			last = e.CreationTimestamp.Time
		}
		count := e.Count
		if e.Series != nil {
			count = e.Series.Count
		}
		out = append(out, eventView{Type: e.Type, Reason: e.Reason, Object: e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
			Message: e.Message, Count: count, Last: last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	if len(out) > 100 {
		out = out[:100]
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
