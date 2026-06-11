package distro

// Distro-specific RBAC rules for the kserve-controller.
// Processed by a separate controller-gen invocation (see Makefile.overrides.mk)
// to generate a dedicated ClusterRole included only in distro overlays.

// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=hardwareprofiles,verbs=get;list;watch
