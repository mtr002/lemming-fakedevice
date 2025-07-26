// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package fault provides configuration-based fault injection management.
package fault

import (
	"fmt"
	"sync"

	log "github.com/golang/glog"

	"github.com/openconfig/lemming/fault"
	configpb "github.com/openconfig/lemming/proto/config"
	faultpb "github.com/openconfig/lemming/proto/fault"
)

// Manager manages configuration-based fault injection for gNOI services.
type Manager struct {
	mu          sync.RWMutex
	interceptor *fault.Interceptor
	faultRules  map[string][]*faultpb.FaultMessage // rpc_method -> faults
}

// NewManager creates a new fault manager with the given configuration.
func NewManager(config *configpb.FaultServiceConfiguration, interceptor *fault.Interceptor) (*Manager, error) {
	if interceptor == nil {
		return nil, fmt.Errorf("interceptor cannot be nil")
	}

	m := &Manager{
		interceptor: interceptor,
		faultRules:  make(map[string][]*faultpb.FaultMessage),
	}

	if config != nil {
		if err := m.loadConfiguration(config); err != nil {
			return nil, fmt.Errorf("failed to load fault configuration: %w", err)
		}
	}

	return m, nil
}

// loadConfiguration loads fault rules from the configuration
func (m *Manager) loadConfiguration(config *configpb.FaultServiceConfiguration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, gnoiFault := range config.GetGnoiFaults() {
		rpcMethod := gnoiFault.GetRpcMethod()
		faults := gnoiFault.GetFaults()

		if len(faults) == 0 {
			continue
		}

		// Store fault rules
		m.faultRules[rpcMethod] = faults

		// Configure faults directly in the interceptor
		m.interceptor.ConfigureFaults(rpcMethod, faults)
	}

	return nil
}

// GetFaultRules returns the configured fault rules for a given RPC method
func (m *Manager) GetFaultRules(rpcMethod string) []*faultpb.FaultMessage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.faultRules[rpcMethod]
}

// HasFaults returns true if there are configured faults for the given RPC method
func (m *Manager) HasFaults(rpcMethod string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	faults, exists := m.faultRules[rpcMethod]
	return exists && len(faults) > 0
}

// GetConfiguredMethods returns all RPC methods that have configured faults
func (m *Manager) GetConfiguredMethods() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	methods := make([]string, 0, len(m.faultRules))
	for method := range m.faultRules {
		methods = append(methods, method)
	}

	return methods
}

// Stop clears all configured fault rules
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Clear all configured faults from the interceptor
	for method := range m.faultRules {
		m.interceptor.ConfigureFaults(method, nil)
		log.Infof("Cleared fault configuration for RPC method: %s", method)
	}

	// Clear fault rules
	m.faultRules = make(map[string][]*faultpb.FaultMessage)
}

// UpdateConfiguration updates the fault configuration at runtime
func (m *Manager) UpdateConfiguration(config *configpb.FaultServiceConfiguration) error {
	// Clear existing configuration
	m.Stop()

	// Load new configuration
	if config != nil {
		if err := m.loadConfiguration(config); err != nil {
			return fmt.Errorf("failed to update fault configuration: %w", err)
		}
	}

	log.Infof("Fault configuration updated successfully")
	return nil
}
