#!/usr/bin/env bash
#
# OpenShift Operator Subscription Configurations
#
# Single source of truth for operator names, channels, and namespaces
# Used by both E2E setup scripts and CI infra deployment scripts

# Cert Manager
CERT_MANAGER_NAME="openshift-cert-manager-operator"
CERT_MANAGER_NAMESPACE="cert-manager-operator"
CERT_MANAGER_CHANNEL="stable-v1"

# Leader Worker Set
LWS_NAME="leader-worker-set"
LWS_NAMESPACE="openshift-lws-operator"
LWS_CHANNEL="stable-v1.0"

# Kuadrant (RHCL)
RHCL_NAME="rhcl-operator"
KUADRANT_NS="${KUADRANT_NS:-kuadrant-system}"
RHCL_NAMESPACE="${KUADRANT_NS}"
RHCL_CHANNEL="stable"

# Custom Metrics Autoscaler (KEDA)
CMA_NAME="openshift-custom-metrics-autoscaler-operator"
CMA_NAMESPACE="openshift-keda"
CMA_CHANNEL="stable"
