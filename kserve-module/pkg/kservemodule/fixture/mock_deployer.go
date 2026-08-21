package fixture

import (
	"context"
	"sync"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/deploy"
)

type MockDeployer struct {
	mu          sync.Mutex
	Calls       []deploy.DeployInput
	DeployError error
}

func (m *MockDeployer) Deploy(_ context.Context, input deploy.DeployInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Calls = append(m.Calls, input)
	return m.DeployError
}

// CallCount reports how many times Deploy has run. Tests that assert an event
// reached the reconciler poll this, so it has to take the lock the same way
// Deploy does.
func (m *MockDeployer) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.Calls)
}

func (m *MockDeployer) LastCall() *deploy.DeployInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Calls) == 0 {
		return nil
	}
	return &m.Calls[len(m.Calls)-1]
}
