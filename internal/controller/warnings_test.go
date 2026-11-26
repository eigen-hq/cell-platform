package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// A diagnose task the operator opens mails the admins once, through the
// cell's mail server; without one set, or for others' tasks, nothing goes.
func TestWarnings(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	task := func(name, requester string, age time.Duration) *cellv1.Task {
		return &cellv1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(now.Add(-age))},
			Spec: cellv1.TaskSpec{Kind: "diagnose", App: "wiki", Input: "Cause: health checks fail since 2026-11-26T10:00:00Z\n\nReady=False",
				Requester: cellv1.Requester{Kind: requester, Name: "x"}}}
	}
	c := fakeClient(k8s.Scheme(),
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-config"}, Data: map[string]string{"domain": "cell.example.com"}},
		task("new", "Operator", time.Minute), task("old", "Operator", 2*time.Hour), task("asked", "User", time.Minute))
	type sent struct {
		to            []string
		subject, body string
	}
	var mails []sent
	var fail error
	r := &WarningReconciler{Client: c, Reader: c, Now: func() time.Time { return now },
		Recipients: func(context.Context) ([]string, error) { return []string{"it@example.com"}, nil },
		Send: func(_ context.Context, m cell.Mail, to []string, subject, body string) error {
			if m.Host != "smtp.example.com" {
				t.Errorf("mail server %+v", m)
			}
			mails = append(mails, sent{to, subject, body})
			return fail
		}}
	run := func(name string) error {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: name}})
		return err
	}
	if err := run("new"); err != nil || len(mails) != 0 {
		t.Fatalf("without mail set: %v %v", err, mails)
	}
	m := cell.Mail{Host: "smtp.example.com", Port: 587, From: "cell@example.com"}
	if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-mail"}, Data: m.Data()}); err != nil {
		t.Fatal(err)
	}
	fail = errors.New("421 try later")
	if err := run("new"); err == nil {
		t.Error("a failed mail is not tried again")
	}
	fail = nil
	for _, n := range []string{"new", "new", "old", "asked"} {
		if err := run(n); err != nil {
			t.Fatal(err)
		}
	}
	if len(mails) != 2 || mails[1].to[0] != "it@example.com" || mails[1].subject != "wiki needs attention (cell.example.com)" ||
		!strings.Contains(mails[1].body, "Cause: health checks fail since") || !strings.Contains(mails[1].body, "https://console.cell.example.com/apps/wiki") {
		t.Errorf("mails %+v", mails)
	}
	var got cellv1.Task
	if err := c.Get(ctx, client.ObjectKey{Name: "new"}, &got); err != nil || got.Annotations[WarnedAnnotation] == "" {
		t.Errorf("not marked: %v %v", err, got.Annotations)
	}
}

// The message is plain text with one line per header; a line break in a
// header is refused.
func TestMailSettingsRoundTrip(t *testing.T) {
	m := cell.Mail{Host: "smtp.example.com", Port: 465, Username: "u", Password: "p", From: "cell@example.com", TLS: true}
	got, err := cell.ReadMail(m.Data())
	if err != nil || got != m {
		t.Errorf("read %+v %v", got, err)
	}
	if _, err := cell.ReadMail(map[string][]byte{"host": []byte("h")}); err == nil {
		t.Error("incomplete settings read")
	}
	bad := m
	bad.From = "cell@example.com\r\nBcc: x@example.org"
	if err := bad.Send(context.Background(), []string{"a@example.com"}, "s", "b"); err == nil || !strings.Contains(err.Error(), "line break") {
		t.Errorf("header injection: %v", err)
	}
}

type fakeMailIdentity struct {
	set  []cell.Mail
	fail error
}

func (f *fakeMailIdentity) SetMail(_ context.Context, m cell.Mail) error {
	if f.fail != nil {
		return f.fail
	}
	f.set = append(f.set, m)
	return nil
}

// The identity's mail stage follows Secret cell-mail; a failed set is an
// error, so it is tried again.
func TestMailSync(t *testing.T) {
	ctx := context.Background()
	c := fakeClient(k8s.Scheme())
	id := &fakeMailIdentity{}
	r := &MailReconciler{Client: c, Reader: c, Identity: id}
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil || len(id.set) != 0 {
		t.Fatalf("without settings: %v %v", err, id.set)
	}
	m := cell.Mail{Host: "smtp.example.com", Port: 587, Username: "u", Password: "p", From: "cell@example.com"}
	if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "cell-mail"}, Data: m.Data()}); err != nil {
		t.Fatal(err)
	}
	id.fail = errors.New("identity starting")
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err == nil {
		t.Error("a failed set is not tried again")
	}
	id.fail = nil
	if _, err := r.Reconcile(ctx, ctrl.Request{}); err != nil || len(id.set) != 1 || id.set[0] != m {
		t.Errorf("set %v %v", err, id.set)
	}
}
