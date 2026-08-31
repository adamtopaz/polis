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

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	polisv1alpha1 "github.com/adamtopaz/polis/api/v1alpha1"
)

func TestAgentReconcilerCreatesDedicatedTopologyFromSuppliedWorkspace(t *testing.T) {
	scheme := testScheme(t)
	agent := testAgent()
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&polisv1alpha1.Agent{}).WithObjects(agent).Build()
	reconciler := &AgentReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("idempotent reconciliation: %v", err)
	}

	var deployment appsv1.Deployment
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: "polis", Name: "polis-agent-researcher"}, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 || deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("deployment does not enforce one Recreate replica: %#v", deployment.Spec)
	}
	if deployment.Spec.Template.Annotations["polis.dev/agent-generation"] != "1" {
		t.Fatalf("pod template does not track Agent generation: %#v", deployment.Spec.Template.Annotations)
	}
	if len(deployment.Spec.Template.Spec.Containers) != 1 || deployment.Spec.Template.Spec.Containers[0].Name != "agent" {
		t.Fatalf("containers = %#v", deployment.Spec.Template.Spec.Containers)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "ghcr.io/adamtopaz/polis-pi:main" || !hasVolumeMount(container.VolumeMounts, "workspace") || !hasVolumeMount(container.VolumeMounts, "shared-research") {
		t.Fatalf("agent container = %#v", container)
	}
	if !slices.Equal(container.Args, []string{"--", "/bin/polis-pi-agent", "--thinking", "high"}) ||
		envValue(container.Env, "POLIS_CHARTER") != agent.Spec.Charter ||
		envValue(container.Env, "POLIS_ADDITIONAL_INSTRUCTIONS") != agent.Spec.AdditionalInstructions ||
		envValue(container.Env, "POLIS_WAKEUP_SECONDS") != "120" ||
		envValue(container.Env, "POLIS_URL") != "http://polis-mailbox" {
		t.Fatalf("runtime configuration was not projected into the pod: %#v", container)
	}
	if got := volumeClaimName(deployment.Spec.Template.Spec.Volumes, "workspace"); got != "durable-research" {
		t.Fatalf("workspace claim = %q, want independently named durable-research", got)
	}
	assertRuntimeIdentity(t, "agent", container.SecurityContext)
	if len(deployment.Spec.Template.Spec.InitContainers) != 1 {
		t.Fatalf("init containers = %#v", deployment.Spec.Template.Spec.InitContainers)
	}
	workerInit := deployment.Spec.Template.Spec.InitContainers[0]
	if workerInit.Name != "prepare-worker-auth" {
		t.Fatalf("worker credential init container = %#v", workerInit)
	}
	assertRuntimeIdentity(t, workerInit.Name, workerInit.SecurityContext)
	encoded, err := json.Marshal(deployment)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "polis-operator") {
		t.Fatalf("operator credential leaked into agent Deployment: %s", encoded)
	}

	var claims corev1.PersistentVolumeClaimList
	if err := client.List(context.Background(), &claims); err != nil {
		t.Fatal(err)
	}
	if len(claims.Items) != 0 {
		t.Fatalf("controller created or changed PVCs: %#v", claims.Items)
	}

	var reconciled polisv1alpha1.Agent
	if err := client.Get(context.Background(), request.NamespacedName, &reconciled); err != nil {
		t.Fatal(err)
	}
	if len(reconciled.Status.Conditions) != 1 || reconciled.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("status = %#v", reconciled.Status)
	}
}

func TestAgentReconcilerRejectsMultiplePersistentContainers(t *testing.T) {
	scheme := testScheme(t)
	agent := testAgent()
	agent.Spec.PodTemplate.Spec.Containers = append(agent.Spec.PodTemplate.Spec.Containers, corev1.Container{Name: "second-agent"})
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&polisv1alpha1.Agent{}).WithObjects(agent).Build()
	reconciler := &AgentReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err == nil {
		t.Fatal("invalid multi-agent pod was reconciled")
	}
	var reconciled polisv1alpha1.Agent
	if err := client.Get(context.Background(), request.NamespacedName, &reconciled); err != nil {
		t.Fatal(err)
	}
	if len(reconciled.Status.Conditions) != 1 || reconciled.Status.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("status = %#v", reconciled.Status)
	}
}

func TestAgentReconcilerRequiresSuppliedWorkspaceVolume(t *testing.T) {
	scheme := testScheme(t)
	agent := testAgent()
	agent.Spec.PodTemplate.Spec.Volumes = slices.DeleteFunc(agent.Spec.PodTemplate.Spec.Volumes, func(volume corev1.Volume) bool {
		return volume.Name == "workspace"
	})
	client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&polisv1alpha1.Agent{}).WithObjects(agent).Build()
	reconciler := &AgentReconciler{Client: client, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err == nil {
		t.Fatal("Agent without a supplied workspace volume was reconciled")
	}
	var reconciled polisv1alpha1.Agent
	if err := client.Get(context.Background(), request.NamespacedName, &reconciled); err != nil {
		t.Fatal(err)
	}
	if len(reconciled.Status.Conditions) != 1 || reconciled.Status.Conditions[0].Status != metav1.ConditionFalse {
		t.Fatalf("status = %#v", reconciled.Status)
	}
}

func TestAgentAdditionalInstructionsAreOptionalButNotBlank(t *testing.T) {
	agent := testAgent()
	agent.Spec.AdditionalInstructions = ""
	if err := validateAgent(agent); err != nil {
		t.Fatalf("omitted additional instructions were rejected: %v", err)
	}
	agent.Spec.AdditionalInstructions = " \n\t"
	if err := validateAgent(agent); err == nil {
		t.Fatal("blank additional instructions were accepted")
	}
}

func TestAgentWakeupIsOptionalButPositive(t *testing.T) {
	agent := testAgent()
	agent.Spec.Wakeup = nil
	if err := validateAgent(agent); err != nil {
		t.Fatalf("omitted wakeup was rejected: %v", err)
	}
	template, err := (&AgentReconciler{}).podTemplate(agent)
	if err != nil {
		t.Fatal(err)
	}
	if hasEnv(template.Spec.Containers[0].Env, "POLIS_WAKEUP_SECONDS") {
		t.Fatal("omitted wakeup was projected into the pod")
	}

	agent.Spec.Wakeup = ptr.To[int64](0)
	if err := validateAgent(agent); err == nil {
		t.Fatal("zero wakeup was accepted")
	}
	agent.Spec.Wakeup = ptr.To[int64](-1)
	if err := validateAgent(agent); err == nil {
		t.Fatal("negative wakeup was accepted")
	}
}

func TestAgentReconcilerProjectsMessagingPolicy(t *testing.T) {
	agent := testAgent()
	agent.Spec.PodTemplate.Spec.Containers[0].Env = append(agent.Spec.PodTemplate.Spec.Containers[0].Env,
		corev1.EnvVar{Name: "POLIS_ALLOWED_RECIPIENTS", Value: `["forged"]`})
	agent.Spec.Messaging = &polisv1alpha1.AgentMessaging{AllowedRecipients: []string{"reviewer", "researcher"}}
	if err := validateAgent(agent); err != nil {
		t.Fatal(err)
	}
	template, err := (&AgentReconciler{}).podTemplate(agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(template.Spec.Containers[0].Env, "POLIS_ALLOWED_RECIPIENTS"); got != `["reviewer","researcher"]` {
		t.Fatalf("projected messaging policy = %q", got)
	}

	agent.Spec.Messaging = &polisv1alpha1.AgentMessaging{}
	template, err = (&AgentReconciler{}).podTemplate(agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(template.Spec.Containers[0].Env, "POLIS_ALLOWED_RECIPIENTS"); got != `[]` {
		t.Fatalf("empty messaging policy = %q", got)
	}

	agent.Spec.Messaging = nil
	template, err = (&AgentReconciler{}).podTemplate(agent)
	if err != nil {
		t.Fatal(err)
	}
	if got := envValue(template.Spec.Containers[0].Env, "POLIS_ALLOWED_RECIPIENTS"); got != "" {
		t.Fatalf("omitted messaging policy was not unrestricted: %q", got)
	}
}

func TestAgentReconcilerRetriesDeploymentConflicts(t *testing.T) {
	scheme := testScheme(t)
	agent := testAgent()
	stale := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "polis-agent-researcher", Namespace: "polis",
	}}
	base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&polisv1alpha1.Agent{}).WithObjects(agent, stale).Build()
	conflicting := &conflictOnceClient{Client: base}

	reconciler := &AgentReconciler{Client: conflicting, Scheme: scheme}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("reconcile did not absorb a transient Deployment conflict: %v", err)
	}
	if !conflicting.returnedConflict {
		t.Fatal("test client did not inject a Deployment conflict")
	}

	var deployment appsv1.Deployment
	if err := base.Get(context.Background(), types.NamespacedName{Namespace: "polis", Name: "polis-agent-researcher"}, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("Deployment was not reconciled after conflict: %#v", deployment.Spec)
	}
}

type conflictOnceClient struct {
	client.Client
	returnedConflict bool
}

func (c *conflictOnceClient) Update(ctx context.Context, object client.Object, options ...client.UpdateOption) error {
	if _, ok := object.(*appsv1.Deployment); ok && !c.returnedConflict {
		c.returnedConflict = true
		return apierrors.NewConflict(
			schema.GroupResource{Group: "apps", Resource: "deployments"},
			object.GetName(),
			errors.New("injected conflict"),
		)
	}
	return c.Client.Update(ctx, object, options...)
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, polisv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func testAgent() *polisv1alpha1.Agent {
	return &polisv1alpha1.Agent{
		TypeMeta:   metav1.TypeMeta{APIVersion: "polis.dev/v1alpha1", Kind: "Agent"},
		ObjectMeta: metav1.ObjectMeta{Name: "researcher", Namespace: "polis", UID: "researcher-uid", Generation: 1},
		Spec: polisv1alpha1.AgentSpec{
			Charter:                "Research useful things.",
			AdditionalInstructions: "Keep reports concise.",
			Wakeup:                 ptr.To[int64](120),
			Runtime: polisv1alpha1.AgentRuntime{
				Image:   "ghcr.io/adamtopaz/polis-pi:main",
				Command: []string{"/bin/polis-pi-agent", "--thinking", "high"},
			},
			PodTemplate: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "agent",
					Env: []corev1.EnvVar{
						{Name: "POLIS_ADDITIONAL_INSTRUCTIONS", Value: "forged instructions"},
						{Name: "POLIS_WAKEUP_SECONDS", Value: "999"},
					},
					VolumeMounts: []corev1.VolumeMount{{Name: "shared-research", MountPath: "/workspace/shared"}},
				}},
				Volumes: []corev1.Volume{
					{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "durable-research"}}},
					{Name: "shared-research", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "shared-research"}}},
				},
			}},
		},
	}
}

func hasVolumeMount(mounts []corev1.VolumeMount, name string) bool {
	for _, mount := range mounts {
		if mount.Name == name {
			return true
		}
	}
	return false
}

func volumeClaimName(volumes []corev1.Volume, name string) string {
	for _, volume := range volumes {
		if volume.Name == name && volume.PersistentVolumeClaim != nil {
			return volume.PersistentVolumeClaim.ClaimName
		}
	}
	return ""
}

func envValue(environment []corev1.EnvVar, name string) string {
	for _, variable := range environment {
		if variable.Name == name {
			return variable.Value
		}
	}
	return ""
}

func hasEnv(environment []corev1.EnvVar, name string) bool {
	for _, variable := range environment {
		if variable.Name == name {
			return true
		}
	}
	return false
}

func assertRuntimeIdentity(t *testing.T, name string, securityContext *corev1.SecurityContext) {
	t.Helper()
	if securityContext == nil || securityContext.RunAsUser == nil || *securityContext.RunAsUser != 10001 ||
		securityContext.RunAsGroup == nil || *securityContext.RunAsGroup != 10001 {
		t.Fatalf("%s runtime identity = %#v, want UID:GID 10001:10001", name, securityContext)
	}
}
