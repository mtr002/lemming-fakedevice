// Copyright 2024 Google LLC
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

package gnoi

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	log "github.com/golang/glog"
	rpc "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	plqpb "github.com/openconfig/gnoi/packet_link_qualification"
	spb "github.com/openconfig/gnoi/system"
	"github.com/openconfig/lemming/fault"
	configpb "github.com/openconfig/lemming/proto/config"
)

// Design Approach 1: Configuration-Driven Failure Injection
// This approach uses configuration to define failure scenarios

// FailureConfig represents failure injection configuration (simplified)
type FailureConfig struct {
	Enabled                bool
	GlobalFailureRate      float32
	RebootFailureRate      float32
	SwitchoverFailureRate  float32
	ProcessKillFailureRate float32
	LinkQualFailureRates   map[string]float32 // phase -> rate
}

// SimpleFailureInjector demonstrates a basic failure injection approach
type SimpleFailureInjector struct {
	config *FailureConfig
	rng    *rand.Rand
}

// NewSimpleFailureInjector creates a basic failure injector
func NewSimpleFailureInjector(config *FailureConfig) *SimpleFailureInjector {
	return &SimpleFailureInjector{
		config: config,
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Design Approach 2: Integration with Existing System Service
// Show how to modify the existing system service to include failure injection

// Enhanced system struct with failure injection
type enhancedSystem struct {
	*system         // embed existing system
	failureInjector *SimpleFailureInjector
}

// Enhanced Reboot with failure injection
func (s *enhancedSystem) RebootWithFailures(ctx context.Context, r *spb.RebootRequest) (*spb.RebootResponse, error) {
	// Pre-operation failure injection
	if err := s.failureInjector.maybeInjectRebootFailure(ctx, r); err != nil {
		return nil, err
	}

	// Call original reboot logic
	response, err := s.system.Reboot(ctx, r)

	// Post-operation failure injection (for partial failures)
	if err == nil {
		if postErr := s.failureInjector.maybeInjectPostRebootFailure(ctx, r); postErr != nil {
			return nil, postErr
		}
	}

	return response, err
}

// Enhanced SwitchControlProcessor with failure injection
func (s *enhancedSystem) SwitchControlProcessorWithFailures(ctx context.Context, r *spb.SwitchControlProcessorRequest) (*spb.SwitchControlProcessorResponse, error) {
	// Check for pre-configured failure scenarios
	if err := s.failureInjector.maybeInjectSwitchoverFailure(ctx, r); err != nil {
		return nil, err
	}

	// Start the operation but inject delayed failures
	opID := fmt.Sprintf("switchover_%d", time.Now().UnixNano())

	// Schedule potential delayed failure during operation
	go s.failureInjector.scheduleDelayedSwitchoverFailure(opID, 2*time.Second)

	// Call original switchover logic with failure monitoring
	response, err := s.switchControlProcessorWithMonitoring(ctx, r, opID)

	return response, err
}

// Design Approach 3: Phase-Aware Failure Injection for Link Qualification
// This shows how to inject failures at different phases of complex operations

// Enhanced LinkQualification with phase-aware failure injection
func (lq *linkQualification) CreateWithFailures(ctx context.Context, req *plqpb.CreateRequest) (*plqpb.CreateResponse, error) {
	// Pre-validate and potentially inject early failures
	for _, config := range req.GetInterfaces() {
		if err := lq.maybeInjectSetupFailure(ctx, config); err != nil {
			return &plqpb.CreateResponse{
				Status: map[string]*rpc.Status{
					config.GetId(): {
						Code:    int32(codes.FailedPrecondition),
						Message: err.Error(),
					},
				},
			}, nil
		}
	}

	// Call original Create logic
	response, err := lq.Create(ctx, req)

	// Inject runtime failures for successfully created qualifications
	for id, status := range response.GetStatus() {
		if status.GetCode() == int32(codes.OK) {
			go lq.injectRuntimeFailures(id)
		}
	}

	return response, err
}

// Design Approach 4: Fault Client Integration
// Show how existing fault client can be extended for gNOI services

// GnoiFailureClient extends the existing fault client for gNOI-specific failures
type GnoiFailureClient struct {
	*fault.Client // embed existing fault client
	config        *configpb.LemmingConfig
}

// NewGnoiFailureClient creates a gNOI-specific failure client
func NewGnoiFailureClient(baseClient *fault.Client, config *configpb.LemmingConfig) *GnoiFailureClient {
	return &GnoiFailureClient{
		Client: baseClient,
		config: config,
	}
}

// InjectSystemRebootFailure demonstrates using fault client for system reboot
func (gfc *GnoiFailureClient) InjectSystemRebootFailure() error {
	// Use fault client to intercept system.Reboot calls
	systemClient := gfc.systemRebootInterceptor()

	// Set request modifier to inject invalid parameters
	systemClient.SetReqMod(func(req *spb.RebootRequest) *spb.RebootRequest {
		// Example: Force invalid method to trigger failure
		req.Method = spb.RebootMethod(999) // Invalid method
		return req
	})

	// Set response modifier to inject custom errors
	systemClient.SetRespMod(func(resp *spb.RebootResponse, err error) (*spb.RebootResponse, error) {
		// Inject custom failure even if operation would succeed
		return nil, status.Errorf(codes.Internal, "injected reboot failure: hardware malfunction")
	})

	return nil
}

// Design Approach 5: State-Based Failure Injection
// This approach tracks system state and injects failures based on conditions

// StatefulFailureInjector tracks system state for intelligent failure injection
type StatefulFailureInjector struct {
	config           *FailureConfig
	systemState      map[string]interface{}
	recentOperations []OperationRecord
	maxRecords       int
}

type OperationRecord struct {
	Type      string
	Timestamp time.Time
	Success   bool
	Component string
}

// InjectFailureBasedOnState decides whether to inject failure based on system history
func (sfi *StatefulFailureInjector) InjectFailureBasedOnState(opType string, component string) error {
	// Example: Don't allow reboot if recent switchover failed
	if opType == "reboot" {
		for _, op := range sfi.recentOperations {
			if op.Type == "switchover" && !op.Success &&
				time.Since(op.Timestamp) < 5*time.Minute {
				return status.Errorf(codes.FailedPrecondition,
					"reboot not allowed: recent switchover failure on %s", op.Component)
			}
		}
	}

	// Example: Increase failure probability if too many operations recently
	recentCount := 0
	for _, op := range sfi.recentOperations {
		if time.Since(op.Timestamp) < 1*time.Minute {
			recentCount++
		}
	}

	if recentCount > 5 {
		return status.Errorf(codes.ResourceExhausted,
			"system overloaded: too many recent operations (%d)", recentCount)
	}

	return nil
}

// Implementation examples for the simple failure injector

func (sfi *SimpleFailureInjector) maybeInjectRebootFailure(ctx context.Context, req *spb.RebootRequest) error {
	if !sfi.config.Enabled {
		return nil
	}

	// Check global failure rate
	if sfi.config.GlobalFailureRate > 0 && sfi.rng.Float32() < sfi.config.GlobalFailureRate {
		return status.Errorf(codes.Internal, "global failure injection triggered")
	}

	// Check reboot-specific failure rate
	if sfi.config.RebootFailureRate > 0 && sfi.rng.Float32() < sfi.config.RebootFailureRate {
		// Select random failure type
		failureTypes := []string{
			"hardware_error", "power_failure", "timeout", "partial_failure",
		}
		failureType := failureTypes[sfi.rng.Intn(len(failureTypes))]

		switch failureType {
		case "hardware_error":
			return status.Errorf(codes.FailedPrecondition, "hardware error prevented reboot")
		case "power_failure":
			return status.Errorf(codes.Unavailable, "power failure during reboot")
		case "timeout":
			return status.Errorf(codes.DeadlineExceeded, "reboot operation timed out")
		case "partial_failure":
			return status.Errorf(codes.Internal, "reboot partially completed but failed")
		}
	}

	return nil
}

func (sfi *SimpleFailureInjector) maybeInjectPostRebootFailure(ctx context.Context, req *spb.RebootRequest) error {
	// Simulate post-reboot failures (component didn't come back up, etc.)
	if sfi.rng.Float32() < 0.1 { // 10% chance of post-reboot failure
		return status.Errorf(codes.Internal, "system failed to stabilize after reboot")
	}
	return nil
}

func (sfi *SimpleFailureInjector) maybeInjectSwitchoverFailure(ctx context.Context, req *spb.SwitchControlProcessorRequest) error {
	if !sfi.config.Enabled || sfi.config.SwitchoverFailureRate == 0 {
		return nil
	}

	if sfi.rng.Float32() < sfi.config.SwitchoverFailureRate {
		failureTypes := []string{
			"communication_failure", "state_sync_failure", "hardware_failure",
		}
		failureType := failureTypes[sfi.rng.Intn(len(failureTypes))]

		switch failureType {
		case "communication_failure":
			return status.Errorf(codes.Unavailable, "communication failure between supervisors")
		case "state_sync_failure":
			return status.Errorf(codes.Internal, "failed to synchronize state during switchover")
		case "hardware_failure":
			return status.Errorf(codes.FailedPrecondition, "hardware failure on target supervisor")
		}
	}

	return nil
}

func (sfi *SimpleFailureInjector) scheduleDelayedSwitchoverFailure(opID string, delay time.Duration) {
	time.Sleep(delay)
	log.Infof("Delayed switchover failure triggered for operation %s", opID)
	// In real implementation, this would signal the ongoing operation to fail
}

func (lq *linkQualification) maybeInjectSetupFailure(ctx context.Context, config *plqpb.QualificationConfiguration) error {
	// Example: 5% chance of setup failure
	if rand.Float32() < 0.05 {
		return fmt.Errorf("setup failure: interface %s not ready", config.GetInterfaceName())
	}
	return nil
}

func (lq *linkQualification) injectRuntimeFailures(qualID string) {
	// Simulate random runtime failures during qualification
	time.Sleep(time.Duration(rand.Intn(5)) * time.Second)

	if rand.Float32() < 0.1 { // 10% chance of runtime failure
		log.Infof("Injecting runtime failure for qualification %s", qualID)
		// In real implementation, this would update the qualification state to ERROR
	}
}

// Helper methods for monitoring and fault client integration
func (s *enhancedSystem) switchControlProcessorWithMonitoring(ctx context.Context, r *spb.SwitchControlProcessorRequest, opID string) (*spb.SwitchControlProcessorResponse, error) {
	// Wrap the original call with monitoring for delayed failures
	done := make(chan bool, 1)
	var response *spb.SwitchControlProcessorResponse
	var err error

	go func() {
		response, err = s.system.SwitchControlProcessor(ctx, r)
		done <- true
	}()

	// Monitor for delayed failure injection
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return response, err
		case <-ticker.C:
			if delayedErr := s.failureInjector.checkDelayedFailure(opID); delayedErr != nil {
				return nil, delayedErr
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (sfi *SimpleFailureInjector) checkDelayedFailure(opID string) error {
	// Check if this operation should fail due to delayed failure injection
	// This is a simplified implementation
	return nil
}

func (gfc *GnoiFailureClient) systemRebootInterceptor() interface{} {
	// Return a mock interceptor for demonstration
	// In real implementation, this would return the proper fault client
	return struct{}{}
}
