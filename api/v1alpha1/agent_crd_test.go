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
	"os"
	"testing"

	extensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func TestAgentCRDPreservesEmbeddedTemplateMetadata(t *testing.T) {
	manifest, err := os.ReadFile("../../config/crd/bases/polis.dev_agents.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd extensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(manifest, &crd); err != nil {
		t.Fatal(err)
	}
	if len(crd.Spec.Versions) != 1 || crd.Spec.Versions[0].Schema == nil || crd.Spec.Versions[0].Schema.OpenAPIV3Schema == nil {
		t.Fatalf("Agent CRD does not have exactly one structural schema: %#v", crd.Spec.Versions)
	}

	root := *crd.Spec.Versions[0].Schema.OpenAPIV3Schema
	spec := requiredProperty(t, root, "spec")
	claims := requiredProperty(t, spec, "volumeClaimTemplates")
	if claims.Items == nil || claims.Items.Schema == nil {
		t.Fatal("volumeClaimTemplates does not define an item schema")
	}
	claimMetadata := requiredProperty(t, *claims.Items.Schema, "metadata")
	for _, field := range []string{"name", "labels", "annotations"} {
		requiredProperty(t, claimMetadata, field)
	}

	podTemplate := requiredProperty(t, spec, "podTemplate")
	podMetadata := requiredProperty(t, podTemplate, "metadata")
	for _, field := range []string{"labels", "annotations"} {
		requiredProperty(t, podMetadata, field)
	}

	messaging := requiredProperty(t, spec, "messaging")
	allowedRecipients := requiredProperty(t, messaging, "allowedRecipients")
	if allowedRecipients.XListType == nil || *allowedRecipients.XListType != "set" {
		t.Fatalf("allowedRecipients is not a Kubernetes set: %#v", allowedRecipients.XListType)
	}
}

func requiredProperty(t *testing.T, schema extensionsv1.JSONSchemaProps, name string) extensionsv1.JSONSchemaProps {
	t.Helper()
	property, ok := schema.Properties[name]
	if !ok {
		t.Fatalf("schema is missing property %q", name)
	}
	return property
}
