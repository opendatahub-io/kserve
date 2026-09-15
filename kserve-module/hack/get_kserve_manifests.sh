#!/usr/bin/env bash
set -e

GITHUB_URL="${KSERVE_MANIFESTS_GITHUB_URL:-https://github.com}"
GIT_RETRIES="${KSERVE_MANIFESTS_GIT_RETRIES:-3}"
GIT_RETRY_DELAY_SECONDS="${KSERVE_MANIFESTS_GIT_RETRY_DELAY_SECONDS:-5}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MODULE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO_ROOT="$(cd "${MODULE_DIR}/.." && pwd)"
DST_MANIFESTS_DIR="${1:-${MODULE_DIR}/opt/manifests}"

# Set KSERVE_MANIFESTS_DEBUG=true to log resolved refs and Git commands.
# KSERVE_MANIFESTS_GITHUB_URL can point to a mirror for isolated testing.
# Set KSERVE_MANIFESTS_GIT_RETRIES and KSERVE_MANIFESTS_GIT_RETRY_DELAY_SECONDS
# to control retries for transient Git transport failures.
if ((BASH_VERSINFO[0] < 4)); then
    echo "ERROR: get_kserve_manifests.sh requires Bash 4 or newer for associative arrays; found Bash ${BASH_VERSION}." >&2
    exit 1
fi

if [[ ! "$GIT_RETRIES" =~ ^[1-9][0-9]*$ || ! "$GIT_RETRY_DELAY_SECONDS" =~ ^[0-9]+$ ]]; then
    echo "ERROR: KSERVE_MANIFESTS_GIT_RETRIES must be positive and KSERVE_MANIFESTS_GIT_RETRY_DELAY_SECONDS must be non-negative." >&2
    exit 1
fi

debug() {
    if [[ "${KSERVE_MANIFESTS_DEBUG:-false}" == "true" ]]; then
        printf '[get_kserve_manifests] %s\n' "$*" >&2
    fi
}

debug "bash=${BASH_VERSION} git=$(git --version) github_url=${GITHUB_URL} retries=${GIT_RETRIES} retry_delay_seconds=${GIT_RETRY_DELAY_SECONDS} destination=${DST_MANIFESTS_DIR}"

# ODH Component Manifests
# Format: "repo-org:repo-name:ref-name:source-folder"
# ref-name supports:
#   "branch"              - tracks latest commit on branch
#   "tag"                 - immutable reference
#   "branch@commit-sha"  - tracks branch but pinned to specific commit
declare -A ODH_COMPONENT_MANIFESTS=(
    ["kserve"]="opendatahub-io:kserve:master:config"
    ["modelcontroller"]="opendatahub-io:odh-model-controller:incubating:config"
    ["wva"]="opendatahub-io:workload-variant-autoscaler:main:legacy/config"
)

declare -A ODH_RELEASE_COMPONENT_MANIFESTS=(
    ["kserve"]="opendatahub-io:kserve:release-v0.17:config"
    ["modelcontroller"]="opendatahub-io:odh-model-controller:main:config"
    ["wva"]="opendatahub-io:workload-variant-autoscaler:main:legacy/config"
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
            else
                echo "ERROR: '$key' does not exist in COMPONENT_MANIFESTS, it will be skipped."
                echo "Available components are: [${!COMPONENT_MANIFESTS[*]}]"
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

    local output reset_output status attempt

    for ((attempt = 1; attempt <= GIT_RETRIES; attempt++)); do
        reset_output=""
        if output=$(git ls-remote --exit-code "$repo" "$git_ref" 2>&1); then
            debug "git ls-remote succeeded: repo=${repo} ref=${git_ref} result=${output//$'\n'/ }"
            if output=$(git fetch --depth 1 "$repo" "$git_ref" 2>&1) && reset_output=$(git reset -q --hard FETCH_HEAD 2>&1); then
                debug "git fetch succeeded: repo=${repo} ref=${git_ref}"
                return 0
            fi
            status=$?
            output="${output} ${reset_output}"
            echo "WARNING: git fetch failed (attempt ${attempt}/${GIT_RETRIES}, exit ${status}) for ${repo} ${git_ref}: ${output//$'\n'/ }" >&2
        else
            status=$?
            # Exit code 2 means the ref does not exist; retrying cannot fix it.
            if ((status == 2)); then
                debug "git ls-remote ref not found: repo=${repo} ref=${git_ref}"
                return 1
            fi
            echo "WARNING: git ls-remote failed (attempt ${attempt}/${GIT_RETRIES}, exit ${status}) for ${repo} ${git_ref}: ${output//$'\n'/ }" >&2
        fi

        if ((attempt < GIT_RETRIES)); then
            sleep "$GIT_RETRY_DELAY_SECONDS"
        fi
    done

    if [[ -n "${output:-}" ]]; then
        echo "ERROR: Failed to fetch ${ref} from ${repo}: ${output//$'\n'/ }" >&2
    else
        echo "ERROR: Failed to fetch ${ref} from ${repo}" >&2
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
        local output status attempt
        for ((attempt = 1; attempt <= GIT_RETRIES; attempt++)); do
            if output=$(git fetch --depth 1 origin "$commit_sha" 2>&1); then
                break
            fi
            status=$?
            echo "WARNING: git fetch failed (attempt ${attempt}/${GIT_RETRIES}, exit ${status}) for ${repo} ${commit_sha}: ${output//$'\n'/ }" >&2
            if ((attempt < GIT_RETRIES)); then
                sleep "$GIT_RETRY_DELAY_SECONDS"
            fi
        done
        if ((attempt > GIT_RETRIES)); then
            echo "ERROR: Failed to fetch commit ${commit_sha} from repository ${repo}: ${output//$'\n'/ }" >&2
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

        if ! git_fetch_ref "$repo_url" "$repo_ref" "$repo_dir"; then
            echo "ERROR: Failed to fetch ref '${repo_ref}' from '${repo_url}' for component '${key}'"
            exit 1
        fi

        if [[ ! -d "${repo_dir}/${source_path}" ]]; then
            echo "ERROR: Source path '${source_path}' does not exist in '${repo_url}' at ref '${repo_ref}'." >&2
            echo "Available top-level paths:" >&2
            git -C "$repo_dir" ls-tree --name-only HEAD >&2
            exit 1
        fi

        mkdir -p "${DST_MANIFESTS_DIR}/${key}"
        cp -rf "${repo_dir}/${source_path}"/* "${DST_MANIFESTS_DIR}/${key}/"
    fi

    echo "  ${key}: $(find "${DST_MANIFESTS_DIR}/${key}" -type f | wc -l) files"
done

echo "Done."
