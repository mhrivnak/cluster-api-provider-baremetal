/*
Copyright 2019 The Kubernetes Authors.

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

package machineset

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	bmh "github.com/metal3-io/baremetal-operator/pkg/apis/metal3/v1alpha1"
	bmv1alpha1 "github.com/metal3-io/cluster-api-provider-baremetal/pkg/apis/baremetal/v1alpha1"
	"github.com/onsi/gomega"
	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	"golang.org/x/net/context"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var c client.Client

var expectedRequest1 = reconcile.Request{NamespacedName: types.NamespacedName{Name: "machineset1", Namespace: "default"}}
var expectedRequest2 = reconcile.Request{NamespacedName: types.NamespacedName{Name: "machineset2", Namespace: "default"}}
var machinesetKey1 = types.NamespacedName{Name: "machineset1", Namespace: "default"}
var machinesetKey2 = types.NamespacedName{Name: "machineset2", Namespace: "default"}

const timeout = time.Second * 10

func TestScale(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	rawProviderSpec, err := json.Marshal(&bmv1alpha1.BareMetalMachineProviderSpec{
		HostSelector: bmv1alpha1.HostSelector{
			MatchLabels: map[string]string{"size": "large"},
		},
	})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	instance := &machinev1beta1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "machineset1",
			Namespace:   "default",
			Annotations: map[string]string{AutoScaleAnnotation: "yesplease"},
		},
		Spec: machinev1beta1.MachineSetSpec{
			Template: machinev1beta1.MachineTemplateSpec{
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: machinev1beta1.ProviderSpec{
						Value: &runtime.RawExtension{Raw: rawProviderSpec},
					},
				},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"machine.openshift.io/cluster-api-machineset": "cluster0-worker"},
			},
		},
	}
	// machine1 has a different label than the MachineSet's Selector, so its
	// consumed host should not be counted as part of that MachineSet's
	// potential hosts.
	machine1 := machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine1",
			Namespace: "default",
			Labels:    map[string]string{"machine.openshift.io/cluster-api-machineset": "cluster0-storage"},
		},
	}
	// machine2 has a label that matches the MachineSet's Selector, so its
	// consumed host should be counted as part of that MachineSet's potential
	// hosts even if that BareMetalHost does not otherwise match.
	machine2 := machinev1beta1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machine2",
			Namespace: "default",
			Labels:    map[string]string{"machine.openshift.io/cluster-api-machineset": "cluster0-worker"},
		},
	}
	host1 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host1",
			Namespace: "default",
			Labels:    map[string]string{"size": "large"},
		},
	}
	// This host has a different label, but its consuming Machine is
	// part of the MachineSet, so it should be counted.
	host2 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host2",
			Namespace: "default",
			Labels:    map[string]string{"size": "small"},
		},
		Spec: bmh.BareMetalHostSpec{
			ConsumerRef: &v1.ObjectReference{
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				Name:       "machine2",
				Namespace:  "default",
			},
		},
	}
	// This host has a different label, so it should not match
	host3 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host3",
			Namespace: "default",
			Labels:    map[string]string{"size": "extramedium"},
		},
	}
	// This host is consumed by a Machine in a different MachineSet, so it
	// should not be counted
	host4 := bmh.BareMetalHost{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "host4",
			Namespace: "default",
			Labels:    map[string]string{"size": "large"},
		},
		Spec: bmh.BareMetalHostSpec{
			ConsumerRef: &v1.ObjectReference{
				Kind:       "Machine",
				APIVersion: machinev1beta1.SchemeGroupVersion.String(),
				Name:       "machine1",
				Namespace:  "default",
			},
		},
	}

	// Setup the Manager and Controller. Wrap the Controller Reconcile function
	// so it writes each request to a channel when it is finished.
	mgr, err := manager.New(cfg, manager.Options{Scheme: scheme.Scheme})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	c = mgr.GetClient()

	recFn, requests := SetupTestReconcile(newReconciler(mgr))
	g.Expect(add(mgr, recFn)).To(gomega.Succeed())

	stopMgr, mgrStopped := StartTestManager(mgr, g)

	defer func() {
		close(stopMgr)
		mgrStopped.Wait()
	}()

	// Create BareMetalHosts
	hosts := []runtime.Object{&host1, &host2, &host3, &host4}
	for i := range hosts {
		err = c.Create(context.TODO(), hosts[i])
		g.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Delete(context.TODO(), hosts[i])
	}

	// Create Machines
	machines := []runtime.Object{&machine1, &machine2}
	for i := range machines {
		err = c.Create(context.TODO(), machines[i])
		g.Expect(err).NotTo(gomega.HaveOccurred())
		defer c.Delete(context.TODO(), machines[i])
	}

	// Create the MachineSet object and expect the Reconcile to happen
	err = c.Create(context.TODO(), instance)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), instance)
	g.Eventually(requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest1)))

	g.Eventually(func() error {
		ms := machinev1beta1.MachineSet{}
		err := c.Get(context.TODO(), machinesetKey1, &ms)
		switch {
		case err != nil:
			return err
		case ms.Spec.Replicas == nil:
			return fmt.Errorf("Replicas is nil")
		case *ms.Spec.Replicas != 2:
			return fmt.Errorf("Replicas %d is not 2", *ms.Spec.Replicas)
		}
		return nil
	}, timeout).Should(gomega.Succeed())

	// Delete a host and expect the MachineSet to be scaled down
	g.Expect(c.Delete(context.TODO(), &host1)).To(gomega.Succeed())
	g.Eventually(requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest1)))

	g.Eventually(func() error {
		ms := machinev1beta1.MachineSet{}
		err := c.Get(context.TODO(), machinesetKey1, &ms)
		if err != nil {
			return err
		}
		if ms.Spec.Replicas == nil || *ms.Spec.Replicas != 1 {
			return fmt.Errorf("Replicas is not 1")
		}
		return nil
	}, timeout).Should(gomega.Succeed())
}

// TestIgnore ensures that a MachineSet without the annotation gets ignored.
func TestIgnore(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	rawProviderSpec, err := json.Marshal(&bmv1alpha1.BareMetalMachineProviderSpec{})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	five := int32(5)
	instance := &machinev1beta1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "machineset2",
			Namespace: "default",
		},
		Spec: machinev1beta1.MachineSetSpec{
			Replicas: &five,
			Template: machinev1beta1.MachineTemplateSpec{
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: machinev1beta1.ProviderSpec{
						Value: &runtime.RawExtension{Raw: rawProviderSpec},
					},
				},
			},
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"machine.openshift.io/cluster-api-machineset": "cluster0-worker"},
			},
		},
	}

	// Setup the Manager and Controller. Wrap the Controller Reconcile function
	// so it writes each request to a channel when it is finished.
	mgr, err := manager.New(cfg, manager.Options{Scheme: scheme.Scheme})
	g.Expect(err).NotTo(gomega.HaveOccurred())
	c = mgr.GetClient()

	recFn, requests := SetupTestReconcile(newReconciler(mgr))
	g.Expect(add(mgr, recFn)).To(gomega.Succeed())

	stopMgr, mgrStopped := StartTestManager(mgr, g)

	defer func() {
		close(stopMgr)
		mgrStopped.Wait()
	}()

	// Create the MachineSet object and expect the Reconcile to happen
	err = c.Create(context.TODO(), instance)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	defer c.Delete(context.TODO(), instance)
	g.Eventually(requests, timeout).Should(gomega.Receive(gomega.Equal(expectedRequest2)))

	g.Eventually(func() error {
		ms := machinev1beta1.MachineSet{}
		err := c.Get(context.TODO(), machinesetKey2, &ms)
		if err != nil {
			return err
		}
		if *ms.Spec.Replicas != 5 {
			return fmt.Errorf("replicas is not 5; the MachineSet was not ignored as expected")
		}
		return nil
	}, timeout).Should(gomega.Succeed())
}
