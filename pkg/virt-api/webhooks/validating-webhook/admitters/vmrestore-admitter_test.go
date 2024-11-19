/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */

package admitters

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	v1 "kubevirt.io/api/core/v1"
	snapshotv1 "kubevirt.io/api/snapshot/v1beta1"
	"kubevirt.io/client-go/kubecli"
	kubevirtfake "kubevirt.io/client-go/kubevirt/fake"

	"kubevirt.io/kubevirt/pkg/apimachinery/patch"
	"kubevirt.io/kubevirt/pkg/pointer"
	"kubevirt.io/kubevirt/pkg/testutils"
	"kubevirt.io/kubevirt/pkg/virt-api/webhooks"
	virtconfig "kubevirt.io/kubevirt/pkg/virt-config"
)

var _ = Describe("Validating VirtualMachineRestore Admitter", func() {
	const (
		vmName         = "vm"
		vmSnapshotName = "snapshot"
	)

	var vmUID types.UID = "vm-uid"
	apiGroup := "kubevirt.io"

	snapshot := &snapshotv1.VirtualMachineSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmSnapshotName,
			Namespace: "default",
		},
		Status: &snapshotv1.VirtualMachineSnapshotStatus{
			SourceUID:  &vmUID,
			ReadyToUse: pointer.P(true),
		},
	}

	config, _, kvStore := testutils.NewFakeClusterConfigUsingKVConfig(&v1.KubeVirtConfiguration{})

	Context("Without feature gate enabled", func() {
		It("should reject anything", func() {
			restore := &snapshotv1.VirtualMachineRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore",
					Namespace: "default",
				},
				Spec: snapshotv1.VirtualMachineRestoreSpec{},
			}

			ar := createRestoreAdmissionReview(restore)
			resp := createTestVMRestoreAdmitter(config, nil).Admit(context.Background(), ar)
			Expect(resp.Allowed).To(BeFalse())
			Expect(resp.Result.Message).Should(Equal("Snapshot/Restore feature gate not enabled"))
		})
	})

	Context("With feature gate enabled", func() {
		enableFeatureGate := func(featureGate string) {
			testutils.UpdateFakeKubeVirtClusterConfig(kvStore, &v1.KubeVirt{
				Spec: v1.KubeVirtSpec{
					Configuration: v1.KubeVirtConfiguration{
						DeveloperConfiguration: &v1.DeveloperConfiguration{
							FeatureGates: []string{featureGate},
						},
					},
				},
			})
		}
		disableFeatureGates := func() {
			testutils.UpdateFakeKubeVirtClusterConfig(kvStore, &v1.KubeVirt{
				Spec: v1.KubeVirtSpec{
					Configuration: v1.KubeVirtConfiguration{
						DeveloperConfiguration: &v1.DeveloperConfiguration{
							FeatureGates: make([]string, 0),
						},
					},
				},
			})
		}

		BeforeEach(func() {
			enableFeatureGate("Snapshot")
		})

		AfterEach(func() {
			disableFeatureGates()
		})

		It("should reject invalid request resource", func() {
			ar := &admissionv1.AdmissionReview{
				Request: &admissionv1.AdmissionRequest{
					Resource: webhooks.VirtualMachineGroupVersionResource,
				},
			}

			resp := createTestVMRestoreAdmitter(config, nil).Admit(context.Background(), ar)
			Expect(resp.Allowed).To(BeFalse())
			Expect(resp.Result.Message).Should(ContainSubstring("unexpected resource"))
		})

		It("should reject missing apigroup", func() {
			restore := &snapshotv1.VirtualMachineRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore",
					Namespace: "default",
				},
				Spec: snapshotv1.VirtualMachineRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						Kind: "VirtualMachine",
						Name: vmName,
					},
					VirtualMachineSnapshotName: vmSnapshotName,
				},
			}

			ar := createRestoreAdmissionReview(restore)
			resp := createTestVMRestoreAdmitter(config, nil, snapshot).Admit(context.Background(), ar)
			Expect(resp.Allowed).To(BeFalse())
			Expect(resp.Result.Details.Causes).To(HaveLen(1))
			Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.target.apiGroup"))
		})

		It("should reject when snapshot does not exist", func() {
			restore := &snapshotv1.VirtualMachineRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore",
					Namespace: "default",
				},
				Spec: snapshotv1.VirtualMachineRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: &apiGroup,
						Kind:     "VirtualMachine",
						Name:     vmName,
					},
					VirtualMachineSnapshotName: vmSnapshotName,
				},
			}

			ar := createRestoreAdmissionReview(restore)
			resp := createTestVMRestoreAdmitter(config, nil).Admit(context.Background(), ar)
			Expect(resp.Allowed).To(BeFalse())
			Expect(resp.Result.Details.Causes).To(HaveLen(1))
			Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.virtualMachineSnapshotName"))
		})

		It("should reject spec update", func() {
			restore := &snapshotv1.VirtualMachineRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore",
					Namespace: "default",
				},
				Spec: snapshotv1.VirtualMachineRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: &apiGroup,
						Kind:     "VirtualMachine",
						Name:     vmName,
					},
					VirtualMachineSnapshotName: vmSnapshotName,
				},
			}

			oldRestore := &snapshotv1.VirtualMachineRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore",
					Namespace: "default",
				},
				Spec: snapshotv1.VirtualMachineRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: &apiGroup,
						Kind:     "VirtualMachine",
						Name:     "baz",
					},
					VirtualMachineSnapshotName: vmSnapshotName,
				},
			}

			ar := createRestoreUpdateAdmissionReview(oldRestore, restore)
			resp := createTestVMRestoreAdmitter(config, nil).Admit(context.Background(), ar)
			Expect(resp.Allowed).To(BeFalse())
			Expect(resp.Result.Details.Causes).To(HaveLen(1))
			Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec"))
		})

		It("should allow metadata update", func() {
			oldRestore := &snapshotv1.VirtualMachineRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "restore",
					Namespace: "default",
				},
				Spec: snapshotv1.VirtualMachineRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: &apiGroup,
						Kind:     "VirtualMachine",
						Name:     vmName,
					},
				},
			}

			restore := &snapshotv1.VirtualMachineRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "restore",
					Namespace:  "default",
					Finalizers: []string{"finalizer"},
				},
				Spec: snapshotv1.VirtualMachineRestoreSpec{
					Target: corev1.TypedLocalObjectReference{
						APIGroup: &apiGroup,
						Kind:     "VirtualMachine",
						Name:     vmName,
					},
				},
			}

			ar := createRestoreUpdateAdmissionReview(oldRestore, restore)
			resp := createTestVMRestoreAdmitter(config, nil).Admit(context.Background(), ar)
			Expect(resp.Allowed).To(BeTrue())
		})

		Context("when VirtualMachine exists", func() {
			var vm *v1.VirtualMachine

			BeforeEach(func() {
				vm = &v1.VirtualMachine{
					ObjectMeta: metav1.ObjectMeta{
						Name: vmName,
						UID:  vmUID,
					},
				}
			})

			It("should allow when VM is running", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyAlways)

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeTrue())
			})

			It("should allow when VM run strategy is not halted", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyManual)

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeTrue())
			})

			It("should reject when snapshot does not exist", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyHalted)

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Details.Causes).To(HaveLen(1))
				Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.virtualMachineSnapshotName"))
			})

			It("should reject when snapshot has failed", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyHalted)
				s := snapshot.DeepCopy()
				s.Status.Phase = snapshotv1.Failed

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, s).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Details.Causes).To(HaveLen(1))
				Expect(resp.Result.Details.Causes[0].Message).To(Equal(fmt.Sprintf("VirtualMachineSnapshot %q has failed and is invalid to use", vmSnapshotName)))
			})

			It("should reject when snapshot not ready", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyHalted)
				s := snapshot.DeepCopy()
				s.Status.ReadyToUse = pointer.P(false)

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, s).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Details.Causes).To(HaveLen(1))
				Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.virtualMachineSnapshotName"))
			})

			It("should reject invalid kind", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachineInstance",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyAlways)

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Details.Causes).To(HaveLen(1))
				Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.target.kind"))
			})

			It("should reject invalid apiGroup", func() {
				g := "foo.bar"
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &g,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyAlways)

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Details.Causes).To(HaveLen(1))
				Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.target.apiGroup"))
			})

			It("should reject if restore in progress", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore-in-process",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyHalted)

				restoreInProcess := &snapshotv1.VirtualMachineRestore{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "restore-in-process",
						Namespace: "default",
					},
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, snapshot, restoreInProcess).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeFalse())
				Expect(resp.Result.Details.Causes).To(HaveLen(1))
				Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.target"))
			})

			It("should accept when VM is not running", func() {
				restore := &snapshotv1.VirtualMachineRestore{
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     vmName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				vm.Spec.RunStrategy = pointer.P(v1.RunStrategyHalted)

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
				Expect(resp.Allowed).To(BeTrue())
			})

			DescribeTable("when target VM is different from source VM", func(doesTargetExist bool) {
				const targetVMName = "new-test-vm"

				var targetVM *v1.VirtualMachine
				if doesTargetExist {
					targetVM = vm.DeepCopy()
					targetVM.Name = targetVMName
					targetVM.UID = "new-uid"
				}

				restore := &snapshotv1.VirtualMachineRestore{
					Spec: snapshotv1.VirtualMachineRestoreSpec{
						Target: corev1.TypedLocalObjectReference{
							APIGroup: &apiGroup,
							Kind:     "VirtualMachine",
							Name:     targetVMName,
						},
						VirtualMachineSnapshotName: vmSnapshotName,
					},
				}

				ar := createRestoreAdmissionReview(restore)
				resp := createTestVMRestoreAdmitter(config, targetVM, snapshot).Admit(context.Background(), ar)

				if doesTargetExist {
					Expect(resp.Allowed).To(BeFalse())
					Expect(resp.Result.Details.Causes).To(HaveLen(1))
					Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.virtualMachineSnapshotName"))
					Expect(resp.Result.Details.Causes[0].Message).To(ContainSubstring("target VM must not exist"))
				} else {
					Expect(resp.Allowed).To(BeTrue())
				}
			},
				Entry("should allow if target doesn't exist", false),
				Entry("should reject if target exists", true),
			)

			Context("when using Patches", func() {

				var restore *snapshotv1.VirtualMachineRestore

				BeforeEach(func() {
					restore = &snapshotv1.VirtualMachineRestore{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "restore",
							Namespace: "default",
						},
						Spec: snapshotv1.VirtualMachineRestoreSpec{
							Target: corev1.TypedLocalObjectReference{
								APIGroup: &apiGroup,
								Kind:     "VirtualMachine",
								Name:     vmName,
							},
							VirtualMachineSnapshotName: vmSnapshotName,
						},
					}
				})

				DescribeTable("should reject patching elements not under /spec/:", func(patchSet *patch.PatchSet) {
					patchBytes, err := patchSet.GeneratePayload()
					Expect(err).To(Not(HaveOccurred()))
					restore.Spec.Patches = []string{string(patchBytes)}

					ar := createRestoreAdmissionReview(restore)
					resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
					Expect(resp.Allowed).To(BeFalse())
					Expect(resp.Result.Details.Causes).To(HaveLen(1))
					Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.patches"))
				},
					Entry("patch to replace metadata", patch.New(patch.WithReplace("/metadata", "some-value"))),
					Entry("patch to replace name", patch.New(patch.WithReplace("/metadata/name", "some-value"))),
					Entry("patch to replace kind", patch.New(patch.WithReplace("/kind", "some-value"))),
					Entry("patch to remove api version", patch.New(patch.WithRemove("/apiVersion"))),
					Entry("patch to replace status", patch.New(patch.WithReplace("/status", "some-value"))),
					Entry("patch to add ready status", patch.New(patch.WithAdd("/status/ready", "some-value"))),
				)

				DescribeTable("should allow patching elements under /spec/:", func(patchSet *patch.PatchSet) {
					patchBytes, err := patchSet.GeneratePayload()
					Expect(err).To(Not(HaveOccurred()))
					restore.Spec.Patches = []string{string(patchBytes)}

					ar := createRestoreAdmissionReview(restore)
					resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
					Expect(resp.Allowed).To(BeTrue())
				},
					Entry("patch to replace MAC", patch.New(patch.WithReplace("/spec/template/spec/domain/devices/interfaces/0/macAddress", "some-value"))),
					Entry("patch to add running", patch.New(patch.WithAdd("/spec/running", "some-value"))),
					Entry("patch to remove instancetype", patch.New(patch.WithRemove("/spec/instancetype"))),
					Entry("patch to replace a label", patch.New(patch.WithReplace("/metadata/labels/key", "some-value"))),
					Entry("patch to remove an annotation", patch.New(patch.WithRemove("/metadata/annotations/key"))),
				)

				It("should reject an invalid patch", func() {
					const invalidPatch = `{"op": "remove", "path": "/spec/running" : "illegal-field"}`
					restore.Spec.Patches = []string{invalidPatch}

					ar := createRestoreAdmissionReview(restore)
					resp := createTestVMRestoreAdmitter(config, vm, snapshot).Admit(context.Background(), ar)
					Expect(resp.Allowed).To(BeFalse())
					Expect(resp.Result.Details.Causes).To(HaveLen(1))
					Expect(resp.Result.Details.Causes[0].Field).To(Equal("spec.patches"))
				})
			})

		})
	})
})

func createRestoreAdmissionReview(restore *snapshotv1.VirtualMachineRestore) *admissionv1.AdmissionReview {
	bytes, _ := json.Marshal(restore)

	ar := &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			Namespace: "default",
			Resource: metav1.GroupVersionResource{
				Group:    "snapshot.kubevirt.io",
				Resource: "virtualmachinerestores",
			},
			Object: runtime.RawExtension{
				Raw: bytes,
			},
		},
	}

	return ar
}

func createRestoreUpdateAdmissionReview(old, current *snapshotv1.VirtualMachineRestore) *admissionv1.AdmissionReview {
	oldBytes, _ := json.Marshal(old)
	currentBytes, _ := json.Marshal(current)

	ar := &admissionv1.AdmissionReview{
		Request: &admissionv1.AdmissionRequest{
			Operation: admissionv1.Update,
			Namespace: "default",
			Resource: metav1.GroupVersionResource{
				Group:    "snapshot.kubevirt.io",
				Resource: "virtualmachinerestores",
			},
			Object: runtime.RawExtension{
				Raw: currentBytes,
			},
			OldObject: runtime.RawExtension{
				Raw: oldBytes,
			},
		},
	}

	return ar
}

func createTestVMRestoreAdmitter(
	config *virtconfig.ClusterConfig,
	vm *v1.VirtualMachine,
	objs ...runtime.Object,
) *VMRestoreAdmitter {
	ctrl := gomock.NewController(GinkgoT())
	virtClient := kubecli.NewMockKubevirtClient(ctrl)
	vmInterface := kubecli.NewMockVirtualMachineInterface(ctrl)
	kubevirtClient := kubevirtfake.NewSimpleClientset(objs...)

	virtClient.EXPECT().VirtualMachineSnapshot("default").
		Return(kubevirtClient.SnapshotV1beta1().VirtualMachineSnapshots("default")).AnyTimes()
	virtClient.EXPECT().VirtualMachine(gomock.Any()).Return(vmInterface).AnyTimes()

	restoreInformer, _ := testutils.NewFakeInformerFor(&snapshotv1.VirtualMachineRestore{})
	for _, obj := range objs {
		r, ok := obj.(*snapshotv1.VirtualMachineRestore)
		if ok {
			restoreInformer.GetIndexer().Add(r)
		}
	}

	vmInterface.EXPECT().Get(context.Background(), gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, name string, getOptions metav1.GetOptions) (*v1.VirtualMachine, error) {
		if vm != nil && name == vm.Name {
			return vm, nil
		}

		err := errors.NewNotFound(schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachines"}, "foo")
		return nil, err
	}).AnyTimes()

	return &VMRestoreAdmitter{Config: config, Client: virtClient, VMRestoreInformer: restoreInformer}
}
