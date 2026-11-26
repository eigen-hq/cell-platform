package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
)

// event is one line of the event stream.
type event struct {
	Type    string `json:"type"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	App     string `json:"app,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
}

func toEvent(e watch.Event) (event, bool) {
	ev := event{Type: string(e.Type)}
	switch o := e.Object.(type) {
	case *cellv1.App:
		ev.Kind, ev.Name, ev.App, ev.Phase = "App", o.Name, o.Name, string(o.Status.Phase)
		for _, c := range o.Status.Conditions {
			if c.Type == "Ready" {
				ev.Message = c.Message
			}
		}
	case *cellv1.ChangeRequest:
		ev.Kind, ev.Name, ev.App, ev.Phase, ev.Message = "ChangeRequest", o.Name, o.Spec.App, string(o.Status.Phase), o.Status.Message
	case *cellv1.Backup:
		ev.Kind, ev.Name, ev.App, ev.Phase, ev.Message = "Backup", o.Name, o.Spec.App, string(o.Status.Phase), o.Status.Message
	case *cellv1.Restore:
		ev.Kind, ev.Name, ev.App, ev.Phase, ev.Message = "Restore", o.Name, o.Spec.App, string(o.Status.Phase), o.Status.Message
	default:
		return ev, false
	}
	return ev, true
}

// events streams changes of apps, changes, backups and restores as
// server-sent events until the client goes away.
func (s *Server) events(w http.ResponseWriter, r *http.Request, c *Caller) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := make(chan event)
	for _, list := range []client.ObjectList{&cellv1.AppList{}, &cellv1.ChangeRequestList{}, &cellv1.BackupList{}, &cellv1.RestoreList{}} {
		wi, err := s.Kube.Watch(ctx, list)
		if err != nil {
			return err
		}
		go func() {
			defer wi.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case e, ok := <-wi.ResultChan():
					if !ok {
						cancel() // a watch ended: end the stream, the client reconnects
						return
					}
					if ev, ok := toEvent(e); ok {
						if forViewer(c) {
							ev.Message = "" // see forViewer
						}
						select {
						case out <- ev:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // clients wait for the headers before the first event
	bw := bufio.NewWriter(w)
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-keepalive.C:
			_, _ = bw.WriteString(": keepalive\n\n")
		case ev := <-out:
			data, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(bw, "event: %s\ndata: %s\n\n", strings.ToLower(ev.Kind), data)
		}
		if err := bw.Flush(); err != nil {
			return nil // client gone
		}
		flusher.Flush()
	}
}
