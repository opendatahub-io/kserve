# Using OCI containers for model storage

Starting from ODH 2.18, the ability to use OCI containers as storage for models
is enabled in KServe by default. The benefits of using OCI containers for
model storage are described in the upstream KServe project
[documentation](https://kserve.github.io/website/latest/modelserving/storage/oci/),
which also explains how to deploy models from OCI images. 

This page offers a guide similar to the upstream project documentation, but
focusing on the OpenDataHub and OpenShift characteristics. To demonstrate
how to create and use OCI containers, two examples are provided:
* The first example uses [IBM's Granite-3.0-2B-Instruct model](https://huggingface.co/ibm-granite/granite-3.0-2b-instruct) 
  available in Hugging Face. This is a generative AI model.
* The second example uses the [MobileNet v2-7 model](https://github.com/onnx/models/tree/main/validated/vision/classification/mobilenet)
  is used. This is a predictive AI model in ONNX format.

## Creating and deploying an OCI image of IBM's Granite-3.0-2B-Instruct model

IBM's Granite-3.0-2B-Instruct model is [available at Hugging Face](https://huggingface.co/ibm-granite/granite-3.0-2b-instruct).
To create an OCI container image, the model needs to be downloaded and copied into
the container. Once the OCI image is built and published in a registry, it can be
deployed on the cluster.

The ODH projects provides configurations for the vLLM model server, which
supports running the Granite model. Thus, this guide will use this model server
to demonstrate how to deploy the Granite model stored in an OCI image.

### Storing the Granite model in an OCI image

To download the Granite model, the [`huggingface-cli download` command](https://huggingface.co/docs/huggingface_hub/guides/cli#huggingface-cli-download) can be used.
The process is as follows:
* Install the huggingface CLI
* Use huggingface CLI to download the model
* Create the final OCI using the downloaded model

It is possible to run this process as part of a multi-stage container build. Create a file named
`Containerfile` with the following contents:
```Dockerfile
##### Stage 1: Download the model
FROM registry.access.redhat.com/ubi9/python-312:latest as downloader

# Install huggingface-cli
RUN pip install "huggingface_hub[cli]"

# Download the model
ARG repo_id
RUN mkdir models && huggingface-cli download --quiet --max-workers 2 --local-dir ./models $repo_id

##### Stage 2: Build the final OCI model container
FROM registry.access.redhat.com/ubi8/ubi-micro:latest as model

# Copy from the download stage
COPY --from=downloader --chown=0:0 /opt/app-root/src/models /models

# Set proper privileges for KServe
RUN chmod -R a=rX /models

# Use non-root user as default
USER 65534
```

> [!TIP]
> This Containerfile should be generic enough to download and containerize any
> model from Hugging Face. However, it has only been tested with the Granite model.
> Feel free to try it with any other model that can work with the vLLM server.

Notice that model files are copied into `/models` inside the final container. KServe
expects this path to exist in the OCI image and also expects the model files to
be inside it.

Also, notice that `ubi8-micro` is used as a base container of the final image.
Empty images, like `scratch` cannot be used, because KServe needs to configure the model image
with a command to keep it alive and ensure the model files remain available in
the pod. Thus, it is required to use a base image that provides a shell.

Finally, notice that ownership of the copied model files is changed to the `root`
group, and also read permissions are granted. This is important, because OpenShift
runs containers with a random user ID and with the `root` group ID. The adjustment
of the group and the privileges on the model files ensures that the model server
can access them.

Create the OCI container image of the Granite model using Podman, and upload it to
a registry. For example, using Quay as the registry:
```shell
podman build --format=oci --squash \
  --build-arg repo_id=ibm-granite/granite-3.0-2b-instruct \
  -t quay.io/<user_name>/<repository_name>:<tag_name> .

podman push quay.io/<user_name>/<repository_name>:<tag_name>
```

It is important to use the `--squash` flag to prevent the final image having
the double size of the model.

> [!TIP]
> When uploading your container image, if your repository is private, ensure you
> are authenticated to the registry.

### Deploying the Granite model using the generated OCI image

Start by creating a namespace to deploy the model:
```shell
oc new-project oci-model-example
```

In the newly created namespace, you need to create a `ServingRuntime` resource
configuring vLLM model server. The ODH project provides templates with
configurations for some model servers, which you can list with the following
command:
```shell
oc get template -n opendatahub

NAME                                 DESCRIPTION                                                                        PARAMETERS    OBJECTS
caikit-standalone-serving-template   Caikit is an AI toolkit that enables users to manage models through a set of...    0 (all set)   1
caikit-tgis-serving-template         Caikit is an AI toolkit that enables users to manage models through a set of...    0 (all set)   1
kserve-ovms                          OpenVino Model Serving Definition                                                  0 (all set)   1
ovms                                 OpenVino Model Serving Definition                                                  0 (all set)   1
tgis-grpc-serving-template           Text Generation Inference Server (TGIS) is a high performance inference engin...   0 (all set)   1
vllm-runtime-template                vLLM is a high-throughput and memory-efficient inference and serving engine f...   0 (all set)   1
```

The template that is applicable for KServe and holds the vLLM configuration
is the one named as `vllm-runtime-template`. To create an instance of it, run the
following command:
```shell
oc process -n opendatahub -o yaml vllm-runtime-template | oc apply -f -
```

You can verify that the `ServingRuntime` has been created successfully with the
following command:
```shell
oc get servingruntimes

NAME           DISABLED   MODELTYPE   CONTAINERS         AGE
vllm-runtime              vLLM        kserve-container   11s
```

Notice that the ServingRuntime has been created with `vllm-runtime` name.

Now that the `ServingRuntime` is configured, the Granite model stored in an OCI image can
be deployed by creating an `InferenceService` resource:
```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: sample-isvc-using-oci
spec:
  predictor:
    model:
      runtime: vllm-runtime # This is the name of the ServingRuntime resource
      modelFormat:
        name: vLLM
      storageUri: oci://quay.io/<user_name>/<repository_name>:<tag_name>
      args:
        - --dtype=half
      resources:
        limits:
          nvidia.com/gpu: 1
        requests:
          nvidia.com/gpu: 1
```

> [!IMPORTANT]
> The resulting `ServingRuntime` and `InferenceService` configurations won't set
> any CPU and memory limits.

Once the `InferenceService` resource is created, KServe will deploy the model
stored in the OCI image referred by the `storageUri` field. Check the status
of the deployment with the following command:
```shell
oc get inferenceservice

NAME                    URL                                                       READY   PREV   LATEST   PREVROLLEDOUTREVISION   LATESTREADYREVISION                     AGE
sample-isvc-using-oci   https://sample-isvc-using-oci-oci-model-example.example   True           100                              sample-isvc-using-oci-predictor-00001   2m11s
```

> [!IMPORTANT]
> Remember that, by default, models are exposed outside the cluster and not
> protected with authorization. Read the [authorization guide](authorization.md#deploying-a-protected-inferenceservice)
> and the [private services guide (TODO)](#TODO) to learn how to privately deploy
> models and how to protect them with authorization.

Test the model is working:
```sh
# Query
curl https://sample-isvc-using-oci-oci-model-example.apps.rosa.ehernand-test.v16g.p3.openshiftapps.com/v1/completions \
      -H "Content-Type: application/json" \
      -d '{
          "model": "sample-isvc-using-oci",
          "prompt": "¿Qué es el modelo IBM granite-3?.",
          "max_tokens": 200,
          "temperature": 0.8
      }' | jq

# Response:
{
  "id": "cmpl-dde608075fa64960a5904896fa0202ca",
  "object": "text_completion",
  "created": 1733433715,
  "model": "sample-isvc-using-oci",
  "choices": [
    {
      "index": 0,
      "text": "\nAnswer:\nIBM Granite es un modelo de lenguaje grande que ha sido entrenado por IBM Research en 13 tiempos del mundo. Granite es capaz de generar texto en muchas categorías, in
      cluyendo la creación de narrativas, la generación de código, la atención médica y la asistencia legal. La inteligencia artificial de Granite se basa en un descubrimiento de 2022 llamado modela
      do multitarea, que permite al modelo generar texto en múltiples categorías simultáneamente. IBM Granite es un modelo de lenguaje grande que ha sido entrenado en una amplia variedad de datos, l
      o que le permite generar texto en muchas categorías y responder a una amplia gama de preguntas.",
      "logprobs": null,
      "finish_reason": "stop",
      "stop_reason": null,
      "prompt_logprobs": null
    }
  ],
  "usage": {
    "prompt_tokens": 12,
    "total_tokens": 210,
    "completion_tokens": 198
  }
}
```

## Creating and deploying an OCI image of MobileNet v2-7 model

The MobileNet v2-7 model is available at the [onnx/models](https://github.com/onnx/models/tree/main/validated/vision/classification/mobilenet)
GitHub repository. This model is in ONNX format.

The ODH projects provides configurations for the OpenVINO model server, which
supports models in ONNX format. Thus, this guide will use this model server
to demonstrate how deploy the MobileNet v2-7 model stored in an OCI image.

### Storing the MobileNet v2-7 model in an OCI image

Start by creating an empty directory for downloading the model and creating
the necessary support files to create the OCI image. You may use a temporary
directory by running the following command:
```shell
cd $(mktemp -d)
```

OpenVINO expects a specific directory tree for model versioning.
Starting from some base directory, its contents should be a collection of
numbered subdirectories using positive integer values. The numbers would
represent the versions of the model. When using OCI container images, this
structure may be irrelevant, as you can use the OCI container registry
features. However, since OpenVINO expects the versioned directory structure, a
single subdirectory with an artificial version `1` can be used. Using `models/` as the
base path, create the expected directory structure and download the sample
model into it:
```shell
mkdir -p models/1

DOWNLOAD_URL=https://github.com/onnx/models/raw/main/validated/vision/classification/mobilenet/model/mobilenetv2-7.onnx
curl -L $DOWNLOAD_URL -O --output-dir models/1/
```

> [!TIP]
> If you are planning to use a different model server, you should adapt this
> guide accordingly to your model server requirements. Typically, you would need
> to place your model files directly under `models/`.

Create a file named `Containerfile` with the following contents:
```Dockerfile
FROM registry.access.redhat.com/ubi8/ubi-micro:latest

# Copy the downloaded model
COPY --chown=0:0 models /models

# Set proper privileges for KServe
RUN chmod -R a=rX /models

# Use non-root user as default
USER 65534 
```

Similarly to the Granite example, notice that model files are copied into `/models`,
that the ownership of the copied model files is changed to the `root` group with
read permissions granted, and that empty base images like `scratch` cannot be used.

Verify that the directory structure is good using the `tree` command:
```shell
tree

.
├── Containerfile
└── models
    └── 1
        └── mobilenetv2-7.onnx
```

> [!NOTE]
> Remember that the shown directory structure under `models/` is specific to OpenVINO.

Create the OCI container image with Podman, and upload it to a registry. For
example, using Quay as the registry:
```shell
podman build --format=oci --squash -t quay.io/<user_name>/<repository_name>:<tag_name> .
podman push quay.io/<user_name>/<repository_name>:<tag_name>
```

### Deploying the MobileNet v2-7 model using the generated OCI image

Start by creating a namespace to deploy the model:
```shell
oc new-project oci-model-example
```

As commented, the OpenVINO model server is used to deploy the MobileNet model.
Create the OpenVINO `ServingRuntime` from the provided template:
```shell
oc process -n opendatahub -o yaml kserve-ovms | oc apply -f -
```

You can verify that the `ServingRuntime` has been created successfully with the
following command:
```shell
oc get servingruntimes

NAME          DISABLED   MODELTYPE     CONTAINERS         AGE
kserve-ovms              openvino_ir   kserve-container   1m
```

Notice that the ServingRuntime has been created with `kserve-ovms` name.

Now that the `ServingRuntime` is configured, a model stored in an OCI image can
be deployed by creating an `InferenceService` resource:
```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: sample-isvc-using-oci
spec:
  predictor:
    model:
      runtime: kserve-ovms # This is the name of the ServingRuntime resource
      modelFormat:
        name: onnx
      storageUri: oci://quay.io/<user_name>/<repository_name>:<tag_name>
```

> [!IMPORTANT]
> The resulting `ServingRuntime` and `InferenceService` configurations won't set
> any resource limits.

Once the `InferenceService` resource is created, KServe will deploy the model
stored in the OCI image referred by the `storageUri` field. Check the status
of the deployment with the following command:
```shell
oc get inferenceservice

NAME                    URL                                                       READY   PREV   LATEST   PREVROLLEDOUTREVISION   LATESTREADYREVISION                     AGE
sample-isvc-using-oci   https://sample-isvc-using-oci-oci-model-example.example   True           100                              sample-isvc-using-oci-predictor-00001   1m
```

> [!IMPORTANT]
> Remember that, by default, models are exposed outside the cluster and not
> protected with authorization. Read the [authorization guide](authorization.md#deploying-a-protected-inferenceservice)
> and the [private services guide (TODO)](#TODO) to learn how to privately deploy
> models and how to protect them with authorization.

## Deploying a model stored in an OCI image from a private repository

To deploy a model stored in a private OCI repository you need to configure an
image pull secret. For detailed documentation, please consult the OpenShift
[documentation](https://docs.openshift.com/container-platform/latest/openshift_images/managing_images/using-image-pull-secrets.html)
for image pull secrets.

When using namespaced pull secrets you can create a pull secret using the following
command template:

```shell
oc create secret docker-registry <pull-secret-name> \
  --docker-server=<registry-server> \
  --docker-username=<username> \
  --docker-password=<password>
```

Once the pull secret is created, you can follow the steps from the previous
section for deploying a model with one small variant: when creating the
`InferenceService`, specify your pull secret in the
`spec.predictor.imagePullSecrets` field:
```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: sample-isvc-using-private-oci
spec:
  predictor:
    model:
      runtime: kserve-ovms
      modelFormat:
        name: onnx
      storageUri: oci://quay.io/<user_name>/<repository_name>:<tag_name>
    imagePullSecrets: # Specify image pull secrets to use for fetching container images (including OCI model images)
    - name: <pull-secret-name>
```
