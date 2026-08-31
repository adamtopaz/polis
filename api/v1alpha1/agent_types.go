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

type AgentSpec struct {
	// Charter is appended to the runtime's system prompt.
	// +kubebuilder:validation:MinLength=1
	Charter string `json:"charter"`

	Runtime AgentRuntime `json:"runtime"`

	// VolumeClaimTemplates creates retained, agent-private claims named
	// <agent>-<template>. A template named workspace is required and is mounted
	// at /workspace. Other templates can be mounted through PodTemplate.
	// +kubebuilder:validation:MinItems=1
	VolumeClaimTemplates []corev1.PersistentVolumeClaim `json:"volumeClaimTemplates"`

	// PodTemplate carries ordinary Kubernetes pod settings. When supplied, its
	// pod spec contains exactly one container named agent; Polis injects the
	// worker contract. Additional volumes and init containers are preserved.
	// +optional
	PodTemplate corev1.PodTemplateSpec `json:"podTemplate,omitempty"`
}

type AgentStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	Deployment string `json:"deployment,omitempty"`

	Claims []string `json:"claims,omitempty"`

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
