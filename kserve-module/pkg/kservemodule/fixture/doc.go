// Package fixture provides test scaffolding for the kserve-module integration
// tests: CR builders (KserveCR + options), the envtest harness (SetupTestEnv),
// and MockDeployer.
//
// Deployer selection: each Ordered context sets the deployer it needs once in its
// BeforeAll, before creating the CR (Ordered runs contiguously, so it stays in
// effect for every spec in that context). Set the real deployer to assert actual
// cluster state; set MockDeployer when the deployer output is irrelevant: intent
// checks (LastCall), fault injection (DeployError), or status-only tests. Setting
// it before Create (when no reconcile is running yet) keeps the shared deployer
// free of races, and containers stay independent of each other's leftover state.
//
// Note: envtest has no garbage collector, so ownerReference-based deletion is not
// observable here — verify that in e2e.
package fixture
