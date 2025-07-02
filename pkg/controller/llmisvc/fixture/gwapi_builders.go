/*
Copyright 2025 The KServe Authors.

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

package fixture

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	v1 "sigs.k8s.io/gateway-api/apis/v1"
)

type ObjectOption[T client.Object] func(T)

type GatewayOption ObjectOption[*v1.Gateway]

func Gateway(name string, opts ...GatewayOption) *v1.Gateway {
	gw := &v1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.GatewaySpec{
			Listeners: []v1.Listener{},
		},
		Status: v1.GatewayStatus{
			Addresses: []v1.GatewayStatusAddress{},
		},
	}

	for _, opt := range opts {
		opt(gw)
	}

	return gw
}

func InNamespace[T client.Object](namespace string) func(T) {
	return func(t T) {
		t.SetNamespace(namespace)
	}
}

func WithClassName(className string) GatewayOption {
	return func(gw *v1.Gateway) {
		gw.Spec.GatewayClassName = v1.ObjectName(className)
	}
}

func WithListener(protocol v1.ProtocolType) GatewayOption {
	return func(gw *v1.Gateway) {
		listener := v1.Listener{
			Protocol: protocol,
		}
		gw.Spec.Listeners = append(gw.Spec.Listeners, listener)
	}
}

func WithListeners(listeners ...v1.Listener) GatewayOption {
	return func(gw *v1.Gateway) {
		gw.Spec.Listeners = append(gw.Spec.Listeners, listeners...)
	}
}
