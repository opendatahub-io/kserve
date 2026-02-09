#!/bin/bash

# Color codes for better output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# Check prerequisites
echo "Checking prerequisites..."
echo ""

# Check for oc command
if ! command -v oc &> /dev/null; then
    echo -e "${RED}❌ Error: OpenShift CLI (oc) is not installed${NC}"
    echo "Please install oc: https://docs.openshift.com/container-platform/latest/cli_reference/openshift_cli/getting-started-cli.html"
    exit 1
fi

# Check for yq command
if ! command -v yq &> /dev/null; then
    echo -e "${RED}❌ Error: yq is not installed${NC}"
    echo "Please install yq: https://github.com/mikefarah/yq#install"
    exit 1
fi

# Check for jq command
if ! command -v jq &> /dev/null; then
    echo -e "${RED}❌ Error: jq is not installed${NC}"
    echo "Please install jq: https://jqlang.github.io/jq/download/"
    exit 1
fi

# Check if oc is logged in
if ! oc whoami &> /dev/null; then
    echo -e "${RED}❌ Error: Not logged into OpenShift cluster${NC}"
    echo "Please login first: oc login <cluster-url>"
    exit 1
fi

echo -e "${GREEN}✅ Logged in as: $(oc whoami)${NC}"
echo -e "${GREEN}✅ Cluster context: $(oc config current-context 2>/dev/null || echo 'unknown')${NC}"
echo ""

echo "=========================================="
echo "Migration Validation Checklist"
echo "=========================================="
echo ""

# DSC Check
echo "1. DSC - KServe Serving Management State:"
DSC_STATE=$(oc get dsc -o yaml 2>/dev/null | yq '.items[].spec.components.kserve.serving.managementState')
echo "   Current: $DSC_STATE"
if [ "$DSC_STATE" = "Removed" ]; then
    echo -e "   ${GREEN}✅ PASS${NC}"
else
    echo -e "   ${RED}❌ FAIL - Expected: Removed${NC}"
fi
echo ""

# DSCI Check
echo "2. DSCI - ServiceMesh Management State:"
DSCI_STATE=$(oc get dsci -o yaml 2>/dev/null | yq '.items[].spec.serviceMesh.managementState')
echo "   Current: $DSCI_STATE"
if [ "$DSCI_STATE" = "Removed" ]; then
    echo -e "   ${GREEN}✅ PASS${NC}"
else
    echo -e "   ${RED}❌ FAIL - Expected: Removed${NC}"
fi
echo ""

# ISVCs Deployment Mode
echo "3. InferenceServices Deployment Mode:"
SERVERLESS_COUNT=$(oc get isvc -A -o json 2>/dev/null | jq -r '.items[].metadata.annotations["serving.kserve.io/deploymentMode"] // "Serverless"' | grep -c "Serverless" || echo 0)
RAW_COUNT=$(oc get isvc -A -o json 2>/dev/null | jq -r '.items[].metadata.annotations["serving.kserve.io/deploymentMode"]' | grep -c "RawDeployment" || echo 0)
TOTAL_ISVC=$(oc get isvc -A --no-headers 2>/dev/null | wc -l)
echo "   RawDeployment: $RAW_COUNT"
echo "   Serverless: $SERVERLESS_COUNT"
echo "   Total ISVCs: $TOTAL_ISVC"
if [ "$SERVERLESS_COUNT" -eq 0 ] && [ "$TOTAL_ISVC" -gt 0 ]; then
    echo -e "   ${GREEN}✅ PASS - All ISVCs use RawDeployment${NC}"
elif [ "$TOTAL_ISVC" -eq 0 ]; then
    echo -e "   ${YELLOW}⚠️  WARNING - No ISVCs found${NC}"
else
    echo -e "   ${RED}❌ FAIL - Found $SERVERLESS_COUNT Serverless ISVCs${NC}"
    echo ""
    echo "   Serverless ISVCs found:"
    oc get isvc -A -o json 2>/dev/null | jq -r '.items[] | select((.metadata.annotations["serving.kserve.io/deploymentMode"] // "Serverless") == "Serverless") | "   - \(.metadata.namespace)/\(.metadata.name)"'
fi
echo ""

# ModelMesh Controller
echo "4. ModelMesh Controller Pods:"
MM_PODS=$(oc get pods -n redhat-ods-applications -l control-plane=modelmesh-controller --no-headers 2>/dev/null | wc -l)
echo "   Count: $MM_PODS"
if [ "$MM_PODS" -eq 0 ]; then
    echo -e "   ${GREEN}✅ PASS - No ModelMesh controllers running${NC}"
else
    echo -e "   ${RED}❌ FAIL - ModelMesh controllers still running${NC}"
    oc get pods -n redhat-ods-applications -l control-plane=modelmesh-controller 2>/dev/null | sed 's/^/   /'
fi
echo ""

# Knative Controller
echo "5. Knative Serving Controller Pods:"
KN_PODS=$(oc get pods -n knative-serving -l app=controller --no-headers 2>/dev/null | wc -l)
echo "   Count: $KN_PODS"
if [ "$KN_PODS" -eq 0 ]; then
    echo -e "   ${GREEN}✅ PASS - No Knative controllers running${NC}"
else
    echo -e "   ${RED}❌ FAIL - Knative controllers still running${NC}"
    oc get pods -n knative-serving -l app=controller 2>/dev/null | sed 's/^/   /'
fi
echo ""

# ISVC Readiness
echo "6. InferenceService Readiness:"
READY_COUNT=$(oc get isvc -A --no-headers 2>/dev/null | awk '$3=="True" {count++} END {print count+0}')
NOT_READY_COUNT=$(oc get isvc -A --no-headers 2>/dev/null | awk '$3!="True" {count++} END {print count+0}')
TOTAL_COUNT=$(oc get isvc -A --no-headers 2>/dev/null | wc -l)

echo "   Ready: $READY_COUNT"
echo "   Not Ready: $NOT_READY_COUNT"
echo "   Total: $TOTAL_COUNT"

if [ "$NOT_READY_COUNT" -eq 0 ] && [ "$TOTAL_COUNT" -gt 0 ]; then
    echo -e "   ${GREEN}✅ PASS - All ISVCs are ready${NC}"
elif [ "$TOTAL_COUNT" -eq 0 ]; then
    echo -e "   ${YELLOW}⚠️  WARNING - No ISVCs found${NC}"
else
    echo -e "   ${RED}❌ FAIL - $NOT_READY_COUNT ISVCs not ready${NC}"
    echo ""
    echo "   Not ready ISVCs:"
    oc get isvc -A --no-headers 2>/dev/null | awk '$3!="True" {print "   - " $1 "/" $2 " (Ready: " $3 ")"}'
fi
echo ""

echo "=========================================="
echo "Validation Complete"
echo "=========================================="
