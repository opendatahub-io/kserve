package kservemodule

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestAllocatableNamesEqualIncludesQuantities(t *testing.T) {
	quantity := func(value string) resource.Quantity { return resource.MustParse(value) }

	tests := []struct {
		name string
		a, b corev1.ResourceList
		want bool
	}{
		{name: "same", a: corev1.ResourceList{"nvidia.com/gpu": quantity("1")}, b: corev1.ResourceList{"nvidia.com/gpu": quantity("1")}, want: true},
		{name: "quantity changed", a: corev1.ResourceList{"nvidia.com/gpu": quantity("0")}, b: corev1.ResourceList{"nvidia.com/gpu": quantity("1")}, want: false},
		{name: "key missing", a: corev1.ResourceList{"nvidia.com/gpu": quantity("1")}, b: corev1.ResourceList{}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := allocatableNamesEqual(tc.a, tc.b); got != tc.want {
				t.Fatalf("allocatableNamesEqual() = %v, want %v", got, tc.want)
			}
		})
	}
}
