package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Task is work for an agent that no rule covers: find out why an app needs
// attention, or turn a Compose file into a manifest. An agent claims it,
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
type TaskSpec struct {
	// Kind: diagnose (find the cause of an app's trouble and propose the
	// repair) or package (make a manifest from Input and propose installing
	// it as App).
	// +kubebuilder:validation:Enum=diagnose;package
	Kind string `json:"kind"`
	// App to diagnose, or the name the packaged app gets.
	// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$`
	App string `json:"app"`
	// Input: what the operator saw (diagnose) or the Compose file (package).
	// +kubebuilder:validation:MaxLength=65536
	Input     string    `json:"input,omitempty"`
	Requester Requester `json:"requester"`
}

type TaskStatus struct {
	// Pending until an agent claims it, Running while it works, then
	// Succeeded or Failed.
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
