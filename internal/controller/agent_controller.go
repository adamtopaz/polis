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
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	polisv1alpha1 "github.com/adamtopaz/polis/api/v1alpha1"
)

const (
	readyCondition    = "Ready"
	workspacePath     = "/workspace"
	workerAuthPath    = "/run/polis-worker-auth"
	defaultWorkerName = "polis-worker"
)

var (
	dnsLabel        = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	reservedVolumes = []string{"workspace", "tmp", "worker-auth-source", "worker-auth"}
	reservedEnv     = []string{
		"POLIS_URL", "POLIS_AGENT_ID", "POLIS_CHARTER", "POLIS_WORKSPACE", "POLIS_LEASE_DURATION",
		"POLIS_SHUTDOWN_GRACE", "POLIS_WORKER_TOKEN", "POLIS_WORKER_TOKEN_FILE", "POLIS_ALLOWED_RECIPIENTS",
	}
)

type AgentReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	MailboxURL   string
	WorkerSecret string
}

// +kubebuilder:rbac:groups=polis.dev,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=polis.dev,resources=agents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=polis.dev,resources=agents/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch

func (r *AgentReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	var agent polisv1alpha1.Agent
	if err := r.Get(ctx, request.NamespacedName, &agent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	claims, err := r.reconcile(ctx, &agent)
	if err != nil {
		statusErr := r.setStatus(ctx, &agent, metav1.ConditionFalse, "ReconcileFailed", err.Error(), nil)
		log.Error(err, "unable to reconcile Agent")
		return ctrl.Result{}, errors.Join(err, statusErr)
	}
	if err := r.setStatus(ctx, &agent, metav1.ConditionTrue, "Reconciled", "Agent topology is reconciled", claims); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AgentReconciler) reconcile(ctx context.Context, agent *polisv1alpha1.Agent) ([]string, error) {
	templateNames, claimNames, err := validateAgent(agent)
	if err != nil {
		return nil, err
	}
	for i := range agent.Spec.VolumeClaimTemplates {
		if err := r.reconcileClaim(ctx, agent, &agent.Spec.VolumeClaimTemplates[i], claimNames[i]); err != nil {
			return nil, err
		}
	}
	if err := r.reconcileDeployment(ctx, agent, templateNames, claimNames); err != nil {
		return nil, err
	}
	return claimNames, nil
}

func (r *AgentReconciler) reconcileClaim(ctx context.Context, agent *polisv1alpha1.Agent, template *corev1.PersistentVolumeClaim, name string) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agent.Namespace}}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, claim, func() error {
			claim.Labels = mergeStringMaps(mergeStringMaps(claim.Labels, template.Labels), map[string]string{
				"app.kubernetes.io/name":      "polis",
				"app.kubernetes.io/component": "agent-workspace",
				"polis.dev/agent":             agent.Name,
			})
			claim.Annotations = mergeStringMaps(claim.Annotations, template.Annotations)
			if claim.ResourceVersion == "" {
				claim.Spec = *template.Spec.DeepCopy()
			} else if requested, ok := template.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
				if claim.Spec.Resources.Requests == nil {
					claim.Spec.Resources.Requests = corev1.ResourceList{}
				}
				claim.Spec.Resources.Requests[corev1.ResourceStorage] = requested
			}
			return nil
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("reconcile PersistentVolumeClaim %s: %w", name, err)
	}
	return nil
}

func (r *AgentReconciler) reconcileDeployment(ctx context.Context, agent *polisv1alpha1.Agent, templateNames, claimNames []string) error {
	name := "polis-agent-" + agent.Name
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: agent.Namespace}}
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
			template, err := r.podTemplate(agent, templateNames, claimNames)
			if err != nil {
				return err
			}
			labels := agentLabels(agent.Name)
			deployment.Labels = mergeStringMaps(deployment.Labels, labels)
			deployment.Spec = appsv1.DeploymentSpec{
				Replicas: ptr.To[int32](1),
				Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
				Selector: &metav1.LabelSelector{MatchLabels: labels},
				Template: template,
			}
			return controllerutil.SetControllerReference(agent, deployment, r.Scheme)
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("reconcile Deployment %s: %w", name, err)
	}
	return nil
}

func (r *AgentReconciler) podTemplate(agent *polisv1alpha1.Agent, templateNames, claimNames []string) (corev1.PodTemplateSpec, error) {
	template := *agent.Spec.PodTemplate.DeepCopy()
	template.Labels = mergeStringMaps(template.Labels, agentLabels(agent.Name))
	template.Annotations = mergeStringMaps(template.Annotations, map[string]string{
		"polis.dev/agent-generation": strconv.FormatInt(agent.Generation, 10),
	})
	if template.Spec.TerminationGracePeriodSeconds == nil {
		template.Spec.TerminationGracePeriodSeconds = ptr.To[int64](45)
	}
	if template.Spec.SecurityContext == nil {
		template.Spec.SecurityContext = &corev1.PodSecurityContext{
			FSGroup:             ptr.To[int64](10001),
			FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
			SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		}
	}

	container := corev1.Container{Name: "agent"}
	if len(template.Spec.Containers) == 1 {
		container = *template.Spec.Containers[0].DeepCopy()
	}
	container.Name = "agent"
	container.Image = agent.Spec.Runtime.Image
	container.Args = append([]string{"--"}, agent.Spec.Runtime.Command...)
	if container.ImagePullPolicy == "" {
		container.ImagePullPolicy = corev1.PullAlways
	}
	container.Command = []string{"/bin/polis-worker"}
	if container.WorkingDir == "" {
		container.WorkingDir = workspacePath
	}
	if reflect.DeepEqual(container.Resources, corev1.ResourceRequirements{}) {
		container.Resources = defaultResources()
	}
	if container.SecurityContext == nil {
		container.SecurityContext = restrictedSecurityContext()
	}
	workerEnvironment := []corev1.EnvVar{
		corev1.EnvVar{Name: "POLIS_URL", Value: r.mailboxURL()},
		corev1.EnvVar{Name: "POLIS_AGENT_ID", Value: agent.Name},
		corev1.EnvVar{Name: "POLIS_CHARTER", Value: agent.Spec.Charter},
		corev1.EnvVar{Name: "POLIS_WORKSPACE", Value: workspacePath},
		corev1.EnvVar{Name: "POLIS_LEASE_DURATION", Value: "30s"},
		corev1.EnvVar{Name: "POLIS_SHUTDOWN_GRACE", Value: "10s"},
		corev1.EnvVar{Name: "POLIS_WORKER_TOKEN_FILE", Value: workerAuthPath + "/token"},
	}
	if agent.Spec.Messaging != nil {
		recipients := agent.Spec.Messaging.AllowedRecipients
		if recipients == nil {
			recipients = []string{}
		}
		encoded, err := json.Marshal(recipients)
		if err != nil {
			return corev1.PodTemplateSpec{}, fmt.Errorf("encode messaging policy: %w", err)
		}
		workerEnvironment = append(workerEnvironment, corev1.EnvVar{Name: "POLIS_ALLOWED_RECIPIENTS", Value: string(encoded)})
	}
	container.Env = appendWithoutNamed(container.Env, reservedEnv, workerEnvironment...)
	container.VolumeMounts = appendWithoutNamed(container.VolumeMounts, reservedVolumes,
		corev1.VolumeMount{Name: "workspace", MountPath: workspacePath},
		corev1.VolumeMount{Name: "tmp", MountPath: "/tmp"},
		corev1.VolumeMount{Name: "worker-auth", MountPath: workerAuthPath},
	)
	template.Spec.Containers = []corev1.Container{container}

	workerInit := corev1.Container{
		Name:            "prepare-worker-auth",
		Image:           agent.Spec.Runtime.Image,
		ImagePullPolicy: corev1.PullAlways,
		Command:         []string{"/bin/bash", "-ceu"},
		Args: []string{`cp /run/secrets/worker-source/token /run/polis-worker-auth/token
chmod 0600 /run/polis-worker-auth/token
`},
		SecurityContext: restrictedSecurityContext(),
		VolumeMounts: []corev1.VolumeMount{
			{Name: "worker-auth-source", MountPath: "/run/secrets/worker-source", ReadOnly: true},
			{Name: "worker-auth", MountPath: workerAuthPath},
		},
	}
	template.Spec.InitContainers = appendWithoutNamed(template.Spec.InitContainers, []string{"prepare-worker-auth"}, workerInit)

	privateVolumes := make([]corev1.Volume, 0, len(claimNames))
	for i, name := range claimNames {
		privateVolumes = append(privateVolumes, corev1.Volume{
			Name:         templateNames[i],
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: name}},
		})
	}
	reserved := append(append([]string(nil), reservedVolumes...), templateNames...)
	template.Spec.Volumes = appendWithoutNamed(template.Spec.Volumes, reserved,
		append(privateVolumes,
			corev1.Volume{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			corev1.Volume{Name: "worker-auth-source", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: r.workerSecret(), DefaultMode: ptr.To[int32](0o440),
				Items: []corev1.KeyToPath{{Key: "token", Path: "token"}},
			}}},
			corev1.Volume{Name: "worker-auth", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		)...,
	)
	return template, nil
}

func validateAgent(agent *polisv1alpha1.Agent) ([]string, []string, error) {
	if agent.Name == "" || len(agent.Name) > 50 || !dnsLabel.MatchString(agent.Name) {
		return nil, nil, errors.New("metadata.name must be a DNS label of at most 50 characters")
	}
	if strings.TrimSpace(agent.Spec.Charter) == "" {
		return nil, nil, errors.New("spec.charter is required")
	}
	if strings.TrimSpace(agent.Spec.Runtime.Image) == "" {
		return nil, nil, errors.New("spec.runtime.image is required")
	}
	if len(agent.Spec.Runtime.Command) == 0 || agent.Spec.Runtime.Command[0] == "" {
		return nil, nil, errors.New("spec.runtime.command is required")
	}
	if agent.Spec.Messaging != nil {
		seenRecipients := make(map[string]bool, len(agent.Spec.Messaging.AllowedRecipients))
		for _, recipient := range agent.Spec.Messaging.AllowedRecipients {
			if len(recipient) > 50 || !dnsLabel.MatchString(recipient) {
				return nil, nil, fmt.Errorf("messaging recipient %q must be a DNS label of at most 50 characters", recipient)
			}
			if seenRecipients[recipient] {
				return nil, nil, fmt.Errorf("duplicate messaging recipient %q", recipient)
			}
			seenRecipients[recipient] = true
		}
	}
	if len(agent.Spec.VolumeClaimTemplates) == 0 {
		return nil, nil, errors.New("spec.volumeClaimTemplates must contain a workspace template")
	}
	templateNames := make([]string, 0, len(agent.Spec.VolumeClaimTemplates))
	claimNames := make([]string, 0, len(agent.Spec.VolumeClaimTemplates))
	seen := make(map[string]bool)
	for _, template := range agent.Spec.VolumeClaimTemplates {
		name := template.Name
		if name == "" || !dnsLabel.MatchString(name) {
			return nil, nil, fmt.Errorf("volume claim template name %q must be a DNS label", name)
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("duplicate volume claim template %q", name)
		}
		seen[name] = true
		claimName := agent.Name + "-" + name
		if len(claimName) > 63 {
			return nil, nil, fmt.Errorf("generated claim name %q exceeds 63 characters", claimName)
		}
		templateNames = append(templateNames, name)
		claimNames = append(claimNames, claimName)
	}
	if !seen["workspace"] {
		return nil, nil, errors.New(`spec.volumeClaimTemplates requires a template named "workspace"`)
	}
	if len(agent.Spec.PodTemplate.Spec.Containers) > 1 ||
		(len(agent.Spec.PodTemplate.Spec.Containers) == 1 && agent.Spec.PodTemplate.Spec.Containers[0].Name != "agent") {
		return nil, nil, errors.New(`spec.podTemplate.spec.containers may contain only one container named "agent"`)
	}
	for _, container := range agent.Spec.PodTemplate.Spec.InitContainers {
		if container.Name == "prepare-worker-auth" {
			return nil, nil, errors.New(`init container name "prepare-worker-auth" is reserved`)
		}
	}
	for _, volume := range agent.Spec.PodTemplate.Spec.Volumes {
		if slices.Contains(reservedVolumes, volume.Name) || seen[volume.Name] {
			return nil, nil, fmt.Errorf("pod volume name %q is reserved", volume.Name)
		}
	}
	return templateNames, claimNames, nil
}

func (r *AgentReconciler) setStatus(ctx context.Context, agent *polisv1alpha1.Agent, conditionStatus metav1.ConditionStatus, reason, message string, claims []string) error {
	original := agent.DeepCopy()
	agent.Status.ObservedGeneration = agent.Generation
	if conditionStatus == metav1.ConditionTrue {
		agent.Status.Deployment = "polis-agent-" + agent.Name
		agent.Status.Claims = append([]string(nil), claims...)
	}
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type: readyCondition, Status: conditionStatus, ObservedGeneration: agent.Generation,
		Reason: reason, Message: message,
	})
	if reflect.DeepEqual(original.Status, agent.Status) {
		return nil
	}
	return r.Status().Patch(ctx, agent, client.MergeFrom(original))
}

func (r *AgentReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&polisv1alpha1.Agent{}).
		Owns(&appsv1.Deployment{}).
		Watches(&corev1.PersistentVolumeClaim{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, object client.Object) []ctrl.Request {
			agent := object.GetLabels()["polis.dev/agent"]
			if agent == "" {
				return nil
			}
			return []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: object.GetNamespace(), Name: agent}}}
		})).
		Named("agent").
		Complete(r)
}

func (r *AgentReconciler) mailboxURL() string {
	if r.MailboxURL != "" {
		return r.MailboxURL
	}
	return "http://polis-mailbox"
}

func (r *AgentReconciler) workerSecret() string {
	if r.WorkerSecret != "" {
		return r.WorkerSecret
	}
	return defaultWorkerName
}

func agentLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "polis",
		"app.kubernetes.io/component": "agent",
		"polis.dev/agent":             name,
	}
}

func mergeStringMaps(first, second map[string]string) map[string]string {
	result := make(map[string]string, len(first)+len(second))
	for key, value := range first {
		result[key] = value
	}
	for key, value := range second {
		result[key] = value
	}
	return result
}

func appendWithoutNamed[T any](existing []T, reserved []string, appended ...T) []T {
	result := make([]T, 0, len(existing)+len(appended))
	for _, item := range existing {
		if !slices.Contains(reserved, objectName(any(item))) {
			result = append(result, item)
		}
	}
	return append(result, appended...)
}

func objectName(object any) string {
	switch value := object.(type) {
	case corev1.Container:
		return value.Name
	case corev1.EnvVar:
		return value.Name
	case corev1.Volume:
		return value.Name
	case corev1.VolumeMount:
		return value.Name
	default:
		return ""
	}
}

func restrictedSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To[int64](10001),
		RunAsGroup:               ptr.To[int64](10001),
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func defaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resourceQuantity("10m"),
			corev1.ResourceMemory: resourceQuantity("24Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resourceQuantity("1"),
			corev1.ResourceMemory: resourceQuantity("1Gi"),
		},
	}
}

func resourceQuantity(value string) resource.Quantity {
	return resource.MustParse(value)
}
