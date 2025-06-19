package testing

import (
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
)

type SetupWithManagerFunc func(mgr ctrl.Manager) error

type AddToSchemeFunc func(scheme *runtime.Scheme) error
