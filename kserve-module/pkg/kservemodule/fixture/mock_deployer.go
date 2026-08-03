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

func (m *MockDeployer) LastCall() *deploy.DeployInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Calls) == 0 {
		return nil
	}
	return &m.Calls[len(m.Calls)-1]
}
