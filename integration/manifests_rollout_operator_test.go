//go:build requires_docker

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"

	"github.com/grafana/rollout-operator/integration/k3t"
)

// These manifest files are generated via jsonnet-integration-tests
const (
	yamlWebhookNoDownscale    = "admissionregistration.k8s.io-v1.ValidatingWebhookConfiguration-no-downscale-default.yaml"
	yamlWebhookPodEviction    = "admissionregistration.k8s.io-v1.ValidatingWebhookConfiguration-pod-eviction-default.yaml"
	yamlWebhookZpdbValidation = "admissionregistration.k8s.io-v1.ValidatingWebhookConfiguration-zpdb-validation-default.yaml"
	yamlDeployment            = "apps-v1.Deployment-rollout-operator.yaml"
	yamlClusterRole           = "rbac.authorization.k8s.io-v1.ClusterRole-rollout-operator-default-webhook-cert-update-role.yaml"
	yamlCluserRoleBinding     = "rbac.authorization.k8s.io-v1.ClusterRoleBinding-rollout-operator-default-webhook-cert-secret-rolebinding.yaml"
	yamlRole                  = "rbac.authorization.k8s.io-v1.Role-rollout-operator-role.yaml"
	yamlRoleSecret            = "rbac.authorization.k8s.io-v1.Role-rollout-operator-webhook-cert-secret-role.yaml"
	yamlRoleBinding           = "rbac.authorization.k8s.io-v1.RoleBinding-rollout-operator-rolebinding.yaml"
	yamlRoleBindingSecret     = "rbac.authorization.k8s.io-v1.RoleBinding-rollout-operator-webhook-cert-secret-rolebinding.yaml"
	yamlService               = "v1.Service-rollout-operator.yaml"
	yamlServiceAccount        = "v1.ServiceAccount-rollout-operator.yaml"
	yamlZpdbConfig            = "rollout-operator.grafana.com-v1.ZoneAwarePodDisruptionBudget-mock-rollout.yaml"
	yamlPathTemplate          = "jsonnet-integration-tests/environments/%s/yaml/"
)

// initManifestFiles re-generates the yaml manifest files from jsonnet.
// Each test has a jsonnet configuration in the jsonnet-integration directory.
// This function re-generates the manifest files associated with the given test,
// allowing the generated files to be used within the integration tests.
// Returns the path to the generated files.
func initManifestFiles(t *testing.T, test string) string {
	t.Log("Generating manifest files for " + test)
	cmd := exec.Command("jsonnet-integration/build.sh", test)
	err := cmd.Start()
	require.NoError(t, err)
	err = cmd.Wait()
	require.NoError(t, err)

	return fmt.Sprintf(yamlPathTemplate, test)
}

func createRolloutOperator(t *testing.T, ctx context.Context, api *kubernetes.Clientset, extApi *apiextensionsclient.Clientset, directory string, webhook bool) {
	createRolloutOperatorDependencies(t, ctx, api, extApi, directory, webhook)

	deployment := loadFromDisk[appsv1.Deployment](t, directory+yamlDeployment, &appsv1.Deployment{})
	deployment.Spec.Template.Spec.Containers[0].Image = "rollout-operator:latest"

	_, err := api.AppsV1().Deployments(corev1.NamespaceDefault).Create(ctx, deployment, metav1.CreateOptions{})
	require.NoError(t, err)
}

func createRolloutOperatorDependencies(t *testing.T, ctx context.Context, api *kubernetes.Clientset, extApi *apiextensionsclient.Clientset, directory string, webhook bool) {

	serviceAccount := loadFromDisk[corev1.ServiceAccount](t, directory+yamlServiceAccount, &corev1.ServiceAccount{})
	_, err := api.CoreV1().ServiceAccounts(corev1.NamespaceDefault).Create(ctx, serviceAccount, metav1.CreateOptions{})
	require.NoError(t, err)

	role := loadFromDisk[rbacv1.Role](t, directory+yamlRole, &rbacv1.Role{})
	_, err = api.RbacV1().Roles(corev1.NamespaceDefault).Create(ctx, role, metav1.CreateOptions{})
	require.NoError(t, err)

	roleBinding := loadFromDisk[rbacv1.RoleBinding](t, directory+yamlRoleBinding, &rbacv1.RoleBinding{})
	_, err = api.RbacV1().RoleBindings(corev1.NamespaceDefault).Create(ctx, roleBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	_ = createZoneAwarePodDistruptionBudgetCustomResourceDefinition(t, extApi)

	if webhook {
		_ = createReplicaTemplateCustomResourceDefinition(t, extApi)

		operatorRole := loadFromDisk[rbacv1.Role](t, directory+yamlRoleSecret, &rbacv1.Role{})
		_, err = api.RbacV1().Roles(corev1.NamespaceDefault).Create(ctx, operatorRole, metav1.CreateOptions{})
		require.NoError(t, err)

		operatorRoleBinding := loadFromDisk[rbacv1.RoleBinding](t, directory+yamlRoleBindingSecret, &rbacv1.RoleBinding{})
		_, err = api.RbacV1().RoleBindings(corev1.NamespaceDefault).Create(ctx, operatorRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err)

		clusterRole := loadFromDisk[rbacv1.ClusterRole](t, directory+yamlClusterRole, &rbacv1.ClusterRole{})
		_, err = api.RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
		require.NoError(t, err)

		clusterRoleBinding := loadFromDisk[rbacv1.ClusterRoleBinding](t, directory+yamlCluserRoleBinding, &rbacv1.ClusterRoleBinding{})
		_, err = api.RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err)

		service := loadFromDisk[corev1.Service](t, directory+yamlService, &corev1.Service{})
		_, err = api.CoreV1().Services(corev1.NamespaceDefault).Create(ctx, service, metav1.CreateOptions{})
		require.NoError(t, err)
	}
}

var (
	scheme       = runtime.NewScheme()
	codecs       = serializer.NewCodecFactory(scheme)
	deserializer = codecs.UniversalDeserializer()
)

func loadToMapFromDisk(path string, obj map[string]interface{}) error {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("failed to read file from disk: %w", err)
	}

	jsonData, err := yaml.ToJSON(bytes)
	if err != nil {
		return fmt.Errorf("failed to convert yaml to json: %w", err)
	}

	if err := json.Unmarshal(jsonData, &obj); err != nil {
		return fmt.Errorf("failed to unmarshal yaml to map: %w", err)
	}

	return nil
}

func loadFromDisk[T any](t *testing.T, path string, into runtime.Object) *T {
	bytes, err := os.ReadFile(path)
	require.NoError(t, err)

	jsonData, err := yaml.ToJSON(bytes)
	require.NoError(t, err)

	obj, _, err := deserializer.Decode(jsonData, nil, into)
	require.NoError(t, err)

	ptr, ok := any(obj).(*T)
	require.True(t, ok)

	return ptr
}

func loadCustomResourceDefinitionFromDisk(t *testing.T, path string) *apiextensionsv1.CustomResourceDefinition {
	return loadFromDisk[apiextensionsv1.CustomResourceDefinition](t, path, &apiextensionsv1.CustomResourceDefinition{})
}

func loadValidatingWebhookConfigurationFromFile(t *testing.T, path string) *admissionregistrationv1.ValidatingWebhookConfiguration {
	return loadFromDisk[admissionregistrationv1.ValidatingWebhookConfiguration](t, path, &admissionregistrationv1.ValidatingWebhookConfiguration{})
}

func createZoneAwarePodDistruptionBudgetCustomResourceDefinition(t *testing.T, extApi *apiextensionsclient.Clientset) *apiextensionsv1.CustomResourceDefinition {
	obj, err := extApi.ApiextensionsV1().CustomResourceDefinitions().Create(context.Background(), loadCustomResourceDefinitionFromDisk(t, "../development/zone-aware-pod-disruption-budget-custom-resource-definition.yaml"), metav1.CreateOptions{})
	require.NoError(t, err)
	return obj
}

func createReplicaTemplateCustomResourceDefinition(t *testing.T, extApi *apiextensionsclient.Clientset) *apiextensionsv1.CustomResourceDefinition {
	obj, err := extApi.ApiextensionsV1().CustomResourceDefinitions().Create(context.Background(), loadCustomResourceDefinitionFromDisk(t, "../development/replica-templates-custom-resource-definition.yaml"), metav1.CreateOptions{})
	require.NoError(t, err)
	return obj
}

func createValidatingWebhookConfiguration(t *testing.T, api *kubernetes.Clientset, ctx context.Context, path string) *admissionregistrationv1.ValidatingWebhookConfiguration {
	obj, err := api.AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, loadValidatingWebhookConfigurationFromFile(t, path), metav1.CreateOptions{})
	require.NoError(t, err)

	return obj
}

func createZoneAwarePodDisruptionBudget(t *testing.T, cluster k3t.Cluster, ctx context.Context, path string) (*unstructured.Unstructured, error) {
	obj := map[string]interface{}{}
	err := loadToMapFromDisk(path, obj)
	if err != nil {
		return nil, err
	}

	zpdb := &unstructured.Unstructured{
		Object: obj,
	}

	// because this is an unstructured object we must explicitly set this so the dynamic client can find this resource
	zpdb.SetGroupVersionKind(zoneAwarePodDisruptionBudgetSchemaKind())

	ret, err := cluster.DynK().Resource(zoneAwarePodDisruptionBudgetSchema()).Namespace(corev1.NamespaceDefault).Create(ctx, zpdb, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	return ret, nil
}
