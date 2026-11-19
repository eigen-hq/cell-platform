package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// App is an installed app. Its spec is the catalog manifest plus the
// customer's choices; see docs/app-manifest.md.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')",message="name must be a DNS label of at most 40 characters"
type App struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AppSpec   `json:"spec"`
	Status AppStatus `json:"status,omitempty"`
}

type AppSpec struct {
	Title string `json:"title,omitempty"`
	// Version of the app; templates see it as {{ .app.version }}.
	// +kubebuilder:validation:MinLength=1
	Version string       `json:"version"`
	Source  Source       `json:"source"`
	Needs   Needs        `json:"needs,omitempty"`
	Access  Access       `json:"access"`
	Health  Health       `json:"health,omitempty"`
	Backup  BackupPolicy `json:"backup,omitempty"`
}

// BackupPolicy says how often the platform backs the app up outside the
// cell. Apps without database and volumes are skipped. Backups are kept
// for 30 days.
type BackupPolicy struct {
	// +kubebuilder:validation:Enum=none;hourly;daily;weekly
	// +kubebuilder:default=daily
	Schedule string `json:"schedule,omitempty"`
}

// Source says where the app comes from. Strings in it may contain
// placeholders such as {{ .database.host }} (Go templates, see docs).
// +kubebuilder:validation:XValidation:rule="has(self.helm) != has(self.image)",message="set exactly one of helm or image"
// +kubebuilder:validation:XValidation:rule="has(self.helm) == has(oldSelf.helm)",message="switching between helm and image is not supported; remove and reinstall the app"
type Source struct {
	Helm  *HelmSource  `json:"helm,omitempty"`
	Image *ImageSource `json:"image,omitempty"`
}

type HelmSource struct {
	// Repo is an HTTP chart repository or an OCI registry path (oci://...).
	// +kubebuilder:validation:Pattern=`^(https://|oci://)`
	Repo  string `json:"repo"`
	Chart string `json:"chart"`
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:Type=object
	Values *apiextensionsv1.JSON `json:"values,omitempty"`
}

type ImageSource struct {
	Repository string `json:"repository"`
	// Tag defaults to the app version.
	Tag     string          `json:"tag,omitempty"`
	Command []string        `json:"command,omitempty"`
	Args    []string        `json:"args,omitempty"`
	Env     []corev1.EnvVar `json:"env,omitempty"`
	// Files are mounted read-only into the container.
	Files []File `json:"files,omitempty"`
}

type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

// Needs are shared services the platform provides.
type Needs struct {
	Database *Database `json:"database,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Volumes []Volume `json:"volumes,omitempty"`
	// Secrets are generated once (key "password") and kept across reinstalls;
	// templates see their names as {{ .secrets.<name> }}.
	Secrets []string `json:"secrets,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.size == oldSelf.size",message="resizing the database is not supported yet"
type Database struct {
	// +kubebuilder:validation:Enum=postgres
	Engine string `json:"engine"`
	// +kubebuilder:default="1Gi"
	Size resource.Quantity `json:"size,omitempty"`
}

// Resizing is not supported yet: a change could grow a volume, but its
// rollback could not shrink it again.
// +kubebuilder:validation:XValidation:rule="self.size == oldSelf.size",message="resizing a volume is not supported yet"
type Volume struct {
	// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]*[a-z0-9])?$`
	Name string            `json:"name"`
	Size resource.Quantity `json:"size"`
	// MountPath in the container (image apps).
	MountPath string `json:"mountPath,omitempty"`
}

type Access struct {
	// Subdomain of the cell domain the app is reachable at.
	// +kubebuilder:validation:Pattern=`^[a-z]([a-z0-9-]*[a-z0-9])?$`
	Subdomain string `json:"subdomain"`
	Port      int32  `json:"port"`
	// Service to route to; default: the app name. May use placeholders.
	Service string `json:"service,omitempty"`
	Auth    Auth   `json:"auth"`
}

// +kubebuilder:validation:XValidation:rule="self.mode == oldSelf.mode",message="changing the login mode is not supported; remove and reinstall the app"
type Auth struct {
	// +kubebuilder:validation:Enum=oidc;proxy;none
	Mode string `json:"mode"`
	// RedirectPaths are the OIDC callback paths of the app (mode oidc).
	RedirectPaths []string `json:"redirectPaths,omitempty"`
	// SecretKeys names the keys of the client secret ({{ .auth.secretName }})
	// the way the app or chart expects them.
	SecretKeys SecretKeys `json:"secretKeys,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="(has(self.clientId) ? self.clientId : 'clientId') != (has(self.clientSecret) ? self.clientSecret : 'clientSecret')",message="clientId and clientSecret must be different keys"
type SecretKeys struct {
	// +kubebuilder:default=clientId
	ClientID string `json:"clientId,omitempty"`
	// +kubebuilder:default=clientSecret
	ClientSecret string `json:"clientSecret,omitempty"`
}

type Health struct {
	// +kubebuilder:default="/"
	Path string `json:"path,omitempty"`
	// TimeoutSeconds after which an unhealthy app needs attention.
	// +kubebuilder:default=300
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

// +kubebuilder:validation:Enum=Installing;Ready;Updating;RollingBack;Restoring;NeedsAttention;Removing
type Phase string

const (
	PhaseInstalling     Phase = "Installing"
	PhaseReady          Phase = "Ready"
	PhaseUpdating       Phase = "Updating"
	PhaseRollingBack    Phase = "RollingBack"
	PhaseRestoring      Phase = "Restoring"
	PhaseNeedsAttention Phase = "NeedsAttention"
	PhaseRemoving       Phase = "Removing"
)

type AppStatus struct {
	Phase              Phase  `json:"phase,omitempty"`
	Version            string `json:"version,omitempty"`
	URL                string `json:"url,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	// LastBackup is the newest completed backup.
	LastBackup *LastBackup `json:"lastBackup,omitempty"`
	// PausedBy names the Restore the operator stopped managing the app for.
	PausedBy string `json:"pausedBy,omitempty"`
	// WorkloadGeneration is the spec generation the operator configured the
	// workloads for (for Helm apps: and Flux applied) since the app was last
	// paused; 0 while paused.
	WorkloadGeneration int64 `json:"workloadGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type LastBackup struct {
	Name string      `json:"name"`
	At   metav1.Time `json:"at"`
}

// +kubebuilder:object:root=true
type AppList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []App `json:"items"`
}

func init() {
	SchemeBuilder.Register(&App{}, &AppList{})
}
