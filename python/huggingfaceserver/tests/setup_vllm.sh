#!/bin/bash

# We'll set -e after finding the virtual environment
# to avoid premature exit during venv detection

TORCH_EXTRA_INDEX_URL="https://download.pytorch.org/whl/cpu"
VLLM_VERSION=v0.9.0.1
VLLM_DIR=vllm-clone
VLLM_TARGET_DEVICE="${VLLM_TARGET_DEVICE:-cpu}"

case $VLLM_TARGET_DEVICE in
  cpu)
    echo "Installing vllm for CPU"
    ;;
  *)
    echo "Unknown target device: $VLLM_TARGET_DEVICE"
    exit 1
      ;;
esac

# Get the script directory and change to the parent (huggingfaceserver) directory
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR/.."

# Try to get venv path from poetry, with fallback for Python 3.9 issues
VENV_PATH=""
if command -v poetry >/dev/null 2>&1; then
    VENV_PATH=$(poetry env info -p 2>/dev/null || echo "")
fi

# Fallback: look for venv in configured poetry path
if [ -z "$VENV_PATH" ] || [ ! -f "$VENV_PATH/bin/activate" ]; then
    POETRY_VENV_BASE="${POETRY_VENV_BASE:-/mnt/python/huggingfaceserver-cpu-venv}"
    # Find the actual venv directory (Poetry creates subdirs with hashed names)
    if [ -d "$POETRY_VENV_BASE" ]; then
        VENV_PATH=$(find "$POETRY_VENV_BASE" -name "bin" -type d -exec test -f {}/activate \; -print | head -1 | xargs dirname 2>/dev/null || echo "")
    fi
fi

if [ -n "$VENV_PATH" ] && [ -f "$VENV_PATH/bin/activate" ]; then
    echo "Activating virtual environment at: $VENV_PATH"
    source "$VENV_PATH/bin/activate"
else
    echo "Warning: Could not find virtual environment, trying to continue without activation"
    echo "Poetry should handle the Python environment"
fi

# Now enable strict error handling for the installation commands
set -e
mkdir $VLLM_DIR
cd $VLLM_DIR

git clone --branch $VLLM_VERSION --depth 1 https://github.com/vllm-project/vllm.git .
pip install --upgrade pip

case $VLLM_TARGET_DEVICE in
    cpu)
        pip uninstall -y torch torchvision torchaudio && \
        pip install -r requirements/build.txt -r requirements/cpu.txt --extra-index-url ${TORCH_EXTRA_INDEX_URL}
        ;;
esac

PIP_EXTRA_INDEX_URL=${TORCH_EXTRA_INDEX_URL} VLLM_TARGET_DEVICE=${VLLM_TARGET_DEVICE} python -m pip install -v .

cd ..
rm -rf $VLLM_DIR
