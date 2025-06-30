// Copyright 2022 Google LLC
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
	"math"
	"strings"
	"sync"
	"time"

	log "github.com/golang/glog"
	"github.com/openconfig/ygnmi/ygnmi"
	rpc "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/openconfig/lemming/gnmi/fakedevice"
	"github.com/openconfig/lemming/gnmi/oc"
	"github.com/openconfig/lemming/gnmi/oc/ocpath"
	configpb "github.com/openconfig/lemming/proto/config"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	bpb "github.com/openconfig/gnoi/bgp"
	cmpb "github.com/openconfig/gnoi/cert"
	diagpb "github.com/openconfig/gnoi/diag"
	frpb "github.com/openconfig/gnoi/factory_reset"
	fpb "github.com/openconfig/gnoi/file"
	hpb "github.com/openconfig/gnoi/healthz"
	lpb "github.com/openconfig/gnoi/layer2"
	mpb "github.com/openconfig/gnoi/mpls"
	ospb "github.com/openconfig/gnoi/os"
	otpb "github.com/openconfig/gnoi/otdr"
	plqpb "github.com/openconfig/gnoi/packet_link_qualification"
	spb "github.com/openconfig/gnoi/system"
	pb "github.com/openconfig/gnoi/types"
	wrpb "github.com/openconfig/gnoi/wavelength_router"
)

const (
	// Kill process default
	defaultRestart = true

	// Ping simulation default values
	defaultPingCount    = 5          // Default number of ping packets
	defaultPingInterval = 1000000000 // Default interval between packets (1 second in nanoseconds)
	defaultPingWait     = 2000000000 // Default wait time for response (2 seconds in nanoseconds)
	defaultPingSize     = 56         // Default packet size in bytes (standard ping size)
)

type bgp struct {
	bpb.UnimplementedBGPServer
}

type cert struct {
	cmpb.UnimplementedCertificateManagementServer
}

type diag struct {
	diagpb.UnimplementedDiagServer
}

type factoryReset struct {
	frpb.UnimplementedFactoryResetServer
}

type file struct {
	fpb.UnimplementedFileServer
}

type healthz struct {
	hpb.UnimplementedHealthzServer
}

type layer2 struct {
	lpb.UnimplementedLayer2Server
}

type mpls struct {
	mpb.UnimplementedMPLSServer
}

type os struct {
	ospb.UnimplementedOSServer
}

type otdr struct {
	otpb.UnimplementedOTDRServer
}

type linkQualification struct {
	plqpb.UnimplementedLinkQualificationServer

	c      *ygnmi.Client
	config *configpb.LemmingConfig

	// Multi-port operation support
	mu              sync.RWMutex
	operationTests  map[string][]*QualificationState // op_id -> states
	qualifications  map[string]*QualificationState   // qual_id -> state (for individual lookup)
	interfaceToTest map[string]string                // interface -> current_op_id
	capabilities    *plqpb.CapabilitiesResponse

	// Historical results tracking per interface
	historicalResults map[string][]*plqpb.QualificationResult // interface -> historical results
	maxHistorical     uint64
}

// QualificationState represents the state of a single qualification in an operation
type QualificationState struct {
	ID            string
	OperationID   string // For grouping multi-port operations
	InterfaceName string
	State         plqpb.QualificationState
	StartTime     time.Time
	EndTime       time.Time

	// Packet statistics
	PacketsSent     uint64
	PacketsReceived uint64
	PacketsDropped  uint64
	PacketsError    uint64

	// Configuration
	IsGenerator bool
	IsReflector bool
	Config      *plqpb.QualificationConfiguration

	// Control channels for cancellation
	cancelCh chan struct{}
	done     bool
	mu       sync.Mutex // Protect individual state updates

	// Network impairments simulation
	impairments *fakedevice.NetworkImpairments

	// For generator-reflector coordination
	pairedQualID string // ID of paired qualification (for generator-reflector pairs)
}

func newLinkQualification(c *ygnmi.Client, config *configpb.LemmingConfig) *linkQualification {
	return &linkQualification{
		c:                 c,
		config:            config,
		operationTests:    make(map[string][]*QualificationState),
		qualifications:    make(map[string]*QualificationState),
		interfaceToTest:   make(map[string]string),
		historicalResults: make(map[string][]*plqpb.QualificationResult),
		capabilities:      buildCapabilities(),
		maxHistorical:     10, // Default max historical results
	}
}

// Capabilities returns the capabilities of the LinkQualification service
func (lq *linkQualification) Capabilities(ctx context.Context, req *plqpb.CapabilitiesRequest) (*plqpb.CapabilitiesResponse, error) {
	log.Infof("Received LinkQualification Capabilities request")
	// Create a new capabilities response with current timestamp
	response := &plqpb.CapabilitiesResponse{
		Time:                             timestamppb.Now(),
		NtpSynced:                        lq.capabilities.NtpSynced,
		Generator:                        lq.capabilities.Generator,
		Reflector:                        lq.capabilities.Reflector,
		MaxHistoricalResultsPerInterface: lq.capabilities.MaxHistoricalResultsPerInterface,
	}
	return response, nil
}

// Create starts link qualification on specified interfaces with multi-port support
func (lq *linkQualification) Create(ctx context.Context, req *plqpb.CreateRequest) (*plqpb.CreateResponse, error) {
	log.Infof("Received LinkQualification Create request with %d interfaces", len(req.GetInterfaces()))

	if len(req.GetInterfaces()) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "no interfaces specified")
	}

	lq.mu.Lock()
	defer lq.mu.Unlock()

	// Generate operation ID for this batch request
	operationID := fmt.Sprintf("op_%d_%d", time.Now().UnixNano(), len(req.GetInterfaces()))
	log.Infof("Generated operation ID: %s", operationID)

	// Batch validation - validate ALL interfaces before starting ANY
	validationErrors := make(map[string]*rpc.Status)
	qualificationStates := make([]*QualificationState, 0, len(req.GetInterfaces()))

	// Track IDs and interfaces within this request to detect duplicates
	requestIDs := make(map[string]bool)
	requestInterfaces := make(map[string]bool)

	// Track generators and reflectors for pairing
	generators := make([]*plqpb.QualificationConfiguration, 0)
	reflectors := make([]*plqpb.QualificationConfiguration, 0)

	for _, config := range req.GetInterfaces() {
		if config.GetId() == "" {
			return nil, status.Errorf(codes.InvalidArgument, "qualification id is required")
		}
		if config.GetInterfaceName() == "" {
			return nil, status.Errorf(codes.InvalidArgument, "interface name is required")
		}

		id := config.GetId()
		interfaceName := config.GetInterfaceName()

		// Check for duplicates within this request first
		if requestIDs[id] {
			validationErrors[id] = &rpc.Status{
				Code:    int32(codes.AlreadyExists),
				Message: fmt.Sprintf("duplicate qualification id %s in request", id),
			}
			continue
		}
		if requestInterfaces[interfaceName] {
			validationErrors[id] = &rpc.Status{
				Code:    int32(codes.AlreadyExists),
				Message: fmt.Sprintf("duplicate interface %s in request", interfaceName),
			}
			continue
		}

		// Mark as seen in this request
		requestIDs[id] = true
		requestInterfaces[interfaceName] = true

		// Track generators and reflectors
		switch config.GetEndpointType().(type) {
		case *plqpb.QualificationConfiguration_PacketGenerator, *plqpb.QualificationConfiguration_PacketInjector:
			generators = append(generators, config)
		case *plqpb.QualificationConfiguration_AsicLoopback, *plqpb.QualificationConfiguration_PmdLoopback:
			reflectors = append(reflectors, config)
		}

		// Validate against existing state and configuration
		if err := lq.validateQualificationConfig(config); err != nil {
			if grpcErr, ok := status.FromError(err); ok {
				validationErrors[id] = &rpc.Status{
					Code:    int32(grpcErr.Code()),
					Message: grpcErr.Message(),
				}
			} else {
				validationErrors[id] = &rpc.Status{
					Code:    int32(codes.Internal),
					Message: err.Error(),
				}
			}
		} else {
			// Create qualification state for valid configs
			qualState := lq.createQualificationState(config, operationID)
			qualificationStates = append(qualificationStates, qualState)
		}
	}

	// Set up generator-reflector pairing if applicable
	if len(generators) == 1 && len(reflectors) == 1 && len(validationErrors) == 0 {
		// Simple pairing case: one generator, one reflector
		for i := range qualificationStates {
			if qualificationStates[i].IsGenerator {
				for j := range qualificationStates {
					if qualificationStates[j].IsReflector {
						qualificationStates[i].pairedQualID = qualificationStates[j].ID
						qualificationStates[j].pairedQualID = qualificationStates[i].ID
						log.Infof("Paired generator %s with reflector %s", qualificationStates[i].ID, qualificationStates[j].ID)
						break
					}
				}
			}
		}
	}

	// Build response with both successful qualifications and validation errors
	response := &plqpb.CreateResponse{
		Status: make(map[string]*rpc.Status),
	}

	// Add validation errors to response first
	for id, errorStatus := range validationErrors {
		response.Status[id] = errorStatus
	}

	// Only proceed with creating qualifications if we have valid ones
	if len(qualificationStates) > 0 {
		// Store valid qualifications in operation group
		lq.operationTests[operationID] = qualificationStates

		// Store individual qualification mappings
		for _, qualState := range qualificationStates {
			lq.qualifications[qualState.ID] = qualState
			lq.interfaceToTest[qualState.InterfaceName] = operationID

			// Only add OK status if there's no validation error for this ID
			if _, hasError := validationErrors[qualState.ID]; !hasError {
				response.Status[qualState.ID] = &rpc.Status{
					Code:    int32(codes.OK),
					Message: "qualification created successfully",
				}
			}
		}

		// Start coordinated multi-port qualification
		go lq.executeOperationGroup(ctx, operationID, qualificationStates)
	}

	log.Infof("Created operation %s with %d valid qualifications, %d validation errors",
		operationID, len(qualificationStates), len(validationErrors))

	return response, nil
}

// Get returns the status for the provided qualification ids
func (lq *linkQualification) Get(ctx context.Context, req *plqpb.GetRequest) (*plqpb.GetResponse, error) {
	log.Infof("Received LinkQualification Get request: %v", req.GetIds())

	if len(req.GetIds()) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "no qualification ids specified")
	}

	response := &plqpb.GetResponse{
		Results: make(map[string]*plqpb.QualificationResult),
	}

	lq.mu.RLock()
	defer lq.mu.RUnlock()

	for _, id := range req.GetIds() {
		qualState, exists := lq.qualifications[id]
		if !exists {
			// Return NOT_FOUND result for missing qualifications
			response.Results[id] = &plqpb.QualificationResult{
				Id:    id,
				State: plqpb.QualificationState_QUALIFICATION_STATE_ERROR,
				Status: &rpc.Status{
					Code:    int32(codes.NotFound),
					Message: fmt.Sprintf("qualification %s not found", id),
				},
			}
			continue
		}

		// Convert internal state to protobuf result
		result := &plqpb.QualificationResult{
			Id:            qualState.ID,
			InterfaceName: qualState.InterfaceName,
			State:         qualState.State,
			StartTime:     timestamppb.New(qualState.StartTime),
		}

		// Add end time and packet stats if qualification is completed
		if qualState.done {
			result.EndTime = timestamppb.New(qualState.EndTime)
			result.PacketsSent = qualState.PacketsSent
			result.PacketsReceived = qualState.PacketsReceived
			result.PacketsDropped = qualState.PacketsDropped
			result.PacketsError = qualState.PacketsError

			// Calculate realistic rate based on configuration and impairments
			duration := qualState.EndTime.Sub(qualState.StartTime)
			if duration > 0 && qualState.PacketsSent > 0 {
				// Get packet size from configuration, default to 1500 bytes
				packetSize := uint64(1500) // Default
				if packetGen := qualState.Config.GetPacketGenerator(); packetGen != nil {
					if packetGen.GetPacketSize() > 0 {
						packetSize = uint64(packetGen.GetPacketSize())
					}
				} else if packetInj := qualState.Config.GetPacketInjector(); packetInj != nil {
					if packetInj.GetPacketSize() > 0 {
						packetSize = uint64(packetInj.GetPacketSize())
					}
				}

				// ExpectedRateBytesPerSecond should be the configured theoretical rate
				// (packet_rate * packet_size), not the observed rate
				if packetGen := qualState.Config.GetPacketGenerator(); packetGen != nil {
					configuredRate := uint64(packetGen.GetPacketRate())
					result.ExpectedRateBytesPerSecond = configuredRate * packetSize
				} else if packetInj := qualState.Config.GetPacketInjector(); packetInj != nil {
					// PacketInjector uses packet count, so calculate rate from test duration
					if duration.Seconds() > 0 {
						packetRate := uint64(packetInj.GetPacketCount()) / uint64(duration.Seconds())
						result.ExpectedRateBytesPerSecond = packetRate * packetSize
					}
				} else {
					// For non-generator endpoints, calculate based on sent packets and duration
					totalBytes := qualState.PacketsSent * packetSize
					result.ExpectedRateBytesPerSecond = totalBytes / uint64(duration.Seconds())
				}

				// Account for packet loss in actual rate calculation
				actualBytes := qualState.PacketsReceived * packetSize
				result.QualificationRateBytesPerSecond = actualBytes / uint64(duration.Seconds())

				log.Infof("Calculated rates for qualification %s (op_id=%s): packet_size=%d, expected_rate=%d Bps, actual_rate=%d Bps, loss=%.4f%%",
					qualState.ID, qualState.OperationID, packetSize,
					result.ExpectedRateBytesPerSecond, result.QualificationRateBytesPerSecond,
					float64(qualState.PacketsDropped)/float64(qualState.PacketsSent)*100)
			}
		} else {
			result.EndTime = timestamppb.Now() // Current time for ongoing qualifications
		}

		// Add successful status
		result.Status = &rpc.Status{
			Code:    int32(codes.OK),
			Message: "qualification result retrieved successfully",
		}

		response.Results[id] = result
	}

	return response, nil
}

// Delete removes the qualification results for the provided ids
func (lq *linkQualification) Delete(ctx context.Context, req *plqpb.DeleteRequest) (*plqpb.DeleteResponse, error) {
	log.Infof("Received LinkQualification Delete request: %v", req.GetIds())

	if len(req.GetIds()) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "no qualification ids specified")
	}

	response := &plqpb.DeleteResponse{
		Results: make(map[string]*rpc.Status),
	}

	lq.mu.Lock()
	defer lq.mu.Unlock()

	// Track operations to clean up
	operationsToCleanup := make(map[string]bool)

	for _, id := range req.GetIds() {
		qualState, exists := lq.qualifications[id]
		if !exists {
			response.Results[id] = &rpc.Status{
				Code:    int32(codes.NotFound),
				Message: fmt.Sprintf("qualification %s not found", id),
			}
			continue
		}

		// Cancel ongoing qualification if not completed
		if !qualState.done {
			select {
			case qualState.cancelCh <- struct{}{}:
				log.Infof("Sent cancellation signal for qualification %s", id)
			default:
				// Channel already has a message or qualification completed
			}
			qualState.done = true
			qualState.State = plqpb.QualificationState_QUALIFICATION_STATE_ERROR
			qualState.EndTime = time.Now()

			// Interface state restoration will be handled by fakedevice simulation
		}

		// Store completed qualification in historical results if it completed successfully
		if qualState.State == plqpb.QualificationState_QUALIFICATION_STATE_COMPLETED {
			interfaceName := qualState.InterfaceName

			// Create qualification result for historical storage
			result := &plqpb.QualificationResult{
				Id:              qualState.ID,
				InterfaceName:   interfaceName,
				State:           qualState.State,
				StartTime:       timestamppb.New(qualState.StartTime),
				EndTime:         timestamppb.New(qualState.EndTime),
				PacketsSent:     qualState.PacketsSent,
				PacketsReceived: qualState.PacketsReceived,
				PacketsDropped:  qualState.PacketsDropped,
				PacketsError:    qualState.PacketsError,
			}

			// Calculate rates if qualification completed with data
			if qualState.PacketsSent > 0 {
				duration := qualState.EndTime.Sub(qualState.StartTime)
				if duration > 0 {
					packetSize := uint64(1500) // Default
					if packetGen := qualState.Config.GetPacketGenerator(); packetGen != nil && packetGen.GetPacketSize() > 0 {
						packetSize = uint64(packetGen.GetPacketSize())
					}

					// Calculate expected and actual rates
					if packetGen := qualState.Config.GetPacketGenerator(); packetGen != nil {
						result.ExpectedRateBytesPerSecond = packetGen.GetPacketRate() * packetSize
					}
					result.QualificationRateBytesPerSecond = (qualState.PacketsReceived * packetSize) / uint64(duration.Seconds())
				}
			}

			// Add to historical results, maintaining maximum limit
			history := lq.historicalResults[interfaceName]
			history = append(history, result)

			// Keep only the most recent results up to maxHistorical
			if uint64(len(history)) > lq.maxHistorical {
				// Remove oldest results
				history = history[uint64(len(history))-lq.maxHistorical:]
			}
			lq.historicalResults[interfaceName] = history

			log.Infof("Stored qualification %s in historical results for interface %s (now %d historical results)",
				id, interfaceName, len(history))
		}

		// Track operation for potential cleanup
		operationsToCleanup[qualState.OperationID] = true

		// Remove from individual qualification mapping
		delete(lq.qualifications, id)
		delete(lq.interfaceToTest, qualState.InterfaceName)

		response.Results[id] = &rpc.Status{
			Code:    int32(codes.OK),
			Message: "qualification deleted successfully",
		}

		log.Infof("Deleted qualification %s (op_id=%s) for interface %s", id, qualState.OperationID, qualState.InterfaceName)
	}

	// Clean up empty operations
	for operationID := range operationsToCleanup {
		if qualStates, exists := lq.operationTests[operationID]; exists {
			// Check if all qualifications in this operation are deleted
			allDeleted := true
			for _, qual := range qualStates {
				if _, stillExists := lq.qualifications[qual.ID]; stillExists {
					allDeleted = false
					break
				}
			}

			if allDeleted {
				delete(lq.operationTests, operationID)
				log.Infof("Cleaned up operation %s - all qualifications deleted", operationID)
			}
		}
	}

	return response, nil
}

// List qualifications currently on the target
func (lq *linkQualification) List(ctx context.Context, req *plqpb.ListRequest) (*plqpb.ListResponse, error) {
	log.Infof("Received LinkQualification List request")

	lq.mu.RLock()
	defer lq.mu.RUnlock()

	var results []*plqpb.ListResult

	// Group by operation for better overview
	operationSummary := make(map[string]int)

	for _, qualState := range lq.qualifications {
		result := &plqpb.ListResult{
			Id:            qualState.ID,
			State:         qualState.State,
			InterfaceName: qualState.InterfaceName,
		}
		results = append(results, result)

		// Track operation summary
		operationSummary[qualState.OperationID]++
	}

	// Log operation summary for debugging
	log.Infof("List results: %d total qualifications across %d operations",
		len(results), len(operationSummary))
	for opID, count := range operationSummary {
		log.Infof("  Operation %s: %d qualifications", opID, count)
	}

	return &plqpb.ListResponse{
		Results: results,
	}, nil
}

// buildCapabilities creates the static capabilities response
func buildCapabilities() *plqpb.CapabilitiesResponse {
	return &plqpb.CapabilitiesResponse{
		Time:      timestamppb.Now(),
		NtpSynced: true,
		Generator: &plqpb.GeneratorCapabilities{
			PacketGenerator: &plqpb.PacketGeneratorCapabilities{
				MaxBps:              400000000000, // 400 Gbps
				MaxPps:              500000000,    // 500M PPS
				MinMtu:              64,
				MaxMtu:              9000,
				MinSetupDuration:    durationpb.New(1 * time.Second),
				MinTeardownDuration: durationpb.New(1 * time.Second),
				MinSampleInterval:   durationpb.New(1 * time.Second),
			},
			PacketInjector: &plqpb.PacketInjectorCapabilities{
				MinMtu:              64,
				MaxMtu:              9000,
				MinInjectedPackets:  1,
				MaxInjectedPackets:  1000000000, // 1B packets
				MinSetupDuration:    durationpb.New(1 * time.Second),
				MinTeardownDuration: durationpb.New(1 * time.Second),
				MinSampleInterval:   durationpb.New(1 * time.Second),
			},
		},
		Reflector: &plqpb.ReflectorCapabilities{
			AsicLoopback: &plqpb.AsicLoopbackCapabilities{
				MinSetupDuration:    durationpb.New(1 * time.Second),
				MinTeardownDuration: durationpb.New(1 * time.Second),
				Fields:              []plqpb.HeaderMatchField{plqpb.HeaderMatchField_HEADER_MATCH_FIELD_L2},
			},
			PmdLoopback: &plqpb.PmdLoopbackCapabilities{
				MinSetupDuration:    durationpb.New(1 * time.Second),
				MinTeardownDuration: durationpb.New(1 * time.Second),
			},
		},
		MaxHistoricalResultsPerInterface: 10,
	}
}

// validateQualificationConfig validates a single qualification configuration
func (lq *linkQualification) validateQualificationConfig(config *plqpb.QualificationConfiguration) error {
	id := config.GetId()
	interfaceName := config.GetInterfaceName()

	// Check for duplicate ID across all operations
	if _, exists := lq.qualifications[id]; exists {
		return status.Errorf(codes.AlreadyExists, "qualification id already exists")
	}

	// Check if interface is already in use by another operation
	if existingOpID, inUse := lq.interfaceToTest[interfaceName]; inUse {
		return status.Errorf(codes.AlreadyExists, "interface %s already in use by operation %s", interfaceName, existingOpID)
	}

	// Validate endpoint type is specified
	if config.GetEndpointType() == nil {
		return status.Errorf(codes.InvalidArgument, "endpoint type is required")
	}

	// Validate timing configuration is present
	if config.GetTiming() == nil {
		return status.Errorf(codes.InvalidArgument, "timing configuration is required")
	}

	// Validate timing configuration content
	timing := config.GetTiming()

	switch t := timing.(type) {
	case *plqpb.QualificationConfiguration_Rpc:
		rpcTiming := t.Rpc
		if rpcTiming.GetDuration() == nil {
			return status.Errorf(codes.InvalidArgument, "test duration is required")
		}
		duration := rpcTiming.GetDuration().AsDuration()
		if duration <= 0 {
			return status.Errorf(codes.InvalidArgument, "test duration must be positive")
		}
	case *plqpb.QualificationConfiguration_Ntp:
		ntpTiming := t.Ntp
		if ntpTiming.GetStartTime() == nil || ntpTiming.GetEndTime() == nil {
			return status.Errorf(codes.InvalidArgument, "NTP start and end times are required")
		}
		startTime := ntpTiming.GetStartTime().AsTime()
		endTime := ntpTiming.GetEndTime().AsTime()
		if !endTime.After(startTime) {
			return status.Errorf(codes.InvalidArgument, "NTP end time must be after start time")
		}
	default:
		return status.Errorf(codes.InvalidArgument, "unknown timing configuration type")
	}

	// Validate endpoint type configuration
	switch et := config.GetEndpointType().(type) {
	case *plqpb.QualificationConfiguration_PacketGenerator:
		pg := et.PacketGenerator
		if pg.GetPacketRate() == 0 {
			return status.Errorf(codes.InvalidArgument, "packet rate must be greater than 0")
		}
		// Apply proto-specified default for packet size
		packetSize := pg.GetPacketSize()
		if packetSize == 0 {
			packetSize = 1500
		}
	case *plqpb.QualificationConfiguration_PacketInjector:
		pi := et.PacketInjector
		if pi.GetPacketCount() == 0 {
			return status.Errorf(codes.InvalidArgument, "packet count must be greater than 0")
		}
		// Validate loopback mode for packet injector (ensure one of the oneof options is set)
		if pi.GetPmdLoopback() == nil && pi.GetAsicLoopback() == nil {
			return status.Errorf(codes.InvalidArgument, "packet injector loopback mode must be specified")
		}
	case *plqpb.QualificationConfiguration_AsicLoopback:
		// ASIC loopback is valid with any non-nil configuration
	case *plqpb.QualificationConfiguration_PmdLoopback:
		// PMD loopback is valid with any non-nil configuration
	default:
		return status.Errorf(codes.InvalidArgument, "unknown endpoint type")
	}

	return nil
}

// createQualificationState creates a qualification state from config
func (lq *linkQualification) createQualificationState(config *plqpb.QualificationConfiguration, operationID string) *QualificationState {
	// Determine endpoint type and impairments
	isGenerator := config.GetPacketGenerator() != nil || config.GetPacketInjector() != nil
	isReflector := config.GetAsicLoopback() != nil || config.GetPmdLoopback() != nil

	// Configure network impairments based on interface type and test parameters
	impairments := lq.calculateNetworkImpairments(config)

	state := &QualificationState{
		ID:            config.GetId(),
		OperationID:   operationID,
		InterfaceName: config.GetInterfaceName(),
		State:         plqpb.QualificationState_QUALIFICATION_STATE_IDLE,
		Config:        config,
		StartTime:     time.Now(),
		IsGenerator:   isGenerator,
		IsReflector:   isReflector,
		impairments:   impairments,
		cancelCh:      make(chan struct{}, 1),
		done:          false,
	}

	log.Infof("Created qualification state: id=%s, op_id=%s, interface=%s, generator=%v, reflector=%v",
		state.ID, state.OperationID, state.InterfaceName, state.IsGenerator, state.IsReflector)

	return state
}

// calculateNetworkImpairments determines network conditions based on configuration
func (lq *linkQualification) calculateNetworkImpairments(config *plqpb.QualificationConfiguration) *fakedevice.NetworkImpairments {
	// Phase 1: Default to near-perfect conditions for OpenConfig compliance
	// Phase 2: Support configurable impairments through lemming config

	impairments := &fakedevice.NetworkImpairments{
		PacketLossRate: 0.0,                   // Perfect link with zero loss
		CorruptionRate: 0.0,                   // Perfect link with zero corruption
		JitterRange:    10 * time.Microsecond, // Minimal ±10μs jitter
		LatencyBase:    1 * time.Microsecond,  // 1μs base latency
	}

	// Check if the lemming config has network simulation parameters
	if lq.config != nil && lq.config.GetNetworkSim() != nil {
		netSim := lq.config.GetNetworkSim()

		// Apply configured packet loss rate if specified
		if netSim.PacketLossRate > 0 {
			impairments.PacketLossRate = float64(netSim.PacketLossRate)
			log.Infof("Applying configured packet loss rate: %.4f%%", impairments.PacketLossRate*100)
		}

		// Apply configured latency parameters
		if netSim.BaseLatencyMs > 0 {
			impairments.LatencyBase = time.Duration(netSim.BaseLatencyMs) * time.Millisecond
		}
		if netSim.LatencyJitterMs > 0 {
			impairments.JitterRange = time.Duration(netSim.LatencyJitterMs) * time.Millisecond
		}

		// Future: Support for corruption rate
		// This would require extending the NetworkSim config
	}

	// For specific testing scenarios, allow interface-specific overrides
	// This could be extended in Phase 2 to support per-interface configuration
	interfaceName := config.GetInterfaceName()
	if strings.Contains(interfaceName, "lossy") {
		// Special test interface with higher loss
		impairments.PacketLossRate = 0.01 // 1% loss
		log.Infof("Applied test configuration for lossy interface %s", interfaceName)
	} else if strings.Contains(interfaceName, "latency") {
		// Special test interface with higher latency
		impairments.LatencyBase = 10 * time.Millisecond
		impairments.JitterRange = 2 * time.Millisecond
		log.Infof("Applied test configuration for high-latency interface %s", interfaceName)
	}

	log.Infof("Calculated impairments for %s: loss=%.4f%%, corruption=%.7f%%, jitter=±%v, latency=%v",
		config.GetInterfaceName(),
		impairments.PacketLossRate*100,
		impairments.CorruptionRate*100,
		impairments.JitterRange,
		impairments.LatencyBase)

	return impairments
}

// executeOperationGroup coordinates multi-port qualification execution by calling fakedevice simulations
func (lq *linkQualification) executeOperationGroup(ctx context.Context, operationID string, qualifications []*QualificationState) {
	log.Infof("Starting operation group execution: %s with %d qualifications", operationID, len(qualifications))

	// Start all qualifications in the operation group
	for _, qual := range qualifications {
		go lq.executeQualification(ctx, qual)
	}

	log.Infof("Started all qualifications in operation %s", operationID)
}

// markQualificationError marks a qualification as failed
func (lq *linkQualification) markQualificationError(qual *QualificationState, errorMsg string) {
	lq.mu.Lock()
	defer lq.mu.Unlock()

	qual.State = plqpb.QualificationState_QUALIFICATION_STATE_ERROR
	qual.done = true
	qual.EndTime = time.Now()

	log.Errorf("Qualification %s failed: %s", qual.ID, errorMsg)
}

// executeQualification runs a single qualification by calling fakedevice simulation
func (lq *linkQualification) executeQualification(ctx context.Context, qual *QualificationState) {
	log.Infof("Starting qualification execution: %s for interface %s (generator=%v, reflector=%v)",
		qual.ID, qual.InterfaceName, qual.IsGenerator, qual.IsReflector)

	// Create update channel for simulation
	updateChan := make(chan *fakedevice.LinkQualificationResult, 10)

	// Create a context that can be cancelled
	qualCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Use simulation with impairments support
	go func() {
		defer close(updateChan)
		if err := fakedevice.RunPacketLinkQualification(qualCtx, lq.c, qual.Config, updateChan, lq.config, qual.impairments); err != nil {
			log.Errorf("Link qualification simulation failed for %s: %v", qual.ID, err)
			lq.markQualificationError(qual, fmt.Sprintf("simulation failed: %v", err))
		}
	}()

	// Process simulation updates
	for {
		select {
		case <-qual.cancelCh:
			log.Infof("Qualification %s cancelled", qual.ID)
			cancel() // Cancel the simulation context
			return
		case update, ok := <-updateChan:
			if !ok {
				// Channel closed, simulation finished
				log.Infof("Qualification %s simulation completed", qual.ID)

				// Handle generator-reflector coordination
				if qual.pairedQualID != "" && qual.IsGenerator {
					lq.coordinateWithPairedQualification(qual)
				}
				return
			}

			// Update internal state atomically
			qual.mu.Lock()
			if !qual.done {
				qual.State = update.State
				qual.PacketsSent = update.PacketsSent
				qual.PacketsReceived = update.PacketsReceived
				qual.PacketsDropped = update.PacketsDropped
				qual.PacketsError = update.PacketsError
				qual.StartTime = update.StartTime
				if !update.EndTime.IsZero() {
					qual.EndTime = update.EndTime
				}

				// Mark as done if completed or error
				if update.State == plqpb.QualificationState_QUALIFICATION_STATE_COMPLETED ||
					update.State == plqpb.QualificationState_QUALIFICATION_STATE_ERROR {
					qual.done = true
				}
			}
			qual.mu.Unlock()

			// Log state transitions
			if update.State != plqpb.QualificationState_QUALIFICATION_STATE_UNSPECIFIED {
				log.Infof("Qualification %s transitioned to state %v", qual.ID, update.State)
			}
		}
	}
}

// coordinateWithPairedQualification updates paired reflector statistics based on generator results
func (lq *linkQualification) coordinateWithPairedQualification(generatorQual *QualificationState) {
	if generatorQual.pairedQualID == "" {
		return
	}

	lq.mu.RLock()
	reflectorQual, exists := lq.qualifications[generatorQual.pairedQualID]
	lq.mu.RUnlock()

	if !exists {
		log.Warningf("Paired qualification %s not found for generator %s", generatorQual.pairedQualID, generatorQual.ID)
		return
	}

	// Update reflector statistics based on generator
	// In a real scenario, reflector receives what generator sends (minus losses)
	reflectorQual.mu.Lock()
	defer reflectorQual.mu.Unlock()

	if reflectorQual.IsReflector && !reflectorQual.done {
		// Reflector receives what generator sent (minus network losses)
		reflectorQual.PacketsReceived = generatorQual.PacketsSent - generatorQual.PacketsDropped
		// Reflector sends back what it received
		reflectorQual.PacketsSent = reflectorQual.PacketsReceived
		// Reflector has same error count as generator
		reflectorQual.PacketsError = generatorQual.PacketsError

		log.Infof("Coordinated reflector %s stats with generator %s: sent=%d, received=%d",
			reflectorQual.ID, generatorQual.ID, reflectorQual.PacketsSent, reflectorQual.PacketsReceived)
	}
}

type system struct {
	spb.UnimplementedSystemServer

	c      *ygnmi.Client
	config *configpb.LemmingConfig

	// rebootMu has the following roles:
	// * ensures that writes to hasPendingReboot are free from race
	//   conditions
	// * ensures consistency between reboot operations and the current
	//   state of hasPendingReboot (i.e. prevent TOCCTOU race conditions).
	rebootMu         sync.Mutex
	hasPendingReboot bool
	// These channels ensure that cancellation is a blocking operation to
	// avoid future reboots from conflicting with cancelled pending
	// reboots.
	cancelReboot       chan struct{}
	cancelRebootFinish chan struct{}
	// componentRebootsMu protects the componentReboots map
	// Map to track pending component reboots by component name
	componentRebootsMu sync.Mutex
	componentReboots   map[string]chan struct{}
	// switchoverMu protects switchover operations and ensures
	// only one switchover can be in progress at a time
	switchoverMu         sync.Mutex
	hasPendingSwitchover bool
	// processMu protects process operations and ensures
	// only one process operation can be in progress at a time
	processMu sync.Mutex
}

func newSystem(c *ygnmi.Client, config *configpb.LemmingConfig) *system {
	return &system{
		c:                  c,
		config:             config,
		cancelReboot:       make(chan struct{}, 1),
		cancelRebootFinish: make(chan struct{}),
		componentReboots:   make(map[string]chan struct{}),
	}
}

func (*system) Time(context.Context, *spb.TimeRequest) (*spb.TimeResponse, error) {
	return &spb.TimeResponse{Time: uint64(time.Now().UnixNano())}, nil
}

func (s *system) Reboot(ctx context.Context, r *spb.RebootRequest) (*spb.RebootResponse, error) {
	log.Infof("Received reboot request: %v", r)
	if r.Method == spb.RebootMethod_POWERUP {
		return &spb.RebootResponse{}, nil
	}

	// If subcomponents are specified, handle component-specific reboot
	if len(r.GetSubcomponents()) > 0 {
		return s.handleComponentReboot(ctx, r)
	}

	// Otherwise handle system-wide reboot
	if err := s.handleSystemReboot(ctx, r); err != nil {
		return nil, err
	}

	log.Infof("successful reboot with delay %v, type %v, and force %v", r.GetDelay(), r.GetMethod(), r.GetForce())
	return &spb.RebootResponse{}, nil
}

// handleComponentReboot processes a reboot request for specific components
func (s *system) handleComponentReboot(ctx context.Context, r *spb.RebootRequest) (*spb.RebootResponse, error) {
	// Check if there's a system-wide reboot pending, which would block all component reboots
	s.rebootMu.Lock()
	systemRebootPending := s.hasPendingReboot
	s.rebootMu.Unlock()

	if systemRebootPending {
		return nil, status.Errorf(codes.FailedPrecondition, "system-wide reboot already pending, cannot reboot components")
	}

	// Process each subcomponent
	for _, subcompPath := range r.GetSubcomponents() {
		componentName, err := extractComponentNameFromPath(subcompPath)
		if err != nil {
			return nil, err
		}

		// Check if this is an active supervisor
		isActive, err := s.isActiveSupervisor(ctx, componentName)
		if err != nil {
			log.Warningf("Failed to determine supervisor role for %s: %v", componentName, err)
		} else if isActive {
			// reject active control card reboot to enforce standby-only policy
			return nil, status.Errorf(codes.FailedPrecondition, "rebooting active supervisor %s is not allowed, use standby or chassis reboot instead", componentName)
		}

		// Check if the component exists by querying it
		componentPath := ocpath.Root().Component(componentName)
		_, err = ygnmi.Get(ctx, s.c, componentPath.State())
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "component %q not found: %v", componentName, err)
		}

		delay := r.GetDelay()
		if delay == 0 {
			s.componentRebootsMu.Lock()
			if _, exists := s.componentReboots[componentName]; exists {
				s.componentRebootsMu.Unlock()
				return nil, status.Errorf(codes.AlreadyExists, "reboot already pending for component %q", componentName)
			}
			s.componentRebootsMu.Unlock()
			// Immediate reboot
			if err := fakedevice.RebootComponent(context.Background(), s.c, componentName, time.Now().UnixNano(), s.config); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to reboot component %q: %v", componentName, err)
			}
			log.Infof("Component %q immediate reboot completed", componentName)
			continue
		}
		// Check if there's already a pending reboot for this component
		s.componentRebootsMu.Lock()
		if _, exists := s.componentReboots[componentName]; exists {
			s.componentRebootsMu.Unlock()
			return nil, status.Errorf(codes.AlreadyExists, "reboot already pending for component %q", componentName)
		}

		// Create a cancellation channel for this component
		cancelCh := make(chan struct{}, 1)
		rebootCtx, cancel := context.WithCancel(context.Background())
		s.componentReboots[componentName] = cancelCh
		s.componentRebootsMu.Unlock()

		// Cleanup function for consistent cleanup
		cleanup := func() {
			cancel()
			s.componentRebootsMu.Lock()
			delete(s.componentReboots, componentName)
			s.componentRebootsMu.Unlock()
		}

		// Handle delayed reboot
		go func(compName string) {
			defer cleanup()
			select {
			case <-cancelCh:
				log.Infof("delayed component reboot for %q cancelled", compName)
			case <-rebootCtx.Done():
				log.Infof("delayed component reboot for %q cancelled due to context", compName)
			case <-time.After(time.Duration(delay) * time.Nanosecond):
				now := time.Now().UnixNano()
				if err := fakedevice.RebootComponent(rebootCtx, s.c, compName, now, s.config); err != nil {
					log.Errorf("delayed component reboot for %q failed: %v", compName, err)
					return
				}
				log.Infof("Component %q delayed reboot completed", compName)
			}
		}(componentName)

		log.Infof("scheduled component reboot for %q with delay %v", componentName, r.GetDelay())
	}

	return &spb.RebootResponse{}, nil
}

// handleSystemReboot processes a reboot request for chassis
func (s *system) handleSystemReboot(ctx context.Context, r *spb.RebootRequest) error {
	s.rebootMu.Lock()
	defer s.rebootMu.Unlock()
	if s.hasPendingReboot {
		return status.Errorf(codes.AlreadyExists, "reboot already pending")
	}

	delay := r.GetDelay()
	if delay == 0 {
		now := time.Now().UnixNano()
		if err := fakedevice.Reboot(ctx, s.c, now); err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		return nil
	}

	s.hasPendingReboot = true
	go func() { // wait the delay time for reboot
		select {
		case <-s.cancelReboot:
			log.Infof("delayed reboot cancelled")
			s.cancelRebootFinish <- struct{}{}
		case <-time.After(time.Duration(delay) * time.Nanosecond):
			now := time.Now().UnixNano()
			if err := fakedevice.Reboot(ctx, s.c, now); err != nil {
				log.Errorf("delayed reboot failed: %v", err)
			}
			s.rebootMu.Lock()
			defer s.rebootMu.Unlock()
			s.hasPendingReboot = false
		}
	}()
	return nil
}

func (s *system) CancelReboot(ctx context.Context, c *spb.CancelRebootRequest) (*spb.CancelRebootResponse, error) {
	log.Infof("Received cancel reboot request %v", c)

	// Check if there are any component reboots to cancel
	s.componentRebootsMu.Lock()
	componentRebootsPending := len(s.componentReboots)
	for component, cancelCh := range s.componentReboots {
		select {
		case cancelCh <- struct{}{}:
			log.Infof("Sent cancellation signal for component %q reboot", component)
		default:
			// Channel already has a message or is closed
			log.Infof("Component %q reboot already completed or in progress, couldn't cancel", component)
		}
		delete(s.componentReboots, component)
	}
	s.componentRebootsMu.Unlock()

	// Check for system-wide reboot to cancel
	s.rebootMu.Lock()
	hasPendingReboot := s.hasPendingReboot
	s.rebootMu.Unlock()

	if !hasPendingReboot && componentRebootsPending == 0 {
		// No reboots of any kind to cancel
		return &spb.CancelRebootResponse{}, nil
	}

	if hasPendingReboot {
		s.cancelReboot <- struct{}{} // signal cancellation
		for {
			select {
			case <-s.cancelRebootFinish:
				s.rebootMu.Lock()
				defer s.rebootMu.Unlock()
				s.hasPendingReboot = false
				return &spb.CancelRebootResponse{}, nil
			case <-time.After(time.Second): // It's possible for reboot to happen after cancellation signal -- use polling to check that.
				s.rebootMu.Lock()
				if !s.hasPendingReboot {
					s.rebootMu.Unlock()
					<-s.cancelReboot // clean-up cancellation signal that's not needed since reboot actually happened.
					return &spb.CancelRebootResponse{}, nil
				}
				s.rebootMu.Unlock()
			}
		}
	}

	return &spb.CancelRebootResponse{}, nil
}

// SwitchControlProcessor performs supervisor switchover from the current active supervisor to the specified target supervisor
func (s *system) SwitchControlProcessor(ctx context.Context, r *spb.SwitchControlProcessorRequest) (*spb.SwitchControlProcessorResponse, error) {
	log.Infof("Received SwitchControlProcessor request: %v", r)

	if r.GetControlProcessor() == nil {
		return nil, status.Errorf(codes.InvalidArgument, "control_processor path is required")
	}

	targetSupervisor, err := extractComponentNameFromPath(r.GetControlProcessor())
	if err != nil {
		return nil, err
	}

	// Protect against concurrent switchover operations
	s.switchoverMu.Lock()
	defer s.switchoverMu.Unlock()

	if s.hasPendingSwitchover {
		return nil, status.Errorf(codes.FailedPrecondition, "supervisor switchover already in progress")
	}

	// Check if there are any pending reboot operations (system or component level)
	s.rebootMu.Lock()
	systemRebootPending := s.hasPendingReboot
	s.rebootMu.Unlock()

	s.componentRebootsMu.Lock()
	componentRebootsPending := len(s.componentReboots)
	s.componentRebootsMu.Unlock()

	if componentRebootsPending > 0 || systemRebootPending {
		return nil, status.Errorf(codes.FailedPrecondition, "reboot operations pending, cannot perform switchover")
	}

	// Validate supervisor state and get active/standby supervisors
	activeSupervisor, standbySupervisor, err := s.getSupervisorRole(ctx)
	if err != nil {
		return nil, err
	}

	if targetSupervisor != activeSupervisor && targetSupervisor != standbySupervisor {
		return nil, status.Errorf(codes.NotFound, "target supervisor %q does not exist", targetSupervisor)
	}

	// Check if target is already the active supervisor (no-op case)
	if targetSupervisor == activeSupervisor {
		log.Infof("Target supervisor %q is already active, returning current state (no-op)", targetSupervisor)

		componentPath := ocpath.Root().Component(targetSupervisor)
		component, err := ygnmi.Get(ctx, s.c, componentPath.State())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get current active supervisor info: %v", err)
		}

		// Return successful response for no-op case
		return &spb.SwitchControlProcessorResponse{
			ControlProcessor: r.GetControlProcessor(),
			Version:          component.GetSoftwareVersion(),
			Uptime:           0,
		}, nil
	}

	s.hasPendingSwitchover = true

	// Get the target supervisor info for response
	componentPath := ocpath.Root().Component(targetSupervisor)
	component, err := ygnmi.Get(ctx, s.c, componentPath.State())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get target supervisor info: %v", err)
	}

	response := &spb.SwitchControlProcessorResponse{
		ControlProcessor: r.GetControlProcessor(),
		Version:          component.GetSoftwareVersion(),
		Uptime:           0,
	}

	log.Infof("Scheduled supervisor switcover from %s to %s", activeSupervisor, targetSupervisor)
	go func() {
		backgroundctx := context.Background()

		defer func() {
			s.switchoverMu.Lock()
			s.hasPendingSwitchover = false
			s.switchoverMu.Unlock()
		}()

		// Small delay to make sure response is sent
		time.Sleep(100 * time.Millisecond)

		switchoverTime := time.Now().UnixNano()
		err := fakedevice.SwitchoverSupervisor(backgroundctx, s.c, targetSupervisor, activeSupervisor, switchoverTime, s.config)
		if err != nil {
			log.Errorf("Background supervisor switchover failed: %v", err)
		}
	}()

	return response, nil
}

// KillProcess simulates process termination and restart functionality
func (s *system) KillProcess(ctx context.Context, r *spb.KillProcessRequest) (*spb.KillProcessResponse, error) {
	log.Infof("Received kill process request: %v", r)

	if r.GetPid() == 0 && r.GetName() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "either pid or name must be specified")
	}

	signal := r.GetSignal()
	if signal == spb.KillProcessRequest_SIGNAL_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "signal must be specified")
	}

	targetPID, processName, err := s.resolvePIDAndName(ctx, r.GetPid(), r.GetName())
	if err != nil {
		return nil, err
	}

	// HUP is for reload, restart should be false by default
	restart := defaultRestart
	if signal == spb.KillProcessRequest_SIGNAL_HUP {
		restart = false
	}

	// Protect against concurrent process operations
	s.processMu.Lock()
	defer s.processMu.Unlock()

	if err := fakedevice.KillProcess(context.Background(), s.c, targetPID, processName, signal, restart, s.config); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to kill process: %v", err)
	}

	return &spb.KillProcessResponse{}, nil
}

// Ping simulates ICMP ping operations with configurable network conditions
func (s *system) Ping(r *spb.PingRequest, stream spb.System_PingServer) error {
	log.Infof("Received ping request: %v", r)

	if r.GetDestination() == "" {
		return status.Errorf(codes.InvalidArgument, "destination address is required")
	}

	ctx := stream.Context()
	destination := r.GetDestination()

	count := r.GetCount()
	if count == 0 {
		count = defaultPingCount
	} else if count < -1 {
		return status.Errorf(codes.InvalidArgument, "count must be >= -1, got %d", count)
	}

	interval := r.GetInterval()
	switch {
	case interval == 0:
		interval = defaultPingInterval
	case interval == -1:
		// Flood ping - 1ms minimum interval for safety
		interval = 1000000
	case interval < -1:
		return status.Errorf(codes.InvalidArgument, "interval must be >= -1, got %d", interval)
	}

	wait := r.GetWait()
	if wait == 0 {
		wait = defaultPingWait
	} else if wait < 0 {
		return status.Errorf(codes.InvalidArgument, "wait must be >= 0, got %d", wait)
	}

	size := r.GetSize()
	if size == 0 {
		size = defaultPingSize
	} else if size < 8 || size > 65507 {
		return status.Errorf(codes.InvalidArgument, "packet size must be between 8 and 65507 bytes, got %d", size)
	}

	// TODO: Add support for do_not_fragment, do_not_resolve, l3protocol, network_instance parameters

	responseChan := make(chan *fakedevice.PingPacketResult, 100)
	errorChan := make(chan error, 1)

	go func() {
		defer close(responseChan)
		if err := fakedevice.PingSimulation(ctx, destination, count, time.Duration(interval), time.Duration(wait), uint32(size), responseChan, s.config); err != nil {
			log.Errorf("Ping simulation error: %v", err)
			select {
			case errorChan <- err:
			default:
			}
		}
		close(errorChan)
	}()

	startTime := time.Now()
	var totalSent, totalReceived int32
	var minTime, maxTime, totalTime time.Duration
	var rtts []time.Duration
	firstPacket := true

	// Process results and stream responses
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errorChan:
			if err != nil {
				return status.Errorf(codes.Internal, "ping simulation failed: %v", err)
			}
		case result, ok := <-responseChan:
			if !ok {
				// Channel closed - check for any remaining errors
				select {
				case err := <-errorChan:
					if err != nil {
						return status.Errorf(codes.Internal, "ping simulation failed: %v", err)
					}
				default:
				}

				// Send summary and finish
				summary := &spb.PingResponse{
					Source:   destination,
					Time:     time.Since(startTime).Nanoseconds(),
					Sent:     totalSent,
					Received: totalReceived,
				}
				if totalReceived > 0 {
					summary.MinTime = minTime.Nanoseconds()
					summary.AvgTime = (totalTime / time.Duration(totalReceived)).Nanoseconds()
					summary.MaxTime = maxTime.Nanoseconds()

					// Calculate standard deviation from collected RTTs
					if totalReceived < 2 {
						summary.StdDev = 0
					} else {
						var sumSquaredDiff float64
						avgTime := float64(summary.AvgTime)
						for _, rtt := range rtts {
							diff := float64(rtt.Nanoseconds()) - avgTime
							sumSquaredDiff += diff * diff
						}
						variance := sumSquaredDiff / float64(totalReceived-1)
						summary.StdDev = int64(math.Round(math.Sqrt(variance)))
					}
				}
				return stream.Send(summary)
			}

			// Update stats
			totalSent++
			if result.Success {
				totalReceived++
				totalTime += result.RTT
				if firstPacket {
					minTime = result.RTT
					maxTime = result.RTT
					firstPacket = false
				} else {
					if result.RTT < minTime {
						minTime = result.RTT
					}
					if result.RTT > maxTime {
						maxTime = result.RTT
					}
				}
				rtts = append(rtts, result.RTT)
			}

			// Send individual response
			response := &spb.PingResponse{
				Source:   destination,
				Time:     result.RTT.Nanoseconds(),
				Bytes:    int32(result.Bytes),
				Sequence: result.Sequence,
				Ttl:      result.TTL,
			}

			if err := stream.Send(response); err != nil {
				log.Errorf("Failed to send ping response: %v", err)
				return err
			}
		}
	}
}

// resolvePIDAndName resolves either PID or name to a validated PID and process name that exists in the system
func (s *system) resolvePIDAndName(ctx context.Context, pid uint32, name string) (uint32, string, error) {
	var targetPID uint32
	var targetProcessName string

	if pid != 0 {
		// PID provided - get process info to extract name
		process, err := ygnmi.Get(ctx, s.c, ocpath.Root().System().Process(uint64(pid)).State())
		if err != nil {
			return 0, "", status.Errorf(codes.NotFound, "PID %d not found in process path: %v", pid, err)
		}
		targetPID = pid
		targetProcessName = process.GetName()
	} else {
		// Name provided - look up PID and process name from process monitoring system
		processes, err := ygnmi.GetAll(ctx, s.c, ocpath.Root().System().ProcessAny().State())
		if err != nil {
			return 0, "", status.Errorf(codes.Internal, "failed to query processes: %v", err)
		}
		var foundPID uint64
		var found bool
		for _, process := range processes {
			if process.Name != nil && *process.Name == name {
				if process.Pid != nil {
					foundPID = *process.Pid
					found = true
					break
				}
			}
		}
		if !found {
			return 0, "", status.Errorf(codes.NotFound, "process %q not found", name)
		}
		targetPID = uint32(foundPID)
		targetProcessName = name
	}

	return targetPID, targetProcessName, nil
}

// extractComponentNameFromPath extracts the component name from the gNMI path
func extractComponentNameFromPath(path *pb.Path) (string, error) {
	elems := path.GetElem()
	// Handle Arista format
	if len(elems) == 1 {
		componentName := elems[0].GetName()
		if componentName == "" {
			return "", status.Errorf(codes.InvalidArgument, "Invalid component path, element name is empty, got: %v", path)
		}
		return componentName, nil
	}
	if len(elems) == 2 &&
		elems[0].GetName() == "components" &&
		elems[1].GetName() == "component" &&
		elems[1].GetKey()["name"] != "" {
		return elems[1].GetKey()["name"], nil
	}
	return "", status.Errorf(codes.InvalidArgument,
		"invalid component path, expected either single element or OpenConfig format (/componets/component[name=...]), got: %v", path)
}

// isActiveSupervisor checks for the redundant role of a supervisor
func (s *system) isActiveSupervisor(ctx context.Context, componentName string) (bool, error) {
	componentPath := ocpath.Root().Component(componentName)
	roleVal, err := ygnmi.Get(ctx, s.c, componentPath.RedundantRole().State())
	if err != nil {
		return false, err
	}
	return roleVal == oc.PlatformTypes_ComponentRedundantRole_PRIMARY, nil
}

// getSupervisorRole validates and returns the active and standby supervisors
func (s *system) getSupervisorRole(ctx context.Context) (activeSupervisor, standbySupervisor string, err error) {
	supervisor1Name := s.config.GetComponents().GetSupervisor1Name()
	supervisor2Name := s.config.GetComponents().GetSupervisor2Name()

	// Check if supervisor1 is active
	supervisor1Active, err := s.isActiveSupervisor(ctx, supervisor1Name)
	if err != nil {
		return "", "", status.Errorf(codes.Internal, "failed to check supervisor %q state: %v", supervisor1Name, err)
	}

	// Check if supervisor2 is active
	supervisor2Active, err := s.isActiveSupervisor(ctx, supervisor2Name)
	if err != nil {
		return "", "", status.Errorf(codes.Internal, "failed to check supervisor %q state: %v", supervisor2Name, err)
	}

	// Determine active and standby based on the results
	switch {
	case supervisor1Active && !supervisor2Active:
		return supervisor1Name, supervisor2Name, nil
	case supervisor2Active && !supervisor1Active:
		return supervisor2Name, supervisor1Name, nil
	case supervisor1Active && supervisor2Active:
		return "", "", status.Errorf(codes.FailedPrecondition, "both supervisors are active")
	default:
		return "", "", status.Errorf(codes.FailedPrecondition, "no active supervisor found")
	}
}

type wavelengthRouter struct {
	wrpb.UnimplementedWavelengthRouterServer
}

type Server struct {
	s                       *grpc.Server
	bgpServer               *bgp
	certServer              *cert
	diagServer              *diag
	fileServer              *file
	resetServer             *factoryReset
	healthzServer           *healthz
	layer2Server            *layer2
	linkQualificationServer *linkQualification
	mplsServer              *mpls
	osServer                *os
	otdrServer              *otdr
	systemServer            *system
	wavelengthRouterServer  *wavelengthRouter
}

func New(s *grpc.Server, gClient gpb.GNMIClient, target string, config *configpb.LemmingConfig) (*Server, error) {
	yclient, err := ygnmi.NewClient(gClient, ygnmi.WithTarget(target), ygnmi.WithRequestLogLevel(2))
	if err != nil {
		return nil, err
	}

	srv := &Server{
		s:                       s,
		bgpServer:               &bgp{},
		certServer:              &cert{},
		diagServer:              &diag{},
		fileServer:              &file{},
		resetServer:             &factoryReset{},
		healthzServer:           &healthz{},
		layer2Server:            &layer2{},
		mplsServer:              &mpls{},
		osServer:                &os{},
		otdrServer:              &otdr{},
		linkQualificationServer: newLinkQualification(yclient, config),
		systemServer:            newSystem(yclient, config),
		wavelengthRouterServer:  &wavelengthRouter{},
	}
	bpb.RegisterBGPServer(s, srv.bgpServer)
	cmpb.RegisterCertificateManagementServer(s, srv.certServer)
	diagpb.RegisterDiagServer(s, srv.diagServer)
	fpb.RegisterFileServer(s, srv.fileServer)
	frpb.RegisterFactoryResetServer(s, srv.resetServer)
	hpb.RegisterHealthzServer(s, srv.healthzServer)
	lpb.RegisterLayer2Server(s, srv.layer2Server)
	mpb.RegisterMPLSServer(s, srv.mplsServer)
	ospb.RegisterOSServer(s, srv.osServer)
	otpb.RegisterOTDRServer(s, srv.otdrServer)
	plqpb.RegisterLinkQualificationServer(s, srv.linkQualificationServer)
	spb.RegisterSystemServer(s, srv.systemServer)
	wrpb.RegisterWavelengthRouterServer(s, srv.wavelengthRouterServer)
	return srv, nil
}
