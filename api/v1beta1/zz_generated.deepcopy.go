//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright 2022.

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

// Code generated by controller-gen. DO NOT EDIT.

package v1beta1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
	apiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ControlPlaneConfig) DeepCopyInto(out *ControlPlaneConfig) {
	*out = *in
	in.InitConfig.DeepCopyInto(&out.InitConfig)
	in.ControlPlaneConfig.DeepCopyInto(&out.ControlPlaneConfig)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ControlPlaneConfig.
func (in *ControlPlaneConfig) DeepCopy() *ControlPlaneConfig {
	if in == nil {
		return nil
	}
	out := new(ControlPlaneConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *MachineTemplate) DeepCopyInto(out *MachineTemplate) {
	*out = *in
	out.InfrastructureTemplate = in.InfrastructureTemplate
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new MachineTemplate.
func (in *MachineTemplate) DeepCopy() *MachineTemplate {
	if in == nil {
		return nil
	}
	out := new(MachineTemplate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *MicroK8sControlPlane) DeepCopyInto(out *MicroK8sControlPlane) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new MicroK8sControlPlane.
func (in *MicroK8sControlPlane) DeepCopy() *MicroK8sControlPlane {
	if in == nil {
		return nil
	}
	out := new(MicroK8sControlPlane)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *MicroK8sControlPlane) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *MicroK8sControlPlaneList) DeepCopyInto(out *MicroK8sControlPlaneList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]MicroK8sControlPlane, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new MicroK8sControlPlaneList.
func (in *MicroK8sControlPlaneList) DeepCopy() *MicroK8sControlPlaneList {
	if in == nil {
		return nil
	}
	out := new(MicroK8sControlPlaneList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *MicroK8sControlPlaneList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *MicroK8sControlPlaneSpec) DeepCopyInto(out *MicroK8sControlPlaneSpec) {
	*out = *in
	if in.Replicas != nil {
		in, out := &in.Replicas, &out.Replicas
		*out = new(int32)
		**out = **in
	}
	out.MachineTemplate = in.MachineTemplate
	in.ControlPlaneConfig.DeepCopyInto(&out.ControlPlaneConfig)
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new MicroK8sControlPlaneSpec.
func (in *MicroK8sControlPlaneSpec) DeepCopy() *MicroK8sControlPlaneSpec {
	if in == nil {
		return nil
	}
	out := new(MicroK8sControlPlaneSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *MicroK8sControlPlaneStatus) DeepCopyInto(out *MicroK8sControlPlaneStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make(apiv1beta1.Conditions, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new MicroK8sControlPlaneStatus.
func (in *MicroK8sControlPlaneStatus) DeepCopy() *MicroK8sControlPlaneStatus {
	if in == nil {
		return nil
	}
	out := new(MicroK8sControlPlaneStatus)
	in.DeepCopyInto(out)
	return out
}
