/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package framework

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	cabpkv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1alpha3"

	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1alpha3"
	controlplanev1 "sigs.k8s.io/cluster-api/controlplane/kubeadm/api/v1alpha3"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// eventuallyInterval is the polling interval used by gomega's Eventually
	// Deprecated
	eventuallyInterval = 10 * time.Second
)

// MachineDeployment contains the objects needed to create a
// CAPI MachineDeployment resource and its associated template
// resources.
// Deprecated. Please use the individual create/assert methods.
type MachineDeployment struct {
	MachineDeployment       *clusterv1.MachineDeployment
	BootstrapConfigTemplate runtime.Object
	InfraMachineTemplate    runtime.Object
}

// Node contains all the pieces necessary to make a single node
// Deprecated.
type Node struct {
	Machine         *clusterv1.Machine
	InfraMachine    runtime.Object
	BootstrapConfig runtime.Object
}

// ControlplaneClusterInput defines the necessary dependencies to run a multi-node control plane cluster.
// Deprecated.
type ControlplaneClusterInput struct {
	Management        ManagementCluster
	Cluster           *clusterv1.Cluster
	InfraCluster      runtime.Object
	Nodes             []Node
	MachineDeployment MachineDeployment
	RelatedResources  []runtime.Object
	CreateTimeout     time.Duration
	DeleteTimeout     time.Duration

	ControlPlane    *controlplanev1.KubeadmControlPlane
	MachineTemplate runtime.Object
}

// SetDefaults defaults the struct fields if necessary.
// Deprecated.
func (input *ControlplaneClusterInput) SetDefaults() {
	if input.CreateTimeout == 0 {
		input.CreateTimeout = 10 * time.Minute
	}

	if input.DeleteTimeout == 0 {
		input.DeleteTimeout = 5 * time.Minute
	}
}

// ControlPlaneCluster creates an n node control plane cluster.
// Assertions:
//  * The number of nodes in the created cluster will equal the number
//    of control plane nodes plus the number of replicas in the machine
//    deployment.
// Deprecated. Please use the supplied functions below to get the exact behavior desired.
func (input *ControlplaneClusterInput) ControlPlaneCluster() {
	ctx := context.Background()
	Expect(input.Management).ToNot(BeNil())

	mgmtClient, err := input.Management.GetClient()
	Expect(err).NotTo(HaveOccurred(), "stack: %+v", err)

	By("creating an InfrastructureCluster resource")
	Expect(mgmtClient.Create(ctx, input.InfraCluster)).To(Succeed())

	// This call happens in an eventually because of a race condition with the
	// webhook server. If the latter isn't fully online then this call will
	// fail.
	By("creating a Cluster resource linked to the InfrastructureCluster resource")
	Eventually(func() error {
		if err := mgmtClient.Create(ctx, input.Cluster); err != nil {
			fmt.Printf("%+v\n", err)
			return err
		}
		return nil
	}, input.CreateTimeout, eventuallyInterval).Should(BeNil())

	By("creating related resources")
	for i := range input.RelatedResources {
		obj := input.RelatedResources[i]
		By(fmt.Sprintf("creating a/an %s resource", obj.GetObjectKind().GroupVersionKind()))
		Eventually(func() error {
			return mgmtClient.Create(ctx, obj)
		}, input.CreateTimeout, eventuallyInterval).Should(BeNil())
	}

	By("creating the machine template")
	Expect(mgmtClient.Create(ctx, input.MachineTemplate)).To(Succeed())

	By("creating a KubeadmControlPlane")
	Eventually(func() error {
		err := mgmtClient.Create(ctx, input.ControlPlane)
		if err != nil {
			fmt.Println(err)
		}
		return err
	}, input.CreateTimeout, 10*time.Second).Should(BeNil())

	By("waiting for cluster to enter the provisioned phase")
	Eventually(func() (string, error) {
		cluster := &clusterv1.Cluster{}
		key := client.ObjectKey{
			Namespace: input.Cluster.GetNamespace(),
			Name:      input.Cluster.GetName(),
		}
		if err := mgmtClient.Get(ctx, key, cluster); err != nil {
			return "", err
		}
		return cluster.Status.Phase, nil
	}, input.CreateTimeout, eventuallyInterval).Should(Equal(string(clusterv1.ClusterPhaseProvisioned)))

	// Create the machine deployment if the replica count >0.
	if machineDeployment := input.MachineDeployment.MachineDeployment; machineDeployment != nil {
		if replicas := machineDeployment.Spec.Replicas; replicas != nil && *replicas > 0 {
			By("creating a core MachineDeployment resource")
			Expect(mgmtClient.Create(ctx, machineDeployment)).To(Succeed())

			By("creating a BootstrapConfigTemplate resource")
			Expect(mgmtClient.Create(ctx, input.MachineDeployment.BootstrapConfigTemplate)).To(Succeed())

			By("creating an InfrastructureMachineTemplate resource")
			Expect(mgmtClient.Create(ctx, input.MachineDeployment.InfraMachineTemplate)).To(Succeed())
		}

		By("waiting for the workload nodes to exist")
		Eventually(func() ([]v1.Node, error) {
			workloadClient, err := input.Management.GetWorkloadClient(ctx, input.Cluster.Namespace, input.Cluster.Name)
			if err != nil {
				return nil, errors.Wrap(err, "failed to get workload client")
			}
			nodeList := v1.NodeList{}
			if err := workloadClient.List(ctx, &nodeList); err != nil {
				return nil, err
			}
			return nodeList.Items, nil
		}, input.CreateTimeout, 10*time.Second).Should(HaveLen(int(*machineDeployment.Spec.Replicas)))
	}

	By("waiting for all machines to be running")
	inClustersNamespaceListOption := client.InNamespace(input.Cluster.Namespace)
	matchClusterListOption := client.MatchingLabels{clusterv1.ClusterLabelName: input.Cluster.Name}
	Eventually(func() (bool, error) {
		// Get a list of all the Machine resources that belong to the Cluster.
		machineList := &clusterv1.MachineList{}
		if err := mgmtClient.List(ctx, machineList, inClustersNamespaceListOption, matchClusterListOption); err != nil {
			return false, err
		}
		for _, machine := range machineList.Items {
			if machine.Status.Phase != string(clusterv1.MachinePhaseRunning) {
				return false, errors.Errorf("machine %s is not running, it's %s", machine.Name, machine.Status.Phase)
			}
		}
		return true, nil
	}, input.CreateTimeout, eventuallyInterval).Should(BeTrue())
	// wait for the control plane to be ready
	By("waiting for the control plane to be ready")
	Eventually(func() bool {
		controlplane := &controlplanev1.KubeadmControlPlane{}
		key := client.ObjectKey{
			Namespace: input.ControlPlane.GetNamespace(),
			Name:      input.ControlPlane.GetName(),
		}
		if err := mgmtClient.Get(ctx, key, controlplane); err != nil {
			fmt.Println(err.Error())
			return false
		}
		return controlplane.Status.Initialized
	}, input.CreateTimeout, 10*time.Second).Should(BeTrue())
}

// CleanUpCoreArtifacts deletes the cluster and waits for everything to be gone.
// Assertions made on objects owned by the Cluster:
//   * All Machines are removed
//   * All MachineSets are removed
//   * All MachineDeployments are removed
//   * All KubeadmConfigs are removed
//   * All Secrets are removed
// Deprecated
func (input *ControlplaneClusterInput) CleanUpCoreArtifacts() {
	input.SetDefaults()
	ctx := context.Background()
	mgmtClient, err := input.Management.GetClient()
	Expect(err).NotTo(HaveOccurred(), "stack: %+v", err)

	By(fmt.Sprintf("deleting cluster %s", input.Cluster.GetName()))
	Expect(mgmtClient.Delete(ctx, input.Cluster)).To(Succeed())

	Eventually(func() bool {
		clusters := clusterv1.ClusterList{}
		if err := mgmtClient.List(ctx, &clusters); err != nil {
			fmt.Println(err.Error())
			return false
		}
		return len(clusters.Items) == 0
	}, input.DeleteTimeout, eventuallyInterval).Should(BeTrue())

	lbl, err := labels.Parse(fmt.Sprintf("%s=%s", clusterv1.ClusterLabelName, input.Cluster.GetClusterName()))
	Expect(err).ToNot(HaveOccurred())
	listOpts := &client.ListOptions{LabelSelector: lbl}

	By("ensuring all CAPI artifacts have been deleted")
	ensureArtifactsDeleted(ctx, mgmtClient, listOpts)
}

// Deprecated
func ensureArtifactsDeleted(ctx context.Context, mgmtClient Lister, opt client.ListOption) {
	// assertions
	ml := &clusterv1.MachineList{}
	Expect(mgmtClient.List(ctx, ml, opt)).To(Succeed())
	Expect(ml.Items).To(HaveLen(0))

	msl := &clusterv1.MachineSetList{}
	Expect(mgmtClient.List(ctx, msl, opt)).To(Succeed())
	Expect(msl.Items).To(HaveLen(0))

	mdl := &clusterv1.MachineDeploymentList{}
	Expect(mgmtClient.List(ctx, mdl, opt)).To(Succeed())
	Expect(mdl.Items).To(HaveLen(0))

	kcpl := &controlplanev1.KubeadmControlPlaneList{}
	Expect(mgmtClient.List(ctx, kcpl, opt)).To(Succeed())
	Expect(kcpl.Items).To(HaveLen(0))

	kcl := &cabpkv1.KubeadmConfigList{}
	Expect(mgmtClient.List(ctx, kcl, opt)).To(Succeed())
	Expect(kcl.Items).To(HaveLen(0))

	sl := &v1.SecretList{}
	Expect(mgmtClient.List(ctx, sl, opt)).To(Succeed())
	Expect(sl.Items).To(HaveLen(0))
}
