#!/bin/bash

# Generate webhook manifests by kustomize
ORIGINAL_WEBHOOK_MANIFESTS=original_config.yaml
export WEBHOOK_SERVER="https://internal-similarly-cockatoo.ngrok-free.app"
kustomize build "config/webhook" > $ORIGINAL_WEBHOOK_MANIFESTS

# Load each manifest
export OUTPUT_FILE="updated_webhook_manifests.yaml"
delimeter_lines=$(cat -n ${ORIGINAL_WEBHOOK_MANIFESTS} | grep '\-\-\-' | cut -f1)
start_line=1
rm -f $OUTPUT_FILE

for end_line in $delimeter_lines; do
  sed -n "${start_line},$((end_line - 1))p" "${ORIGINAL_WEBHOOK_MANIFESTS}" > "temp_output_file.yaml"
  start_line=$((end_line + 1))
  kind=$(yq '.kind' "temp_output_file.yaml")

  if [[ $kind == 'MutatingWebhookConfiguration' ]] || [[ $kind == 'ValidatingWebhookConfiguration' ]]; then
    echo "---" >> $OUTPUT_FILE
    for webhook in $(yq '.webhooks | keys | .[]' "temp_output_file.yaml"); do
      export WEBHOOK_PATH=$(yq ".webhooks[$webhook].clientConfig.service.path // \"default_value\"" "temp_output_file.yaml")
      yq "del(.webhooks[$webhook].clientConfig.service)" -i "temp_output_file.yaml" 
      yq ".webhooks[$webhook].clientConfig.url = env(WEBHOOK_SERVER) + env(WEBHOOK_PATH)" -i "temp_output_file.yaml"
    done
    cat "temp_output_file.yaml" >> $OUTPUT_FILE
  fi
done

echo "Updated webhook manifests saved to $OUTPUT_FILE"
kubectl delete -f $OUTPUT_FILE
kubectl apply -f $OUTPUT_FILE

# Clean up  manifests
rm -f original_config.yaml
rm -f temp_output_file.yaml
#rm -f $OUTPUT_FILE
