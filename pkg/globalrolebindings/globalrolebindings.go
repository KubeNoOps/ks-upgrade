package globalrolebindings

import (
	"encoding/json"
	"fmt"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	"kubesphere.io/ks-upgrade/pkg/rbac"
	"kubesphere.io/ks-upgrade/pkg/task"
)

type globalRoleBindingMigrateTask struct {
	k8sClient kubernetes.Interface
}

func NewGlobalRoleBindingMigrateTask(k8sClient kubernetes.Interface) task.UpgradeTask {
	return &globalRoleBindingMigrateTask{k8sClient: k8sClient}
}

func (t *globalRoleBindingMigrateTask) Run() error {

	clusterRoleBindings, err := t.k8sClient.RbacV1().ClusterRoleBindings().List(metav1.ListOptions{})
	if err != nil {
		klog.Error(err)
		return err
	}

	migrateMapping := map[string]string{
		"cluster-admin":      "platform-admin",
		"cluster-regular":    "platform-regular",
		"workspaces-manager": "workspaces-manager",
	}

	globalRoleBindings := make([]GlobalRoleBinding, 0)
	globalRoles := make([]GlobalRole, 0)

	for _, clusterRoleBinding := range clusterRoleBindings.Items {
		if len(clusterRoleBinding.Subjects) != 1 ||
			clusterRoleBinding.Subjects[0].Kind != "User" ||
			clusterRoleBinding.Name != clusterRoleBinding.Subjects[0].Name {
			continue
		}
		globalRoleRef := migrateMapping[clusterRoleBinding.RoleRef.Name]
		if globalRoleRef == "" {
			clusterRole, err := t.k8sClient.RbacV1().ClusterRoles().Get(clusterRoleBinding.RoleRef.Name, metav1.GetOptions{})
			if err != nil {
				if errors.IsNotFound(err) {
					klog.Warningf("invalid cluster role binding found: %s", clusterRoleBinding.Name)
					continue
				}
				klog.Error(err)
				return err
			}
			if clusterRole.Annotations["kubesphere.io/creator"] == "" {
				continue
			}
			globalRole := newGlobalRole(clusterRole)
			globalRoles = append(globalRoles, globalRole)
			globalRoleRef = globalRole.Name
		}
		globalRoleBinding := newGlobalRoleBinding(clusterRoleBinding.Subjects[0].Name, globalRoleRef)
		globalRoleBindings = append(globalRoleBindings, globalRoleBinding)
	}

	cli := t.k8sClient.(*kubernetes.Clientset)
	for _, globalRole := range globalRoles {
		outputData, _ := json.Marshal(globalRole)
		klog.Infof("migrate globalRole: %s: %s", globalRole.Name, string(outputData))
		err := cli.RESTClient().
			Post().
			AbsPath(fmt.Sprintf("/apis/iam.kubesphere.io/v1alpha2/globalroles")).
			Body(outputData).
			Do().Error()
		if err != nil && !errors.IsAlreadyExists(err) {
			klog.Error(err)
			return err
		}
	}
	for _, globalRoleBinding := range globalRoleBindings {
		outputData, _ := json.Marshal(globalRoleBinding)
		klog.Infof("migrate globalRoleBinding: %s: %s", globalRoleBinding.Name, string(outputData))
		err := cli.RESTClient().
			Post().
			AbsPath(fmt.Sprintf("/apis/iam.kubesphere.io/v1alpha2/globalrolebindings")).
			Body(outputData).
			Do().Error()

		if err != nil && !errors.IsAlreadyExists(err) {
			klog.Error(err)
			return err
		}
	}

	for _, clusterRoleBinding := range clusterRoleBindings.Items {
		if len(clusterRoleBinding.Subjects) != 1 ||
			clusterRoleBinding.Subjects[0].Kind != "User" ||
			clusterRoleBinding.Name != clusterRoleBinding.Subjects[0].Name {
			continue
		}
		if err := t.k8sClient.RbacV1().ClusterRoleBindings().
			Delete(clusterRoleBinding.Name, metav1.NewDeleteOptions(0)); err != nil {
			klog.Warningf("delete legacy cluster role binding failed: %s", err)
		}
	}

	return nil
}

var customRoleMapping = map[string]rbacv1.PolicyRule{
	"role-template-view-users": {
		Verbs:     []string{"get", "list"},
		APIGroups: []string{"*"},
		Resources: []string{"users"},
	},
	"role-template-view-workspaces": {
		Verbs:     []string{"get", "list"},
		APIGroups: []string{"*"},
		Resources: []string{"workspaces"},
	},
	"role-template-view-roles": {
		Verbs:     []string{"get", "list"},
		APIGroups: []string{"*"},
		Resources: []string{"clusterroles"},
	},
	"role-template-view-app-templates": {
		Verbs:     []string{"get", "list"},
		APIGroups: []string{"openpitrix.io"},
		Resources: []string{"apps"},
	},
	"role-template-manage-users": {
		Verbs:     []string{"get", "list", "create", "delete", "update"},
		APIGroups: []string{"*"},
		Resources: []string{"users"},
	},
	"role-template-manage-workspaces": {
		Verbs:     []string{"get", "list", "create", "delete", "update"},
		APIGroups: []string{"*"},
		Resources: []string{"workspaces"},
	},
	"role-template-manage-roles": {
		Verbs:     []string{"get", "list", "create", "delete", "update"},
		APIGroups: []string{"*"},
		Resources: []string{"clusterroles"},
	},
	"role-template-manage-app-templates": {
		Verbs:     []string{"get", "list", "create", "delete", "update"},
		APIGroups: []string{"openpitrix.io"},
		Resources: []string{"apps"},
	},
}

func newGlobalRole(clusterRole *rbacv1.ClusterRole) GlobalRole {
	aggregationRoles := make([]string, 0)
	for role, policyRule := range customRoleMapping {
		if rbac.RulesMatchesRequired(clusterRole.Rules, policyRule) {
			aggregationRoles = append(aggregationRoles, role)
		}
	}
	roles, _ := json.Marshal(aggregationRoles)
	return GlobalRole{
		TypeMeta: metav1.TypeMeta{
			Kind:       "GlobalRole",
			APIVersion: "iam.kubesphere.io/v1alpha2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRole.Name,
			Annotations: map[string]string{
				"iam.kubesphere.io/aggregation-roles": string(roles),
			},
		},
		Rules: []rbacv1.PolicyRule{},
	}
}

func newGlobalRoleBinding(username string, globalRoleRef string) GlobalRoleBinding {
	return GlobalRoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "GlobalRoleBinding",
			APIVersion: "iam.kubesphere.io/v1alpha2",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s-%s", username, globalRoleRef),
			Labels: map[string]string{
				"iam.kubesphere.io/user-ref": username,
			},
		},
		Subjects: []rbacv1.Subject{{Name: username, Kind: "User", APIGroup: "rbac.authorization.k8s.io"}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "iam.kubesphere.io",
			Kind:     "GlobalRole",
			Name:     globalRoleRef,
		},
	}
}
