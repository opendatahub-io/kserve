# Chaos Validation (operator-chaos)

This directory integrates [operator-chaos](https://github.com/opendatahub-io/operator-chaos)
for shift-left upgrade validation at **Level 1** maturity.

A GitHub Actions workflow (`.github/workflows/chaos-validate.yml`) runs on PRs
that touch API types, controllers, CRDs, or the knowledge model and:

- Validates the knowledge model (`validate --knowledge`, `preflight --local`)
- Validates all experiment YAML files
- Diffs the knowledge model between base and PR branches (`diff --breaking`)
- Diffs CRD schemas between base and PR branches (`diff-crds`)
- Previews upgrade experiments (`simulate-upgrade --dry-run`)

## Directory structure

```text
chaos/
  knowledge/
    kserve.yaml          # Knowledge model describing operator topology
  experiments/
    main-controller-kill.yaml
    llm-controller-isolation.yaml
    isvc-config-corruption.yaml
    isvc-validator-disrupt.yaml
    crashloop-inject.yaml
    dependency-odh-model-controller-kill.yaml
    image-corrupt.yaml
    ownerref-orphan.yaml
    pdb-block.yaml
    resource-deletion-service.yaml
    route-host-collision.yaml
    route-tls-mutation.yaml
```

## Knowledge model

`knowledge/kserve.yaml` describes KServe's operator topology:

| Component | Type | Webhooks | Finalizers | Dependencies |
|-----------|------|----------|------------|--------------|
| kserve-controller-manager | Deployment | 7 (2 mutating, 5 validating) | 2 | none |
| llmisvc-controller-manager | Deployment | 4 (validating) | 1 | kserve-controller-manager |
| kserve-localmodel-controller-manager | Deployment | 1 (validating) | 1 | kserve-controller-manager |
| kserve-localmodelnode-agent | DaemonSet | none | none | kserve-localmodel-controller-manager |

Each component lists its managed resources (Deployments, ServiceAccounts,
Secrets, Leases, ConfigMaps) and steady-state checks for convergence
verification.

## Local validation

```sh
make chaos-validate
```

This downloads `operator-chaos` to `bin/` and validates the knowledge model
and all experiments.

## Maintenance

When CRDs, webhooks, managed resources, or controller topology change,
update `knowledge/kserve.yaml` in the same PR. The GHA workflow diffs the
knowledge model against the base branch and flags breaking changes.

## Maturity levels

| Level | Status | Description |
|-------|--------|-------------|
| L1 | Implemented | GHA workflow, knowledge model, experiment validation |
| L2 | Future | ChaosClient SDK integration into controller envtests |
| L3 | Future | Live experiment execution on a KinD/OCP cluster |
| L4 | Future | OLM channel-hop simulation with upgrade playbooks |

## References

- [operator-chaos documentation](https://opendatahub-io.github.io/operator-chaos/)
- [operator-chaos repository](https://github.com/opendatahub-io/operator-chaos)
