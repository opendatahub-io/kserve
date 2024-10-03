---
name: Upstream sync
about: Outlines the process to synchonize ODH fork with upstream KServe repositories
title: Synchronization with upstream repositories
labels: ''
assignees: ''

---

This repository is configured with a GitHub App that automatically creates pull requests to incorporate upstream changes into ODH's fork. Because of adaptations to enhance UX with OpenShift, changes need to be reviewed carefully. The most notable points of conflict are:

* ODH fork removes `ClusterServingRuntime` CRD. If new upstream development includes changes that involve this CRD, the code needs to be adapted.
* ODH fork uses OpenShift's Serving Certificates, instead of relying on cert-manager. If new upstream development includes changes that involve cert-manager, the code needs to be adapted.
* Files related to GitHub Actions have some changes because of ODH tooling.

It is important to note that the synchronization should be done both for the `master` branch, and also for `release-*` branches (this ticket should be used to cover both). At the time of writing, only `release-v0.11` was an active release branch.

## Code synchronization

To synchronize ODH's KServe fork with upstream, do the following steps:

1. Search for automatic synchronization pull requests. You can use [this link](https://github.com/opendatahub-io/kserve/pulls/app%2Fpull).
    * If there are no PRs, no synchronization should be needed. Although, it is worth to re-check one or two days later, in case the bot hasn't run.
1. Open the found pull request. If there is more than one pull request, it is (most likely) one per branch that needs synchronization, and you need to do the rest of the steps for each pull request. 
1. Go to the bottom of the pull request page, where the "Merge pull request" button is located. If you see the "This branch has conflicts that must be resolved" message, you will need to do the synchronization manually (skip to the following set of steps).
1. Verify if there is any failing check. You may need to give approval to run checks: one approval to run openshift-ci and another one for GitHub Actions. Trigger re-tests if needed.
    * If some check is giving a true positive, you will need to do a manual sync (as that typically needs code changes/adaptations).
1. Carefully review the new changes, to make sure that no unwanted logic is included (typically, it would be around `ClusterServingRuntimes` and cert-manager stuff).
    * If there are unwanted changes, you will need to do a manual sync (as that typically needs code changes/adaptations).
1. If everything is good. Approve the PR and let openshift-ci merge it.
    * You can either use GitHub's review/approval feature, or you can leave a comment with both `/approve` and `/lgtm` commands. 

If you need to syncrhonize manually, do the following steps:

1. If you haven't done so, create your own fork of ODH's KServe: https://github.com/opendatahub-io/kserve/fork
1. If you haven't done so, checkout your fork of KServe: `git clone git@github.com:{$YOUR_GITHUB_USER}/kserve.git && cd kserve`
    * If you already forked ODH's KServe, make sure you fetch the latest code from ODH repositories. Fetch the relevant branch you want to synchronize.
1. Checkout to the branch you want to synchronize.
    * e.g. If you want to synchronize `release-v0.11`, do `git branch release-v0.11-sync origin/release-v0.11 && git checkout -b release-v0.11-sync`.
1. If you haven't done so, add upstream as a remote: `git remote add kserve git@github.com:kserve/kserve.git`
1. Fetch latest code from upstream KServe: `git fetch kserve`
1. Do the sync as a merge.
    * e.g. If you want to synchronize `release-v0.11`, do `git merge kserve/release-0.11`.
1. If there are any conflicts, or there are any adaptations that need to be done, do them now.
1. Commit any changes to your local branch: `git commit --signoff`.
1. Push the local branch to your fork: `git push origin release-v0.11-sync`.
1. Open a pull request: e.g. go to https://github.com/{$YOUR_GITHUB_USER}/kserve/pull/new/release-v0.11-sync
1. You will need help from one of your peers. Ask somebody to carefully review and test your synchronization PR, and approve it.
1. Once your PR is merged, you can close the automatic PR that the GitHub App (the bot) created.

## Manifests replication to `odh-manifests` repository

At the time of writing, ODH operator is installing components using manifests from `odh-manifests`. There is active work to deprecate that repository and use manifests located on each component repository. While that work finishes, you need to replicate manifests if something changed inside the `config/` folder as part of the syncrhonization.

You need to do manifest syncrhonization **only** for the most recent release branch.

1. If you haven't done so, create your own fork of `odh-manifests` repository: https://github.com/opendatahub-io/odh-manifests/fork
1. If you haven't done so, checkout your fork of `odh-manifests`: `git clone git@github.com:{$YOUR_GITHUB_USER}/odh-manifests.git && cd odh-manifests`
    * If you already forked `odh-manifests`, make sure you fetch the latest code from ODH repository `master` branch.
1. Create a branch to do the update: `git branch kserve-manifest-update master && git checkout kserve-manifest-update`
1. Follow the instructions at https://github.com/opendatahub-io/odh-manifests/tree/master/kserve#updating-the-manifests.
    * You may need to change the hack/kustomize.yaml file to point to the right `ref`: https://github.com/opendatahub-io/odh-manifests/blob/master/kserve/hack/kustomization.yaml#L5.
1. Run `git diff`, or your favorite diff tool and carefully review the changes.
    * There are some patches in the [kserve/base folder](https://github.com/opendatahub-io/odh-manifests/tree/master/kserve/base). As part of reviewing the manifest update changes, check if the patches need to be updated. Updating the patches is a manual process.
1. Review and update the `params.env` file, if image references need to be updated: https://github.com/opendatahub-io/odh-manifests/blob/master/kserve/base/params.env
1. Test that the manifests build correctly: `kustomize build ./kserve/base`.
    * You should see the manifests being outputted to the console.
    * If you see any errors, you will need to fix them. Usually, no YAML is going to be generated as the result of an error.
1. Commit any changes to your local branch: `git commit --signoff`.
1. Push the local branch to your fork: `git push origin kserve-manifest-update`.
1. Open a pull request: e.g. go to https://github.com/{$YOUR_GITHUB_USER}/odh-manifests/pull/new/kserve-manifest-update
1. You will need help from one of your peers. Ask somebody to carefully review and test your synchronization PR, and approve it. Let openshift-ci to merge your pull request.

