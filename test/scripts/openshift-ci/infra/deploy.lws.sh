SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/../common.sh"

echo "⏳ Installing openshift-lws-operator"
{
cat <<EOF | oc create -f -
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: lws-operator
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: quay.io/jooholee/lws-operator-index:llmd
EOF
} || true

oc create ns openshift-lws-operator || true

{
cat <<EOF | oc create -f -
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: openshift-lws-operator-jw944
  namespace: openshift-lws-operator
spec:
  targetNamespaces:
  - openshift-lws-operator
  upgradeStrategy: Default
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: leader-worker-set
  namespace: openshift-lws-operator
spec:
  channel: stable
  installPlanApproval: Automatic
  name: leader-worker-set
  source: lws-operator
  sourceNamespace: openshift-marketplace
  startingCSV: leader-worker-set.v1.0.0
EOF
} || true

wait_for_crd leaderworkersetoperators.operator.openshift.io 90s

{
cat <<EOF | oc create -f -
apiVersion: operator.openshift.io/v1
kind: LeaderWorkerSetOperator
metadata:
  name: cluster
  namespace: openshift-lws-operator
spec:
  managementState: Managed
  logLevel: Normal
  operatorLogLevel: Normal
EOF
} || true

echo "⏳ waiting for openshift-lws-operator to be ready.…"
wait_for_pod_ready "openshift-lws-operator" "name=openshift-lws-operator"

echo "✅ openshift-lws-operator installed"
