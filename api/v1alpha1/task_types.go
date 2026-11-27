package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Task is work for an agent that no rule covers: find out why an app needs
// attention, turn a Compose file into a manifest, or do what a person asked. An agent claims it,
// works only through the platform API, and the API records every call it
// makes as a step, so the task is the agent's part of the change log. What
// the agent wants changed it proposes as ChangeRequests that name the task.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.kind`
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.app`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Assignee",type=string,JSONPath=`.status.assignee`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type Task struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TaskSpec   `json:"spec"`
	Status TaskStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="a task is immutable; create a new one"
// +kubebuilder:validation:XValidation:rule="self.kind == 'ask' || has(self.app)",message="diagnose and package tasks name an app"
type TaskSpec struct {
	// Kind: diagnose (find the cause of an app's trouble and propose the
	// repair), package (make a manifest from Input and propose installing
	// it as App) or ask (do what a person asked in Input, about App if set).
	// +kubebuilder:validation:Enum=diagnose;package;ask
	Kind string `json:"kind"`
	// App to diagnose, the name the packaged app gets, or the app an ask is
	// about. An ask without one sees the cell, not the inside of an app.
	// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`
	App string `json:"app,omitempty"`
	// Input: what the operator saw (diagnose), the Compose file (package)
	// or the person's request (ask).
	// +kubebuilder:validation:MaxLength=65536
	Input     string    `json:"input,omitempty"`
	Requester Requester `json:"requester"`
}

type TaskStatus struct {
	// Pending until an agent claims it, Running while it works, then
	// Succeeded, Failed, or WaitingForInput when the agent asked the person
	// something. A reply makes it Pending again, for the same agent (E-99).
	Phase      RunPhase     `json:"phase,omitempty"`
	Assignee   string       `json:"assignee,omitempty"`
	StartedAt  *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Steps are the agent's API calls, recorded by the API before it
	// serves them.
	// +kubebuilder:validation:MaxItems=200
	Steps []TaskStep `json:"steps,omitempty"`
	// Result is the agent's answer: the cause it found and what it
	// proposed, or why it could not.
	// +kubebuilder:validation:MaxLength=16384
	Result string `json:"result,omitempty"`
	// Reply is a person's last reply: the answer to the agent's question or
	// a follow-up. The agent adds it to the task's transcript, which keeps
	// every reply, and goes on (E-99).
	Reply *TaskReply `json:"reply,omitempty"`
}

type TaskReply struct {
	By string      `json:"by"`
	At metav1.Time `json:"at"`
	// +kubebuilder:validation:MaxLength=16384
	Text string `json:"text"`
}

type TaskStep struct {
	At metav1.Time `json:"at"`
	// Call is the method and path (with query) of the API call.
	// +kubebuilder:validation:MaxLength=1024
	Call string `json:"call"`
}

// +kubebuilder:object:root=true
type TaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Task `json:"items"`
}

func init() {
	register(&Task{}, &TaskList{})
}
