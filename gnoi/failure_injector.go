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
	"sync"
	"time"

	log "github.com/golang/glog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	plqpb "github.com/openconfig/gnoi/packet_link_qualification"
	spb "github.com/openconfig/gnoi/system"
	configpb "github.com/openconfig/lemming/proto/config"
)

// FailureInjector manages failure injection for gNOI services
type FailureInjector struct {
	config *configpb.GnoiFailureConfig
	mu     sync.RWMutex
	rng    *rand.Rand

	// Track ongoing operations for failure injection
	activeOperations map[string]*OperationContext
}

// OperationContext tracks context for ongoing operations
type OperationContext struct {
	ID          string
	Type        OperationType
	StartTime   time.Time
	FailureTime *time.Time // When failure should occur (if scheduled)
	Phase       OperationPhase
}

type OperationType int

const (
	OperationTypeReboot OperationType = iota
	OperationTypeSwitchover
	OperationTypeProcessKill
	OperationTypeLinkQualification
)

type OperationPhase int

const (
	PhaseInitial OperationPhase = iota
	PhaseSetup
	PhaseRunning
	PhaseTeardown
	PhaseCompleted
)

// NewFailureInjector creates a new failure injector
func NewFailureInjector(config *configpb.GnoiFailureConfig) *FailureInjector {
	if config == nil || !config.GetEnabled() {
		return &FailureInjector{config: &configpb.GnoiFailureConfig{Enabled: false}}
	}

	return &FailureInjector{
		config:           config,
		rng:              rand.New(rand.NewSource(time.Now().UnixNano())),
		activeOperations: make(map[string]*OperationContext),
	}
}

// ShouldInjectFailure determines if a failure should be injected for a given operation
func (fi *FailureInjector) ShouldInjectFailure(opType OperationType, phase OperationPhase) (bool, error) {
	fi.mu.RLock()
	defer fi.mu.RUnlock()

	if !fi.config.GetEnabled() {
		return false, nil
	}

	// Check global failure rate first
	if fi.config.GetGlobalFailureRate() > 0 && fi.rng.Float32() < fi.config.GetGlobalFailureRate() {
		return true, fi.generateError(opType, phase, "global_failure_injection")
	}

	// Check operation-specific failure rates
	switch opType {
	case OperationTypeReboot:
		return fi.shouldInjectRebootFailure(phase)
	case OperationTypeSwitchover:
		return fi.shouldInjectSwitchoverFailure(phase)
	case OperationTypeProcessKill:
		return fi.shouldInjectProcessKillFailure(phase)
	case OperationTypeLinkQualification:
		return fi.shouldInjectLinkQualFailure(phase)
	}

	return false, nil
}

// InjectRebootFailure injects failures into reboot operations
func (fi *FailureInjector) InjectRebootFailure(ctx context.Context, req *spb.RebootRequest) error {
	shouldFail, err := fi.ShouldInjectFailure(OperationTypeReboot, PhaseInitial)
	if !shouldFail {
		return nil
	}

	rebootConfig := fi.config.GetSystemFailure().GetRebootFailure()
	if len(rebootConfig.GetFailureModes()) == 0 {
		return status.Errorf(codes.Internal, "reboot operation failed due to injected failure")
	}

	// Select random failure mode
	mode := rebootConfig.GetFailureModes()[fi.rng.Intn(len(rebootConfig.GetFailureModes()))]

	switch mode {
	case configpb.RebootFailureMode_REBOOT_FAILURE_HANG:
		return status.Errorf(codes.DeadlineExceeded, "reboot operation hung and did not complete")
	case configpb.RebootFailureMode_REBOOT_FAILURE_PARTIAL:
		return status.Errorf(codes.Internal, "reboot failed midway through operation")
	case configpb.RebootFailureMode_REBOOT_FAILURE_POWER_LOSS:
		return status.Errorf(codes.Unavailable, "power failure occurred during reboot")
	case configpb.RebootFailureMode_REBOOT_FAILURE_HARDWARE_ERROR:
		return status.Errorf(codes.FailedPrecondition, "hardware error prevented reboot")
	default:
		return status.Errorf(codes.Internal, "unknown reboot failure")
	}
}

// InjectSwitchoverFailure injects failures into switchover operations
func (fi *FailureInjector) InjectSwitchoverFailure(ctx context.Context, req *spb.SwitchControlProcessorRequest) error {
	shouldFail, err := fi.ShouldInjectFailure(OperationTypeSwitchover, PhaseInitial)
	if !shouldFail {
		return nil
	}

	switchoverConfig := fi.config.GetSystemFailure().GetSwitchoverFailure()
	if len(switchoverConfig.GetFailureModes()) == 0 {
		return status.Errorf(codes.Internal, "switchover operation failed due to injected failure")
	}

	mode := switchoverConfig.GetFailureModes()[fi.rng.Intn(len(switchoverConfig.GetFailureModes()))]

	switch mode {
	case configpb.SwitchoverFailureMode_SWITCHOVER_FAILURE_COMMUNICATION:
		return status.Errorf(codes.Unavailable, "communication failure between supervisors")
	case configpb.SwitchoverFailureMode_SWITCHOVER_FAILURE_STATE_SYNC:
		return status.Errorf(codes.Internal, "state synchronization failed during switchover")
	case configpb.SwitchoverFailureMode_SWITCHOVER_FAILURE_HARDWARE:
		return status.Errorf(codes.FailedPrecondition, "hardware failure on target supervisor")
	case configpb.SwitchoverFailureMode_SWITCHOVER_FAILURE_TIMEOUT:
		return status.Errorf(codes.DeadlineExceeded, "switchover operation timed out")
	default:
		return status.Errorf(codes.Internal, "unknown switchover failure")
	}
}

// InjectProcessKillFailure injects failures into process kill operations
func (fi *FailureInjector) InjectProcessKillFailure(ctx context.Context, req *spb.KillProcessRequest, processName string) error {
	// Check if process is protected
	killConfig := fi.config.GetSystemFailure().GetProcessKillFailure()
	for _, protected := range killConfig.GetProtectedProcesses() {
		if protected == processName {
			return status.Errorf(codes.PermissionDenied, "process %s is protected and cannot be killed", processName)
		}
	}

	shouldFail, err := fi.ShouldInjectFailure(OperationTypeProcessKill, PhaseInitial)
	if !shouldFail {
		return nil
	}

	if len(killConfig.GetFailureModes()) == 0 {
		return status.Errorf(codes.Internal, "process kill operation failed due to injected failure")
	}

	mode := killConfig.GetFailureModes()[fi.rng.Intn(len(killConfig.GetFailureModes()))]

	switch mode {
	case configpb.ProcessKillFailureMode_PROCESS_KILL_FAILURE_PERMISSION_DENIED:
		return status.Errorf(codes.PermissionDenied, "insufficient permissions to kill process %s", processName)
	case configpb.ProcessKillFailureMode_PROCESS_KILL_FAILURE_NOT_RESPONDING:
		return status.Errorf(codes.DeadlineExceeded, "process %s not responding to kill signal", processName)
	case configpb.ProcessKillFailureMode_PROCESS_KILL_FAILURE_RESTART_FAILED:
		return status.Errorf(codes.Internal, "process %s killed but restart failed", processName)
	case configpb.ProcessKillFailureMode_PROCESS_KILL_FAILURE_ZOMBIE:
		return status.Errorf(codes.Internal, "process %s became zombie after kill", processName)
	default:
		return status.Errorf(codes.Internal, "unknown process kill failure")
	}
}

// InjectLinkQualFailure injects failures into link qualification operations
func (fi *FailureInjector) InjectLinkQualFailure(ctx context.Context, phase OperationPhase, config *plqpb.QualificationConfiguration) error {
	shouldFail, err := fi.ShouldInjectFailure(OperationTypeLinkQualification, phase)
	if !shouldFail {
		return nil
	}

	lqConfig := fi.config.GetLinkQualificationFailure()
	if len(lqConfig.GetFailureModes()) == 0 {
		return status.Errorf(codes.Internal, "link qualification failed due to injected failure")
	}

	mode := lqConfig.GetFailureModes()[fi.rng.Intn(len(lqConfig.GetFailureModes()))]

	switch mode {
	case configpb.LinkQualFailureMode_LINK_QUAL_FAILURE_INTERFACE_DOWN:
		return status.Errorf(codes.FailedPrecondition, "interface %s went down during qualification", config.GetInterfaceName())
	case configpb.LinkQualFailureMode_LINK_QUAL_FAILURE_HARDWARE_ERROR:
		return status.Errorf(codes.Internal, "hardware error in packet generator")
	case configpb.LinkQualFailureMode_LINK_QUAL_FAILURE_RESOURCE_EXHAUSTION:
		return status.Errorf(codes.ResourceExhausted, "insufficient resources for qualification test")
	case configpb.LinkQualFailureMode_LINK_QUAL_FAILURE_CALIBRATION_ERROR:
		return status.Errorf(codes.Internal, "test calibration failed")
	default:
		return status.Errorf(codes.Internal, "unknown link qualification failure")
	}
}

// Helper methods for operation-specific failure injection
func (fi *FailureInjector) shouldInjectRebootFailure(phase OperationPhase) (bool, error) {
	rebootConfig := fi.config.GetSystemFailure().GetRebootFailure()
	if rebootConfig.GetFailureProbability() > 0 && fi.rng.Float32() < rebootConfig.GetFailureProbability() {
		return true, fi.generateError(OperationTypeReboot, phase, "reboot_failure_injection")
	}
	return false, nil
}

func (fi *FailureInjector) shouldInjectSwitchoverFailure(phase OperationPhase) (bool, error) {
	switchoverConfig := fi.config.GetSystemFailure().GetSwitchoverFailure()
	if switchoverConfig.GetFailureProbability() > 0 && fi.rng.Float32() < switchoverConfig.GetFailureProbability() {
		return true, fi.generateError(OperationTypeSwitchover, phase, "switchover_failure_injection")
	}
	return false, nil
}

func (fi *FailureInjector) shouldInjectProcessKillFailure(phase OperationPhase) (bool, error) {
	killConfig := fi.config.GetSystemFailure().GetProcessKillFailure()
	if killConfig.GetFailureProbability() > 0 && fi.rng.Float32() < killConfig.GetFailureProbability() {
		return true, fi.generateError(OperationTypeProcessKill, phase, "process_kill_failure_injection")
	}
	return false, nil
}

func (fi *FailureInjector) shouldInjectLinkQualFailure(phase OperationPhase) (bool, error) {
	lqConfig := fi.config.GetLinkQualificationFailure()

	var probability float32
	switch phase {
	case PhaseSetup:
		probability = lqConfig.GetSetupFailureProbability()
	case PhaseRunning:
		probability = lqConfig.GetRunningFailureProbability()
	case PhaseTeardown:
		probability = lqConfig.GetTeardownFailureProbability()
	default:
		return false, nil
	}

	if probability > 0 && fi.rng.Float32() < probability {
		return true, fi.generateError(OperationTypeLinkQualification, phase, "link_qual_failure_injection")
	}
	return false, nil
}

func (fi *FailureInjector) generateError(opType OperationType, phase OperationPhase, reason string) error {
	return fmt.Errorf("failure injected: type=%d, phase=%d, reason=%s", opType, phase, reason)
}

// ScheduleDelayedFailure schedules a failure to occur after a delay
func (fi *FailureInjector) ScheduleDelayedFailure(opID string, opType OperationType, delay time.Duration) {
	fi.mu.Lock()
	defer fi.mu.Unlock()

	failureTime := time.Now().Add(delay)
	fi.activeOperations[opID] = &OperationContext{
		ID:          opID,
		Type:        opType,
		StartTime:   time.Now(),
		FailureTime: &failureTime,
		Phase:       PhaseInitial,
	}

	log.Infof("Scheduled delayed failure for operation %s at %v", opID, failureTime)
}

// CheckDelayedFailure checks if a delayed failure should trigger
func (fi *FailureInjector) CheckDelayedFailure(opID string) error {
	fi.mu.RLock()
	op, exists := fi.activeOperations[opID]
	fi.mu.RUnlock()

	if !exists || op.FailureTime == nil {
		return nil
	}

	if time.Now().After(*op.FailureTime) {
		fi.mu.Lock()
		delete(fi.activeOperations, opID)
		fi.mu.Unlock()

		return status.Errorf(codes.Internal, "delayed failure triggered for operation %s", opID)
	}

	return nil
}

// UpdateConfig updates the failure injection configuration
func (fi *FailureInjector) UpdateConfig(config *configpb.GnoiFailureConfig) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.config = config
	log.Infof("Updated failure injection configuration")
}
