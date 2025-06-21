package e2e_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"time"

	"github.com/kharf/navecd/pkg/kube"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func TestInstallation(t *testing.T) {
	kubeConfig, err := config.GetConfig()
	assert.NilError(t, err)
	client, err := kube.NewDynamicClient(kubeConfig)
	assert.NilError(t, err)

	dep := &unstructured.Unstructured{}
	dep.SetKind("Deployment")
	dep.SetAPIVersion("apps/v1")
	dep.SetName("project-controller-primary")
	dep.SetNamespace("navecd-system")
	dep, err = client.Get(context.Background(), dep)
	assert.NilError(t, err)

	conditions := kube.GetConditions(dep)

	progressing := slices.ContainsFunc(conditions, func(cond kube.Condition) bool {
		return cond.ConditionType == string(appsv1.DeploymentProgressing) &&
			cond.Status == string(metav1.ConditionTrue)
	})
	assert.Assert(t, progressing)

	fmt.Println("Navecd is progressing")

	poll.WaitOn(t, func(_ poll.LogT) poll.Result {
		dep, err = client.Get(context.Background(), dep)
		assert.NilError(t, err)
		conditions = kube.GetConditions(dep)

		if slices.ContainsFunc(conditions, func(cond kube.Condition) bool {
			return cond.ConditionType == string(appsv1.DeploymentAvailable) &&
				cond.Status == string(metav1.ConditionTrue)
		}) {
			return poll.Success()
		}
		return poll.Continue("not available yet")
	}, poll.WithDelay(1*time.Second), poll.WithTimeout(60*time.Second))

	fmt.Println("Navecd is available")
	assert.Assert(t, len(conditions) == 2)
}
