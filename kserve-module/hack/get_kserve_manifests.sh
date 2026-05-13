#!/usr/bin/env bash
set -e

GITHUB_URL="https://github.com"
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
MODULE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${MODULE_DIR}/.." && pwd)"
DST_MANIFESTS_DIR="${1:-${MODULE_DIR}/opt/manifests}"

# ODH Component Manifests
# Format: "repo-org:repo-name:ref-name:source-folder"
# ref-name supports:
#   "branch"              - tracks latest commit on branch
#   "tag"                 - immutable reference
#   "branch@commit-sha"  - tracks branch but pinned to specific commit
declare -A ODH_COMPONENT_MANIFESTS=(
    ["kserve"]="opendatahub-io:kserve:release-v0.17@6effd2fc4006d07602b3134fe37d33610eda7bfc:config"
    ["modelcontroller"]="opendatahub-io:odh-model-controller:incubating@229ff3788fc585914586264ad9764f993cc0e2da:config"
)

# RHOAI Component Manifests
declare -A RHOAI_COMPONENT_MANIFESTS=(
    ["kserve"]="red-hat-data-services:kserve:rhoai-3.5-ea.1@a0331d5707e0adda0c2dc0c94b3c9826e0cec45b:config"
    ["modelcontroller"]="red-hat-data-services:odh-model-controller:rhoai-3.5-ea.1@71a24e68920459186d0972d7ed6fcdef5610955e:config"
)

# Select manifests based on platform type
if [ "${ODH_PLATFORM_TYPE:-OpenDataHub}" = "OpenDataHub" ]; then
    echo "Cloning manifests for ODH"
    declare -A COMPONENT_MANIFESTS=()
    for key in "${!ODH_COMPONENT_MANIFESTS[@]}"; do
        COMPONENT_MANIFESTS["$key"]="${ODH_COMPONENT_MANIFESTS[$key]}"
    done
else
    echo "Cloning manifests for RHOAI"
    declare -A COMPONENT_MANIFESTS=()
    for key in "${!RHOAI_COMPONENT_MANIFESTS[@]}"; do
        COMPONENT_MANIFESTS["$key"]="${RHOAI_COMPONENT_MANIFESTS[$key]}"
    done
fi

# Allow overwriting repo using flags component=repo
pattern="^[a-zA-Z0-9_.-]+:[a-zA-Z0-9_.-]+:([a-zA-Z0-9_./-]+|[a-zA-Z0-9_./-]+@[a-f0-9]{7,40}):[a-zA-Z0-9_./-]+$"
if [ "$#" -ge 2 ]; then
    for arg in "${@:2}"; do
        if [[ $arg == --* ]]; then
            arg="${arg:2}"
            IFS="=" read -r key value <<< "$arg"
            if [[ -n "${COMPONENT_MANIFESTS[$key]}" ]]; then
                if [[ ! $value =~ $pattern ]]; then
                    echo "ERROR: The value '$value' does not match the expected format 'repo-org:repo-name:ref-name:source-folder'."
                    continue
                fi
                COMPONENT_MANIFESTS["$key"]=$value
            else
                echo "ERROR: '$key' does not exist in COMPONENT_MANIFESTS, it will be skipped."
                echo "Available components are: [${!COMPONENT_MANIFESTS[@]}]"
                exit 1
            fi
        fi
    done
fi

TMP_DIR=$(mktemp -d -t "kserve-manifests.XXXXXXXXXX")
trap '{ rm -rf -- "$TMP_DIR"; }' EXIT

function try_fetch_ref()
{
    local repo=$1
    local ref_type=$2
    local ref=$3

    local git_ref="refs/$ref_type/$ref"

    if git ls-remote --exit-code "$repo" "$git_ref" &>/dev/null; then
        if git fetch -q --depth 1 "$repo" "$git_ref" && git reset -q --hard FETCH_HEAD; then
            return 0
        else
            echo "ERROR: Failed to fetch $ref from $repo"
            return 1
        fi
    fi
    return 1
}

function git_fetch_ref()
{
    local repo=$1
    local ref=$2
    local dir=$3

    mkdir -p $dir
    pushd $dir &>/dev/null
    git init -q

    if [[ $ref =~ ^([a-zA-Z0-9_./-]+)@([a-f0-9]{7,40})$ ]]; then
        local commit_sha="${BASH_REMATCH[2]}"

        git remote add origin $repo
        if ! git fetch --depth 1 -q origin $commit_sha; then
            echo "ERROR: Failed to fetch from repository $repo"
            popd &>/dev/null
            return 1
        fi
        if ! git reset -q --hard $commit_sha 2>/dev/null; then
            echo "ERROR: Commit SHA $commit_sha not found in repository $repo"
            popd &>/dev/null
            return 1
        fi
    else
        if try_fetch_ref "$repo" "tags" "$ref" || try_fetch_ref "$repo" "heads" "$ref"; then
            :
        else
            echo "ERROR: '$ref' is not a valid branch, tag, or commit SHA in repository $repo"
            popd &>/dev/null
            return 1
        fi
    fi

    popd &>/dev/null
}

# For kserve (local), copy from repo root config/
for key in "${!COMPONENT_MANIFESTS[@]}"; do
    IFS=':' read -r -a repo_info <<< "${COMPONENT_MANIFESTS[$key]}"
    repo_org="${repo_info[0]}"
    repo_name="${repo_info[1]}"
    repo_ref="${repo_info[2]}"
    source_path="${repo_info[3]}"

    echo -e "\033[32mCloning \033[33m${key}\033[32m:\033[0m ${COMPONENT_MANIFESTS[$key]}"

    if [[ "${repo_name}" == "kserve" && -d "${REPO_ROOT}/${source_path}" ]]; then
        echo "  Using local kserve config"
        mkdir -p "${DST_MANIFESTS_DIR}/${key}"
        cp -rf "${REPO_ROOT}/${source_path}"/* "${DST_MANIFESTS_DIR}/${key}/"
        rm -rf "${DST_MANIFESTS_DIR}/${key}/kserve-module"
    else
        repo_url="${GITHUB_URL}/${repo_org}/${repo_name}"
        repo_dir="${TMP_DIR}/${key}"

        if ! git_fetch_ref ${repo_url} ${repo_ref} ${repo_dir}; then
            echo "ERROR: Failed to fetch ref '${repo_ref}' from '${repo_url}' for component '${key}'"
            exit 1
        fi

        mkdir -p "${DST_MANIFESTS_DIR}/${key}"
        cp -rf "${repo_dir}/${source_path}"/* "${DST_MANIFESTS_DIR}/${key}/"
    fi

    echo "  ${key}: $(find "${DST_MANIFESTS_DIR}/${key}" -type f | wc -l) files"
done

echo "Done."
