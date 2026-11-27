package v1alpha1

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Agent is one agent of the cell (E-97): an account of its own in the
// identity, the kinds of tasks it takes, the apps whose tasks it takes and
// the tools it may call. The operator runs it as Deployment
// cell-agent-<name> with the credentials in Secret cell-agent-<name>; the
// API checks every call of the agent against this spec.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Kinds",type=string,JSONPath=`.spec.kinds`
// +kubebuilder:printcolumn:name="Apps",type=string,JSONPath=`.spec.apps`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z]([a-z0-9-]{0,28}[a-z0-9])?$')",message="an agent's name is a lowercase name of at most 30 characters"
// +kubebuilder:validation:XValidation:rule="self.metadata.name != 'llm'",message="llm is taken: Secret cell-agent-llm holds the model settings"
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AgentSpec `json:"spec"`
}

type AgentSpec struct {
	// Kinds of tasks the agent takes: diagnose, package, ask.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Enum=diagnose;package;ask
	Kinds []string `json:"kinds"`
	// Apps whose tasks the agent takes; * stands for every app and for
	// tasks about the whole cell.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Pattern=`^(\*|[a-z]([a-z0-9-]{0,38}[a-z0-9])?)$`
	Apps []string `json:"apps"`
	// Tools are the operationIds of the platform API marked x-agent that the
	// agent may call; * stands for all of them.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:items:Pattern=`^(\*|[a-zA-Z]+)$`
	Tools []string `json:"tools"`
	// Instructions are added to the agent's system prompt.
	// +kubebuilder:validation:MaxLength=8192
	Instructions string `json:"instructions,omitempty"`
	// Paused stops the agent: its Deployment runs no pod, and its tasks
	// wait for another agent that takes them.
	Paused bool `json:"paused,omitempty"`
}

// Takes tells whether the agent takes a task of this kind about app (""
// for the whole cell).
func (s AgentSpec) Takes(kind, app string) bool {
	return slices.Contains(s.Kinds, kind) && (slices.Contains(s.Apps, "*") || app != "" && slices.Contains(s.Apps, app))
}

// MayCall tells whether the agent may call the operation; "" is an
// operation no agent may call.
func (s AgentSpec) MayCall(operationID string) bool {
	return operationID != "" && (slices.Contains(s.Tools, "*") || slices.Contains(s.Tools, operationID))
}

// +kubebuilder:object:root=true
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	register(&Agent{}, &AgentList{})
}
