#!/usr/bin/env bash
set -e

GITHUB_URL="${GITHUB_URL:-https://github.com}"
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)"
MODULE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${MODULE_DIR}/.." && pwd)"
DST_MANIFESTS_DIR="${1:-${MODULE_DIR}/opt/manifests}"
PREFETCHED_MANIFESTS_DIR="${PREFETCHED_MANIFESTS_DIR:-${MODULE_DIR}/prefetched-manifests-odh}"

# ODH Component Manifests
# Format: "repo-org:repo-name:ref-name:source-folder"
# ref-name supports:
#   "branch"              - tracks latest commit on branch
#   "tag"                 - immutable reference
#   "branch@commit-sha"  - tracks branch but pinned to specific commit
declare -A ODH_COMPONENT_MANIFESTS=(
    ["kserve"]="opendatahub-io:kserve:master:config"
    ["modelexpress"]="opendatahub-io:modelexpress:main:config/manifests"
)

declare -A ODH_RELEASE_COMPONENT_MANIFESTS=(
    ["kserve"]="opendatahub-io:kserve:release-v0.17:config"
    ["modelexpress"]="opendatahub-io:modelexpress:main:config/manifests"
)

declare -A PREFETCHED_COMPONENT_MANIFESTS=(
    ["modelcontroller"]="${PREFETCHED_MANIFESTS_DIR}/modelcontroller"
)

echo "Cloning manifests for ODH"
declare -A COMPONENT_MANIFESTS=()
for key in "${!ODH_COMPONENT_MANIFESTS[@]}"; do
    COMPONENT_MANIFESTS["$key"]="${ODH_COMPONENT_MANIFESTS[$key]}"
done

# Release branches ship a `release` marker file → use ODH_RELEASE_COMPONENT_MANIFESTS.
# main has no marker, so behavior is unchanged there.
if [ -f "${SCRIPT_DIR}/release" ]; then
    echo "Release marker found: using ODH_RELEASE_COMPONENT_MANIFESTS"
    for key in "${!ODH_RELEASE_COMPONENT_MANIFESTS[@]}"; do
        COMPONENT_MANIFESTS["$key"]="${ODH_RELEASE_COMPONENT_MANIFESTS[$key]}"
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
            elif [[ -n "${PREFETCHED_COMPONENT_MANIFESTS[$key]}" ]]; then
                if [[ ! $value =~ $pattern ]]; then
                    echo "ERROR: The value '$value' does not match the expected format 'repo-org:repo-name:ref-name:source-folder'."
                    continue
                fi
                COMPONENT_MANIFESTS["$key"]=$value
                unset 'PREFETCHED_COMPONENT_MANIFESTS[$key]'
            else
                echo "ERROR: '$key' does not exist in the component manifests, it will be skipped."
                echo "Available components are: [${!COMPONENT_MANIFESTS[*]} ${!PREFETCHED_COMPONENT_MANIFESTS[*]}]"
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

    mkdir -p "$dir"
    pushd "$dir" &>/dev/null
    git init -q

    if [[ $ref =~ ^([a-zA-Z0-9_./-]+)@([a-f0-9]{7,40})$ ]]; then
        local commit_sha="${BASH_REMATCH[2]}"

        git remote add origin "$repo"
        if ! git fetch --depth 1 -q origin "$commit_sha"; then
            echo "ERROR: Failed to fetch from repository $repo"
            popd &>/dev/null
            return 1
        fi
        if ! git reset -q --hard "$commit_sha" 2>/dev/null; then
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

for key in "${!PREFETCHED_COMPONENT_MANIFESTS[@]}"; do
    source_dir="${PREFETCHED_COMPONENT_MANIFESTS[$key]}"
    if [[ ! -f "${source_dir}/.source.yaml" ]]; then
        echo "ERROR: Prefetched manifests for '${key}' lack .source.yaml"
        exit 1
    fi
    source_revision=$(awk '$1 == "revision:" { print $2 }' "${source_dir}/.source.yaml")
    if [[ ! "${source_revision}" =~ ^[0-9a-f]{40}$ ]]; then
        echo "ERROR: Prefetched manifests for '${key}' lack a full source revision"
        exit 1
    fi

    echo -e "\033[32mCopying prefetched \033[33m${key}\033[32m manifests:\033[0m ${source_dir}"
    rm -rf "${DST_MANIFESTS_DIR:?}/${key}"
    mkdir -p "${DST_MANIFESTS_DIR}/${key}"
    cp -rf "${source_dir}"/. "${DST_MANIFESTS_DIR}/${key}/"
    echo "  ${key}: $(find "${DST_MANIFESTS_DIR}/${key}" -type f | wc -l) files"
done

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
        rm -rf "${DST_MANIFESTS_DIR:?}/${key}"
        mkdir -p "${DST_MANIFESTS_DIR}/${key}"
        cp -rf "${REPO_ROOT}/${source_path}"/* "${DST_MANIFESTS_DIR}/${key}/"
        rm -rf "${DST_MANIFESTS_DIR}/${key}/kserve-module"
    else
        repo_url="${GITHUB_URL}/${repo_org}/${repo_name}"
        repo_dir="${TMP_DIR}/${key}"

        if ! git_fetch_ref "${repo_url}" "${repo_ref}" "${repo_dir}"; then
            echo "ERROR: Failed to fetch ref '${repo_ref}' from '${repo_url}' for component '${key}'"
            exit 1
        fi

        rm -rf "${DST_MANIFESTS_DIR:?}/${key}"
        mkdir -p "${DST_MANIFESTS_DIR}/${key}"
        cp -rf "${repo_dir}/${source_path}"/* "${DST_MANIFESTS_DIR}/${key}/"
    fi

    echo "  ${key}: $(find "${DST_MANIFESTS_DIR}/${key}" -type f | wc -l) files"
done

echo "Done."
