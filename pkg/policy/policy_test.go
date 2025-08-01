package policy_test

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bluele/gcache"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aquasecurity/trivy-operator/pkg/apis/aquasecurity/v1alpha1"
	"github.com/aquasecurity/trivy-operator/pkg/kube"
	"github.com/aquasecurity/trivy-operator/pkg/plugins/trivy"
	"github.com/aquasecurity/trivy-operator/pkg/policy"
	"github.com/aquasecurity/trivy-operator/pkg/utils"
	"github.com/aquasecurity/trivy/pkg/fanal/types"
	"github.com/aquasecurity/trivy/pkg/iac/scan"

	. "github.com/onsi/gomega"
)

var (
	cacheReportTTL = time.Hour * 24

	simpleNginxPod = &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "nginx:1.16",
				},
			},
		},
	}
)

func TestPolicies_Hash(t *testing.T) {
	t.Run("Should return hash for valid policies", func(t *testing.T) {
		g := NewGomegaWithT(t)
		config := policy.NewPolicies(map[string]string{
			"policy.valid.rego":  "<REGO_CONTENT>",
			"policy.valid.kinds": "Pod",
		}, testConfig{}, ctrl.Log.WithName("policy logger"), policy.NewPolicyLoader("", gcache.New(1).LRU().Build(), types.RegistryOptions{}), "1.27.1", &cacheReportTTL)

		hash, err := config.Hash("Pod")
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(hash).ToNot(BeEmpty())
	})
	t.Run("Should compute correct hash for given policies", func(t *testing.T) {
		g := NewGomegaWithT(t)
		policies := map[string]string{
			"policy.policy1.rego":  "package test\nallow = true",
			"policy.policy1.kinds": "Pod",
			"policy.policy2.rego":  "package test\nallow = false",
			"policy.policy2.kinds": "Workload",
		}
		config := policy.NewPolicies(policies, testConfig{}, ctrl.Log.WithName("policy logger"), policy.NewPolicyLoader("", gcache.New(1).LRU().Build(), types.RegistryOptions{}), "1.27.1", &cacheReportTTL)

		// Sort policy content before computing the expected hash
		policyContents := []string{
			"package test\nallow = true",
			"package test\nallow = false",
		}
		sort.Strings(policyContents)

		expectedHash := kube.ComputeHash(policyContents)

		hash, err := config.Hash("Pod")
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(hash).To(Equal(expectedHash))
	})
}

func TestPolicies_PoliciesByKind(t *testing.T) {
	t.Run("Should return error when kinds are not defined for policy", func(t *testing.T) {
		g := NewGomegaWithT(t)
		config := policy.NewPolicies(map[string]string{
			"library.kubernetes.rego":        "<REGO_A>",
			"library.utils.rego":             "<REGO_B>",
			"policy.access_to_host_pid.rego": "<REGO_C>",
		}, testConfig{}, ctrl.Log.WithName("policy logger"), policy.NewPolicyLoader("", gcache.New(1).LRU().Build(), types.RegistryOptions{}), "1.27.1", &cacheReportTTL)
		_, err := config.PoliciesByKind("Pod")
		g.Expect(err).To(MatchError("kinds not defined for policy: policy.access_to_host_pid.rego"))
	})

	t.Run("Should return error when policy is not found", func(t *testing.T) {
		g := NewGomegaWithT(t)
		config := policy.NewPolicies(map[string]string{
			"policy.access_to_host_pid.kinds": "Workload",
		}, testConfig{}, ctrl.Log.WithName("policy logger"), policy.NewPolicyLoader("", gcache.New(1).LRU().Build(), types.RegistryOptions{}), "1.27.1", &cacheReportTTL)
		_, err := config.PoliciesByKind("Pod")
		g.Expect(err).To(MatchError("expected policy not found: policy.access_to_host_pid.rego"))
	})

	t.Run("Should return policies as Rego modules", func(t *testing.T) {

		g := NewGomegaWithT(t)
		config := policy.NewPolicies(map[string]string{
			"library.kubernetes.rego":                       "<REGO_A>",
			"library.utils.rego":                            "<REGO_B>",
			"policy.access_to_host_pid.rego":                "<REGO_C>",
			"policy.cpu_not_limited.rego":                   "<REGO_D>",
			"policy.configmap_with_sensitive_data.rego":     "<REGO_E>",
			"policy.configmap_with_secret_data.rego":        "<REGO_F>",
			"policy.object_without_recommended_labels.rego": "<REGO_G>",

			"policy.access_to_host_pid.kinds":                "Pod,ReplicaSet",
			"policy.cpu_not_limited.kinds":                   "Workload",
			"policy.configmap_with_sensitive_data.kinds":     "ConfigMap",
			"policy.configmap_with_secret_data.kinds":        "ConfigMap",
			"policy.object_without_recommended_labels.kinds": "*",

			// This one should be skipped (no .rego suffix)
			"policy.privileged": "<REGO_E>",
			// This one should be skipped (no policy. prefix)
			"foo": "bar",
		}, testConfig{}, ctrl.Log.WithName("policy logger"), policy.NewPolicyLoader("", gcache.New(1).LRU().Build(), types.RegistryOptions{}), "1.27.1", &cacheReportTTL)
		g.Expect(config.PoliciesByKind("Pod")).To(Equal(map[string]string{
			"policy.access_to_host_pid.rego":                "<REGO_C>",
			"policy.cpu_not_limited.rego":                   "<REGO_D>",
			"policy.object_without_recommended_labels.rego": "<REGO_G>",
		}))
		g.Expect(config.PoliciesByKind("ConfigMap")).To(Equal(map[string]string{
			"policy.configmap_with_sensitive_data.rego":     "<REGO_E>",
			"policy.configmap_with_secret_data.rego":        "<REGO_F>",
			"policy.object_without_recommended_labels.rego": "<REGO_G>",
		}))
	})
}

func TestPolicies_Supported(t *testing.T) {

	testCases := []struct {
		name       string
		data       map[string]string
		resource   client.Object
		rbacEnable bool
		expected   bool
	}{
		{
			name: "Should return true for workload policies",
			data: make(map[string]string),
			resource: &corev1.Pod{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Pod",
					APIVersion: "v1",
				},
			},
			rbacEnable: true,
			expected:   true,
		},
		{
			name: "Should return true if there is at least one policy",
			resource: &corev1.Pod{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Pod",
					APIVersion: "v1",
				},
			},
			rbacEnable: true,
			expected:   true,
		},
		{
			name: "Should return false if Role kind and rbac disable",
			resource: &corev1.Pod{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Role",
					APIVersion: "v1",
				},
			},
			rbacEnable: false,
			expected:   false,
		},
		{
			name: "Should return true if Pod kind and rbac disable",
			resource: &corev1.Pod{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Pod",
					APIVersion: "v1",
				},
			},
			rbacEnable: false,
			expected:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			log := ctrl.Log.WithName("resourcecontroller")
			ready, err := policy.NewPolicies(tc.data, testConfig{}, log, policy.NewPolicyLoader("", gcache.New(1).LRU().Build(), types.RegistryOptions{}), "1.27.1", &cacheReportTTL).SupportedKind(tc.resource, tc.rbacEnable)
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(ready).To(Equal(tc.expected))
		})
	}
}

func TestPolicies_Eval(t *testing.T) {
	testCases := []struct {
		name               string
		resource           client.Object
		policies           map[string]string
		results            Results
		useBuiltInPolicies bool
		expectedError      string
	}{
		{
			name: "Should eval deny rule with invalid resource as failed check",
			resource: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "appsv1",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "nginx",
									Image: "nginx:1.16",
								},
							},
						},
					},
				},
			},
			useBuiltInPolicies: false,
			policies: map[string]string{
				"library.utils.rego": `package lib.utils

		has_key(x, k) {
		  _ = x[k]
		}`,
				"policy.policy1.kinds": "Workload",
				"policy.policy1.rego": `package appshield.kubernetes.KSV014

		__rego_metadata__ := {
			"id": "KSV014",
			"title": "Root file system is not read-only",
			"description": "An immutable root file system prevents applications from writing to their local disk",
			"severity": "LOW",
			"type": "Kubernetes Security Check"
		}

		deny[res] {
			input.kind == "Deployment"
			not input.spec.template.spec.securityContext.runAsNonRoot

			msg := "Containers must not run as root"

			res := {
				"id": __rego_metadata__.id,
				"title": __rego_metadata__.title,
				"severity": __rego_metadata__.severity,
				"type": __rego_metadata__.type,
				"msg": msg
			}
		}
		`,
			},
			results: []Result{
				{
					Success: false,
					Metadata: Metadata{
						ID:          "KSV014",
						Title:       "Root file system is not read-only",
						Description: "An immutable root file system prevents applications from writing to their local disk",
						Severity:    v1alpha1.SeverityLow,
						Type:        "Kubernetes Security Check",
					},
					Messages: []string{"Containers must not run as root"},
				},
			},
		},
		{
			name: "Should eval deny rule with valid resource as successful check",
			resource: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "appsv1",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							SecurityContext: &corev1.PodSecurityContext{
								RunAsNonRoot: ptr.To[bool](true),
							},
							Containers: []corev1.Container{
								{
									Name:  "nginx",
									Image: "nginx:1.16",
								},
							},
						},
					},
				},
			},
			useBuiltInPolicies: false,
			policies: map[string]string{
				"library.utils.rego": `package lib.utils

		has_key(x, k) {
		  _ = x[k]
		}`,
				"policy.policy1.kinds": "Workload",
				"policy.policy1.rego": `package appshield.kubernetes.KSV014

		__rego_metadata__ := {
			"id": "KSV014",
			"title": "Root file system is not read-only",
			"description": "An immutable root file system prevents applications from writing to their local disk",
			"severity": "LOW",
			"type": "Kubernetes Security Check"
		}

		deny[res] {
			input.kind == "Deployment"
			not input.spec.template.spec.securityContext.runAsNonRoot

			msg := "Containers must not run as root"

			res := {
				"id": __rego_metadata__.id,
				"title": __rego_metadata__.title,
				"severity": __rego_metadata__.severity,
				"type": __rego_metadata__.type,
				"msg": msg
			}
		}
		`,
			},
			results: []Result{
				{
					Success: true,
					Metadata: Metadata{
						ID:          "KSV014",
						Severity:    v1alpha1.SeverityLow,
						Title:       "Root file system is not read-only",
						Description: "An immutable root file system prevents applications from writing to their local disk",
						Type:        "Kubernetes Security Check",
					},
				},
			},
		},
		{
			name: "Should return error when policy cannot be parsed",
			resource: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "appsv1",
				},
			},
			useBuiltInPolicies: false,
			policies: map[string]string{
				"policy.invalid.kinds": "Workload",
				"policy.invalid.rego":  "$^&!",
			},
			expectedError: `failed to load rego policies from [externalPolicies]: 1 error occurred: externalPolicies/file_0.rego:1: rego_parse_error: illegal token\\n\\t$^&!\\n\\t^`,
		},
		{
			name: "Should return error when library cannot be parsed",
			resource: &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Deployment",
					APIVersion: "appsv1",
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							SecurityContext: &corev1.PodSecurityContext{
								RunAsNonRoot: ptr.To[bool](true),
							},
							Containers: []corev1.Container{
								{
									Name:  "nginx",
									Image: "nginx:1.16",
								},
							},
						},
					},
				},
			},
			useBuiltInPolicies: false,
			policies: map[string]string{
				"library.utils.rego": "$^&!",
			},
			expectedError: `failed to load rego policies from [externalPolicies]: 1 error occurred: externalPolicies/file_0.rego:1: rego_parse_error: illegal token\\n\\t$^&!\\n\\t^`,
		},
		{
			name: "Should eval warn role rule with built in policies",
			resource: &rbacv1.Role{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Role",
					APIVersion: "rbacv1",
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{"*"},
						Verbs:     []string{"get"},
						Resources: []string{"secrets"}},
				},
			},
			useBuiltInPolicies: true,
			policies:           make(map[string]string),
			results: []Result{
				{
					Metadata: Metadata{
						ID:          "KSV054",
						Title:       "Do not allow attaching to shell on pods",
						Description: "Check whether role permits attaching to shell on pods",
						Severity:    "HIGH",
						Type:        "Kubernetes Security Check",
					},
					Success: true,
				},
			},
		},
		{
			name: "Should eval return error no policies found",
			resource: &rbacv1.Role{
				TypeMeta: metav1.TypeMeta{
					Kind:       "Role",
					APIVersion: "rbacv1",
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{"*"},
						Verbs:     []string{"get"},
						Resources: []string{"secrets"}},
				},
			},
			useBuiltInPolicies: false,
			policies:           make(map[string]string),
			expectedError:      policy.PoliciesNotFoundError,
		},
		{
			name:               "using a custom rule - check passed",
			resource:           simpleNginxPod,
			useBuiltInPolicies: false,
			policies: map[string]string{
				"policy.uses_image_tag_latest.kinds": "Pod",
				"policy.uses_image_tag_latest.rego": `
   package trivyoperator.policy.k8s.custom
   __rego_metadata__ := {
        "id": "CUSTOMCHECK",
        "title": "custom check title",
        "severity": "LOW",
        "type": "Kubernetes Security Check",
        "description": "custom check description",
    }

   alwaysFalse {
      1 == 0
   }

   deny[res] {
        alwaysFalse
		res := {
				"msg": "the check should always pass",
			}
   }`,
			},
			results: []Result{
				{
					Metadata: Metadata{
						ID:          "CUSTOMCHECK",
						Title:       "custom check title",
						Description: "custom check description",
						Severity:    "LOW",
						Type:        "Kubernetes Security Check",
					},
					Success: true,
				},
			},
		},
		{
			name:               "using a custom rule - check failed",
			resource:           simpleNginxPod,
			useBuiltInPolicies: false,
			policies: map[string]string{
				"policy.uses_image_tag_latest.kinds": "Pod",
				"policy.uses_image_tag_latest.rego": `
   package trivyoperator.policy.k8s.custom
   __rego_metadata__ := {
        "id": "CUSTOMCHECK",
        "title": "custom check title",
        "severity": "LOW",
        "type": "Kubernetes Security Check",
        "description": "custom check description",
    }

   alwaysTrue {
      1 == 1
   }

   deny[res] {
        alwaysTrue
		res := {
				"msg": "the check should be always failed",
			}
   }`,
			},
			results: []Result{
				{
					Metadata: Metadata{
						ID:          "CUSTOMCHECK",
						Title:       "custom check title",
						Description: "custom check description",
						Severity:    "LOW",
						Type:        "Kubernetes Security Check",
					},
					Messages: []string{"the check should be always failed"},
					Success:  false,
				},
			},
		},
		{
			name:               "using a custom rule with incorrect name",
			resource:           simpleNginxPod,
			useBuiltInPolicies: false,
			policies: map[string]string{
				"policy.uses_image_tag_latest.kinds": "Pod",
				"policy.uses_image_tag_latest.rego": `
   package myfunnyname.policy.k8s.custom
   __rego_metadata__ := {
        "id": "CUSTOMCHECK",
        "title": "custom check title",
        "severity": "LOW",
        "type": "Kubernetes Security Check",
        "description": "custom check description",
    }

   deny[{"msg": "this message should be hidden"}]`,
			},
			results: []Result{}, // passing an invalid policy should not return any results but not an error
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			log := ctrl.Log.WithName("resourcecontroller")
			p := policy.NewPolicies(tc.policies, newTestConfig(tc.useBuiltInPolicies), log, &TestLoader{}, "1.27.1", &cacheReportTTL)
			g.Expect(p.Load()).ToNot(HaveOccurred())
			checks, err := p.Eval(t.Context(), tc.resource)
			if tc.expectedError != "" {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).ToNot(HaveOccurred())
			g.Expect(getPolicyResults(checks)).Should(Equal(tc.results))
		})
	}
}

func TestNewMetadata(t *testing.T) {
	testCases := []struct {
		name             string
		values           map[string]any
		expectedMetadata Metadata
		expectedError    string
	}{
		{
			name:          "Should return error when value is nil",
			values:        nil,
			expectedError: "values must not be nil",
		},
		{
			name: "Should return error when severity key is not set",
			values: map[string]any{
				"id":          "some id",
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required key not found: severity",
		},
		{
			name: "Should return error when severity value is nil",
			values: map[string]any{
				"severity":    nil,
				"id":          "some id",
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required value is nil for key: severity",
		},
		{
			name: "Should return error when severity value is blank",
			values: map[string]any{
				"severity":    "",
				"id":          "some id",
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required value is blank for key: severity",
		},
		{
			name: "Should return error when severity value is invalid",
			values: map[string]any{
				"severity":    "INVALID",
				"id":          "some id",
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "failed parsing severity: unrecognized name literal: INVALID",
		},
		{
			name: "Should return error when id key is not set",
			values: map[string]any{
				"severity":    "CRITICAL",
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required key not found: id",
		},
		{
			name: "Should return error when id value is nil",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          nil,
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required value is nil for key: id",
		},
		{
			name: "Should return error when id value is blank",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          "",
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required value is blank for key: id",
		},
		{
			name: "Should return error when id value is not string",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          3,
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "expected string got int for key: id",
		},
		{
			name: "Should return error when title key is not set",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          "some id",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required key not found: title",
		},
		{
			name: "Should return error when title value is nil",
			values: map[string]any{
				"severity": "CRITICAL",
				"id":       "KVH012",
				"title":    nil,
			},
			expectedError: "required value is nil for key: title",
		},
		{
			name: "Should return error when title value is blank",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          "some id",
				"title":       "",
				"type":        "some type",
				"description": "some description",
			},
			expectedError: "required value is blank for key: title",
		},
		{
			name: "Should return error when type key is not set",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          "some id",
				"title":       "some title",
				"description": "some description",
			},
			expectedError: "required key not found: type",
		},
		{
			name: "Should return error when type value is nil",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          "some id",
				"title":       "some title",
				"type":        nil,
				"description": "some description",
			},
			expectedError: "required value is nil for key: type",
		},
		{
			name: "Should return error when type value is blank",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          "some id",
				"title":       "some title",
				"type":        "",
				"description": "some description",
			},
			expectedError: "required value is blank for key: type",
		},
		{
			name: "Should return error when description key is not set",
			values: map[string]any{
				"severity": "CRITICAL",
				"id":       "some id",
				"title":    "some title",
				"type":     "some type",
			},
			expectedError: "required key not found: description",
		},
		{
			name: "Should return metadata",
			values: map[string]any{
				"severity":    "CRITICAL",
				"id":          "some id",
				"title":       "some title",
				"type":        "some type",
				"description": "some description",
			},
			expectedMetadata: Metadata{
				ID:          "some id",
				Title:       "some title",
				Severity:    "CRITICAL",
				Type:        "some type",
				Description: "some description",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			metadata, err := NewMetadata(tc.values)
			if tc.expectedError != "" {
				g.Expect(err).To(MatchError(tc.expectedError))
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(metadata).To(Equal(tc.expectedMetadata))
			}
		})
	}

}

func TestNewMessage(t *testing.T) {
	testCases := []struct {
		name           string
		values         map[string]any
		expectedResult string
		expectedError  string
	}{
		{
			name:          "Should return error when values is nil",
			values:        nil,
			expectedError: "values must not be nil",
		},
		{
			name:          "Should return error when msg key is not set",
			values:        make(map[string]any),
			expectedError: "required key not found: msg",
		},
		{
			name: "Should return error when msg value is nil",
			values: map[string]any{
				"msg": nil,
			},
			expectedError: "required value is nil for key: msg",
		},
		{
			name: "Should return error when msg value is blank",
			values: map[string]any{
				"msg": "",
			},
			expectedError: "required value is blank for key: msg",
		},
		{
			name: "Should return result",
			values: map[string]any{
				"msg": "some message",
			},
			expectedResult: "some message",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			result, err := NewMessage(tc.values)
			if tc.expectedError != "" {
				g.Expect(err).To(MatchError(tc.expectedError))
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(result).To(Equal(tc.expectedResult))
			}
		})
	}
}

const (
	// varMessage is the name of Rego variable used to bind deny or warn
	// messages.
	varMessage = "msg"
)

// Result describes result of evaluating a Rego policy that defines `deny` or
// `warn` rules.
type Result struct {
	// Metadata describes Rego policy metadata.
	Metadata Metadata

	// Success represents the status of evaluating Rego policy.
	Success bool

	// Messages deny or warning messages.
	Messages []string
}

// model and function helpers

type Results []Result

func getPolicyResults(results scan.Results) Results {
	prs := make(Results, 0)
	for _, result := range results {
		var msgs []string
		if result.Description() != "" {
			msgs = []string{result.Description()}
		} else {
			msgs = nil
		}
		id := result.Rule().AVDID
		if len(result.Rule().Aliases) > 0 {
			id = result.Rule().Aliases[0]
		}
		pr := Result{Metadata: Metadata{ID: id, Title: result.Rule().Summary, Severity: v1alpha1.Severity(result.Severity()), Type: "Kubernetes Security Check", Description: result.Rule().Explanation}, Success: result.Status() == scan.StatusPassed, Messages: msgs}
		prs = append(prs, pr)
	}
	sort.Sort(resultSort(prs))
	return prs
}

// NewMetadata constructs new Metadata based on raw values.
func NewMetadata(values map[string]any) (Metadata, error) {
	if values == nil {
		return Metadata{}, errors.New("values must not be nil")
	}
	severityString, err := requiredStringValue(values, "severity")
	if err != nil {
		return Metadata{}, err
	}
	severity, err := v1alpha1.StringToSeverity(severityString)
	if err != nil {
		return Metadata{}, fmt.Errorf("failed parsing severity: %w", err)
	}
	id, err := requiredStringValue(values, "id")
	if err != nil {
		return Metadata{}, err
	}
	title, err := requiredStringValue(values, "title")
	if err != nil {
		return Metadata{}, err
	}
	policyType, err := requiredStringValue(values, "type")
	if err != nil {
		return Metadata{}, err
	}
	description, err := requiredStringValue(values, "description")
	if err != nil {
		return Metadata{}, err
	}

	return Metadata{
		Severity:    severity,
		ID:          id,
		Title:       title,
		Type:        policyType,
		Description: description,
	}, nil
}

// Metadata describes policy metadata.
type Metadata struct {
	ID          string
	Title       string
	Severity    v1alpha1.Severity
	Type        string
	Description string
}

// NewMessage constructs new message string based on raw values.
func NewMessage(values map[string]any) (string, error) {
	if values == nil {
		return "", errors.New("values must not be nil")
	}
	message, err := requiredStringValue(values, varMessage)
	if err != nil {
		return "", err
	}
	return message, nil
}
func requiredStringValue(values map[string]any, key string) (string, error) {
	value, ok := values[key]
	if !ok {
		return "", fmt.Errorf("required key not found: %s", key)
	}
	if value == nil {
		return "", fmt.Errorf("required value is nil for key: %s", key)
	}
	valueString, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("expected string got %T for key: %s", value, key)
	}
	if valueString == "" {
		return "", fmt.Errorf("required value is blank for key: %s", key)
	}
	return valueString, nil
}

type resultSort Results

func (a resultSort) Len() int           { return len(a) }
func (a resultSort) Less(i, j int) bool { return a[i].Metadata.ID < a[j].Metadata.ID }
func (a resultSort) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

type testConfig struct {
	builtInPolicies  bool
	embeddedPolicies bool
}

func newTestConfig(builtInPolicies bool) testConfig {
	return testConfig{builtInPolicies: builtInPolicies}
}

// GetUseBuiltinRegoPolicies return trivy config which associated to configauditreport plugin
func (tc testConfig) GetUseBuiltinRegoPolicies() bool {
	return tc.builtInPolicies
}

// GetUseBuiltinRegoPolicies return trivy config which associated to configauditreport plugin
func (tc testConfig) GetUseEmbeddedRegoPolicies() bool {
	return tc.embeddedPolicies
}

// GetSupportedConfigAuditKinds list of supported kinds to be scanned by the config audit scanner
func (tc testConfig) GetSupportedConfigAuditKinds() []string {
	return utils.MapKinds(strings.Split(trivy.SupportedConfigAuditKinds, ","))
}

func (tc testConfig) GetSeverity() string {
	return trivy.KeyTrivySeverity
}

type TestLoader struct {
}

func (tl *TestLoader) GetPoliciesAndBundlePath() ([]string, []string, error) {
	policies, err := policy.LoadPoliciesData([]string{"./testdata/fixture/content"})
	if err != nil {
		return nil, nil, err
	}
	return policies, nil, nil
}
