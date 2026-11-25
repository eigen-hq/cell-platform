package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// The backup gateway's target may change only while nothing uses it: a
// backup across the switch would land half in each target, a change's
// rollback would look for its backup in the wrong one. One ConfigMap is
// a reader/writer lock: backups, restores and changes list themselves in
// users; a switch sets switching to its request's UID once users is empty.
// Every write is an Update with the resourceVersion read (or a Create), so
// of two sides writing at once one gets a conflict and tries again.
const (
	gatewayLock   = "backup-gateway-lock"
	lockUsers     = "users"
	lockSwitching = "switching"
)

const waitingForSwitch = "waiting for the switch to the new backup target"

type lockState struct {
	cm        corev1.ConfigMap
	exists    bool
	users     []string
	switching string
}

// apiReader is the reader past the cache, the client where none is set.
func apiReader(rd client.Reader, c client.Client) client.Reader {
	if rd != nil {
		return rd
	}
	return c
}

func readLock(ctx context.Context, rd client.Reader) (*lockState, error) {
	l := &lockState{}
	err := rd.Get(ctx, system(gatewayLock), &l.cm)
	if apierrors.IsNotFound(err) {
		l.cm = corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cell.SystemNamespace, Name: gatewayLock}}
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	l.exists = true
	if raw := l.cm.Data[lockUsers]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &l.users); err != nil {
			return nil, fmt.Errorf("backup gateway lock: %w", err)
		}
	}
	l.switching = l.cm.Data[lockSwitching]
	return l, nil
}

// write stores the lock as read plus the caller's change. A conflict (or
// AlreadyExists) means another side wrote first; the reconcile retries.
func (l *lockState) write(ctx context.Context, c client.Client) error {
	slices.Sort(l.users)
	l.users = slices.Compact(l.users)
	if l.users == nil {
		l.users = []string{}
	}
	raw, err := json.Marshal(l.users)
	if err != nil {
		return err
	}
	l.cm.Data = map[string]string{lockUsers: string(raw), lockSwitching: l.switching}
	if !l.exists {
		err = c.Create(ctx, &l.cm)
	} else {
		err = c.Update(ctx, &l.cm)
	}
	if err != nil {
		return fmt.Errorf("backup gateway lock: %w", err)
	}
	l.exists = true
	return nil
}

// acquireGateway lists user (such as "backup/<name>") among the gateway's
// users. While the target switches it answers why the caller waits. The
// backup or rollback of a change that holds the lock still gets in: the
// switch waits for that change, and the change for its run.
func acquireGateway(ctx context.Context, c client.Client, rd client.Reader, user string) (string, error) {
	l, err := readLock(ctx, rd)
	if err != nil {
		return "", err
	}
	if slices.Contains(l.users, user) {
		return "", nil
	}
	if l.switching != "" {
		ok, err := runOfHolder(ctx, rd, l, user)
		if err != nil || !ok {
			return waitingForSwitch, err
		}
	}
	l.users = append(l.users, user)
	return "", l.write(ctx, c)
}

// runOfHolder tells whether user is the backup or restore a change listed
// in the lock names in its status. The changes are looked up from the lock,
// not from a label of the run: labels drop names too long for them.
func runOfHolder(ctx context.Context, rd client.Reader, l *lockState, user string) (bool, error) {
	kind, name, _ := strings.Cut(user, "/")
	for _, u := range l.users {
		parent, ok := strings.CutPrefix(u, "change/")
		if !ok {
			continue
		}
		var cr cellv1.ChangeRequest
		if err := rd.Get(ctx, client.ObjectKey{Name: parent}, &cr); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, err
		}
		if (kind == "backup" && cr.Status.Backup == name) || (kind == "restore" && cr.Status.Restore == name) {
			return true, nil
		}
	}
	return false, nil
}

func releaseGateway(ctx context.Context, c client.Client, rd client.Reader, user string) error {
	l, err := readLock(ctx, rd)
	if err != nil || !slices.Contains(l.users, user) {
		return err
	}
	l.users = slices.DeleteFunc(l.users, func(u string) bool { return u == user })
	return l.write(ctx, c)
}

// usesGateway tells whether a listed user still runs: one whose object is
// gone or finished left the lock behind (a crash between end and release).
func usesGateway(ctx context.Context, rd client.Reader, user string) (bool, error) {
	kind, name, _ := strings.Cut(user, "/")
	var o client.Object
	var phase func() cellv1.RunPhase
	switch kind {
	case "backup":
		b := &cellv1.Backup{}
		o, phase = b, func() cellv1.RunPhase { return b.Status.Phase }
	case "restore":
		rs := &cellv1.Restore{}
		o, phase = rs, func() cellv1.RunPhase { return rs.Status.Phase }
	case "change":
		cr := &cellv1.ChangeRequest{}
		o, phase = cr, func() cellv1.RunPhase { return cr.Status.Phase }
	default:
		return false, nil
	}
	if err := rd.Get(ctx, client.ObjectKey{Name: name}, o); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return !finished(phase()), nil
}
