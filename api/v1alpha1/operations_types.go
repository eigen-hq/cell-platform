package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Lifecycle of Backup, Restore and ChangeRequest.
// +kubebuilder:validation:Enum=Pending;WaitingForApproval;Running;Completed;Failed;Succeeded;RolledBack;Rejected
type RunPhase string

const (
	RunPending            RunPhase = "Pending"
	RunWaitingForApproval RunPhase = "WaitingForApproval"
	RunRunning            RunPhase = "Running"
	RunCompleted          RunPhase = "Completed"
	RunFailed             RunPhase = "Failed"
	RunSucceeded          RunPhase = "Succeeded"
	RunRolledBack         RunPhase = "RolledBack"
	RunRejected           RunPhase = "Rejected"
)

// Backup is one backup of an app outside the cell: its database (base
// backup in the object store, WAL archived continuously) and its volumes
// (restic snapshot tagged with the backup name and database backup).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.app`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Completed",type=date,JSONPath=`.status.completedAt`
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupSpec   `json:"spec"`
	Status BackupStatus `json:"status,omitempty"`
}

type BackupSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="app is immutable"
	App string `json:"app"`
	// Reason: scheduled, manual, pre-change or a free text.
	Reason string `json:"reason,omitempty"`
}

type BackupStatus struct {
	Phase       RunPhase     `json:"phase,omitempty"`
	Message     string       `json:"message,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Database is set when the app has a database.
	Database *DatabaseBackup `json:"database,omitempty"`
	// Volumes is the restic snapshot of the app's volumes.
	VolumesSnapshot string `json:"volumesSnapshot,omitempty"`
	// Claims are the volumes in the snapshot.
	Claims []string `json:"claims,omitempty"`
	// ClaimsRecorded is true when Claims is the complete list (also when
	// empty); older backups did not record their volumes.
	ClaimsRecorded bool `json:"claimsRecorded,omitempty"`
}

type DatabaseBackup struct {
	// ServerName is the archive the backup and its WAL live in.
	ServerName string `json:"serverName"`
	BackupID   string `json:"backupId,omitempty"`
}

// +kubebuilder:object:root=true
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

// Restore brings an app back to the state of a backup: it stops the app,
// restores its volumes and database, and starts it again. The backup is
// found by name in the object store, so a fresh cell can restore too.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.app`
// +kubebuilder:printcolumn:name="Backup",type=string,JSONPath=`.spec.backup`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
type Restore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RestoreSpec   `json:"spec"`
	Status RestoreStatus `json:"status,omitempty"`
}

type RestoreSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="app is immutable"
	App string `json:"app"`
	// Backup name, or "latest" for the newest backup of the app.
	// +kubebuilder:default=latest
	Backup string `json:"backup,omitempty"`
	// Change names the ChangeRequest this restore rolls back; it then runs
	// under that change's hold on the app.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="change is immutable"
	Change string `json:"change,omitempty"`
}

type RestoreStatus struct {
	Phase       RunPhase     `json:"phase,omitempty"`
	Message     string       `json:"message,omitempty"`
	StartedAt   *metav1.Time `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
	// Step is the current step: lookup, stop, volumes, database, start.
	Step string `json:"step,omitempty"`
	// Resolved is what the backup name pointed to in the object store.
	Resolved *ResolvedBackup `json:"resolved,omitempty"`
	// Replicas of the app's workloads before it was stopped.
	Replicas map[string]int32 `json:"replicas,omitempty"`
	// AppUID binds the restore to the app it started on.
	AppUID string `json:"appUID,omitempty"`
}

type ResolvedBackup struct {
	Backup          string          `json:"backup"`
	VolumesSnapshot string          `json:"volumesSnapshot,omitempty"`
	Database        *DatabaseBackup `json:"database,omitempty"`
	// Claims in the snapshot, complete if ClaimsRecorded.
	Claims         []string `json:"claims,omitempty"`
	ClaimsRecorded bool     `json:"claimsRecorded,omitempty"`
}

// +kubebuilder:object:root=true
type RestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Restore `json:"items"`
}

// ChangeRequest is a proposed or executed change to an app: who wants it,
// why, the diff, and what happened. All ChangeRequests together are the
// change log. An approved change runs as backup, change, health check and,
// on failure, rollback to the backup.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cr
// +kubebuilder:printcolumn:name="App",type=string,JSONPath=`.spec.app`
// +kubebuilder:printcolumn:name="Requester",type=string,JSONPath=`.spec.requester.name`
// Its backup and rollback are named <name>-pre and <name>-rollback.
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 244",message="name must be at most 244 characters"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ChangeRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ChangeRequestSpec   `json:"spec"`
	Status ChangeRequestStatus `json:"status,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(oldSelf.patch) || self.patch == oldSelf.patch",message="patch is immutable"
// +kubebuilder:validation:XValidation:rule="self.action == oldSelf.action",message="action is immutable"
// +kubebuilder:validation:XValidation:rule="has(self.backup) == has(oldSelf.backup) && (!has(self.backup) || self.backup == oldSelf.backup)",message="backup is immutable"
// +kubebuilder:validation:XValidation:rule="(has(self.deleteData) && self.deleteData) == (has(oldSelf.deleteData) && oldSelf.deleteData)",message="deleteData is immutable"
// +kubebuilder:validation:XValidation:rule="self.action in ['remove', 'restore'] || has(self.patch)",message="install and update need a patch"
// +kubebuilder:validation:XValidation:rule="self.action == 'restore' || !has(self.backup)",message="backup is only for action restore"
// +kubebuilder:validation:XValidation:rule="self.action != 'restore' || (has(self.backup) && size(self.backup) > 0 && self.backup != 'latest')",message="restore needs the name of a backup"
// +kubebuilder:validation:XValidation:rule="self.action == 'remove' || !has(self.deleteData) || !self.deleteData",message="deleteData is only for action remove"
type ChangeRequestSpec struct {
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="app is immutable"
	App string `json:"app"`
	// Action: update applies Patch as a JSON merge patch on the App spec;
	// install creates the app with Patch as its spec; remove deletes the
	// app; restore brings back the data of Backup.
	// +kubebuilder:validation:Enum=update;install;remove;restore
	// +kubebuilder:default=update
	Action string `json:"action,omitempty"`
	// Backup to restore (action restore), by name: approvers see what comes
	// back, and the change's own backup cannot be mistaken for "latest".
	Backup string `json:"backup,omitempty"`
	// DeleteData (action remove) also deletes the data the app leaves in
	// the cell: volumes, database and secrets. Its backups stay.
	DeleteData bool      `json:"deleteData,omitempty"`
	Requester  Requester `json:"requester"`
	Reason     string    `json:"reason,omitempty"`
	// +kubebuilder:validation:Enum=low;medium;high
	// +kubebuilder:default=medium
	Risk string `json:"risk,omitempty"`
	// Patch is a JSON merge patch on the App spec (install: the spec).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	Patch *apiextensionsv1.JSON `json:"patch,omitempty"`
	// Approval is required before anything changes (A-10). Only the
	// platform API decides, in a call of its own, and records who decided
	// (admission policy cell-change-approval).
	// +kubebuilder:default={"required":true}
	Approval Approval `json:"approval,omitempty"`
}

type Requester struct {
	// +kubebuilder:validation:Enum=Human;Agent;Operator
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type Approval struct {
	// +kubebuilder:default=true
	Required bool `json:"required,omitempty"`
	// +kubebuilder:validation:Enum=Approved;Rejected
	Decision string       `json:"decision,omitempty"`
	By       string       `json:"by,omitempty"`
	At       *metav1.Time `json:"at,omitempty"`
}

type ChangeRequestStatus struct {
	Phase      RunPhase     `json:"phase,omitempty"`
	Message    string       `json:"message,omitempty"`
	StartedAt  *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
	// Backup taken before the change (rollback point).
	Backup string `json:"backup,omitempty"`
	// AppliedAt is when the change was written to the App.
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
	// Restore used for the rollback.
	Restore string `json:"restore,omitempty"`
	// PreviousSpec is the App spec before the change.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	PreviousSpec *apiextensionsv1.JSON `json:"previousSpec,omitempty"`
	// BaseGeneration is the App generation PreviousSpec was read at.
	BaseGeneration int64 `json:"baseGeneration,omitempty"`
	// AppliedGeneration is the App generation that carries the change.
	AppliedGeneration int64 `json:"appliedGeneration,omitempty"`
	// AppUID binds the change to the app it started on.
	AppUID string `json:"appUID,omitempty"`
}

// +kubebuilder:object:root=true
type ChangeRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ChangeRequest `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Backup{}, &BackupList{}, &Restore{}, &RestoreList{}, &ChangeRequest{}, &ChangeRequestList{})
}
