// Copyright 2026.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AgentRuntime struct {
	// Image contains polis-worker and the selected agent runtime.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Command is the runtime argv launched by polis-worker for each incarnation.
	// +kubebuilder:validation:MinItems=1
	Command []string `json:"command"`
}

type AgentMessaging struct {
	// AllowedRecipients is the exact set of agent IDs this agent may message.
	// Self-messaging requires the agent's own ID. An empty set denies all
	// agent-authored messages.
	// +listType=set
	// +optional
	AllowedRecipients []string `json:"allowedRecipients,omitempty"`
}

type AgentSpec struct {
	// Charter is appended to the runtime's system prompt.
	// +kubebuilder:validation:MinLength=1
	Charter string `json:"charter"`

	// AdditionalInstructions are appended at the end of the runtime's system
	// prompt, after the standard Polis guidance. They are optional and may
	// contain runtime-specific guidance.
	// +kubebuilder:validation:MinLength=1
	// +optional
	AdditionalInstructions string `json:"additionalInstructions,omitempty"`

	Runtime AgentRuntime `json:"runtime"`

	// Messaging restricts agent-authored messages. When omitted, outbound
	// messaging is unrestricted for backward compatibility. Operator messages
	// and messages received by this agent are unaffected.
	// +optional
	Messaging *AgentMessaging `json:"messaging,omitempty"`

	// PodTemplate carries ordinary Kubernetes pod settings. Its pod spec must
	// supply a volume named workspace, which Polis mounts at /workspace. The pod
	// spec may also contain one container named agent; Polis injects the worker
	// contract. Additional volumes and init containers are preserved.
	PodTemplate corev1.PodTemplateSpec `json:"podTemplate"`
}

type AgentStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Deployment string `json:"deployment,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pa
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=`.spec.runtime.image`

type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}
