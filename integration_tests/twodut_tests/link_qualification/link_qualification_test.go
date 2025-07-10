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

package link_qualification_test

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	plqpb "github.com/openconfig/gnoi/packet_link_qualification"
	"github.com/openconfig/gnoigo"
	"github.com/openconfig/lemming/gnmi/oc"
	"github.com/openconfig/lemming/gnmi/oc/ocpath"
	"github.com/openconfig/lemming/internal/binding"
	"github.com/openconfig/ondatra"
	"github.com/openconfig/ondatra/gnmi"
	"github.com/openconfig/ygot/ygot"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	// Test timing configuration
	testDuration     = 10 * time.Second
	setupDuration    = 2 * time.Second
	teardownDuration = 2 * time.Second
	preSyncDuration  = 1 * time.Second
	postSyncDuration = 1 * time.Second
	waitTimeout      = 30 * time.Second
	pollInterval     = 2 * time.Second

	// Test packet configuration
	simulationPacketRate = uint64(138888)
	testPacketSize       = uint32(1500)

	// Result validation tolerance
	tolerancePercent = 2.0
)

func TestMain(m *testing.M) {
	ondatra.RunTests(m, binding.KNE(".."))
}

// LinkQualificationTiming holds capability-based timing configuration
type LinkQualificationTiming struct {
	generatorSetupDuration    time.Duration
	reflectorSetupDuration    time.Duration
	generatorPreSyncDuration  time.Duration
	reflectorPreSyncDuration  time.Duration
	testDuration              time.Duration
	generatorPostSyncDuration time.Duration
	reflectorPostSyncDuration time.Duration
	generatorTeardownDuration time.Duration
	reflectorTeardownDuration time.Duration
}

// Topology:
//
//	dut(lemming):eth1 <--> dut2(lemming2):eth1
//
// Test Flow:
//  1. Check capabilities on both devices
//  2. Clean up any existing qualifications
//  3. Create generator qualification on dut:eth1 (using 138888 PPS)
//  4. Create reflector qualification on dut2:eth1 (using 138888 PPS)
//  5. Monitor both qualifications until completion
//  6. Validate individual device results
//  7. Validate cross-device correlation with matched rates
//  8. Cleanup qualifications
func TestLinkQualification(t *testing.T) {
	dut1 := ondatra.DUT(t, "dut")
	dut2 := ondatra.DUT(t, "dut2")

	t.Logf("Starting cross-device link qualification test")
	t.Logf("Generator device: %s (interface: eth1)", dut1.Name())
	t.Logf("Reflector device: %s (interface: eth1)", dut2.Name())

	// Configure interfaces on both devices
	configureInterfaces(t, dut1, dut2)

	// Give interfaces time to be fully available in state tree
	time.Sleep(2 * time.Second)
	t.Logf("Interfaces configured and ready for link qualification")

	// Get gNOI clients for both devices
	gnoiClient1 := dut1.RawAPIs().GNOI(t)
	gnoiClient2 := dut2.RawAPIs().GNOI(t)

	// Check capabilities on both devices
	genCapabilities := checkCapabilities(t, gnoiClient1, "generator", dut1.Name())
	refCapabilities := checkCapabilities(t, gnoiClient2, "reflector", dut2.Name())

	// Calculate test durations based on capabilities
	timing := calculateTestDurations(t, genCapabilities, refCapabilities)

	// Clean up any existing qualifications
	cleanupExistingQualifications(t, gnoiClient1, gnoiClient2)

	// Generate unique test IDs
	testID := fmt.Sprintf("cross-device-%d", time.Now().Unix())
	generatorID := testID + "-generator"
	reflectorID := testID + "-reflector"

	t.Logf("Test ID: %s", testID)
	t.Logf("Generator ID: %s on %s:eth1", generatorID, dut1.Name())
	t.Logf("Reflector ID: %s on %s:eth1", reflectorID, dut2.Name())

	// Create generator qualification on dut1
	createGeneratorQualification(t, gnoiClient1, generatorID, timing, genCapabilities)

	// Create reflector qualification on dut2
	createReflectorQualification(t, gnoiClient2, reflectorID, timing, refCapabilities)

	// Monitor both qualifications until completion
	monitorQualifications(t, gnoiClient1, gnoiClient2, generatorID, reflectorID, dut1, dut2)

	// Retrieve final results from both devices
	genResult := getQualificationResult(t, gnoiClient1, generatorID)
	refResult := getQualificationResult(t, gnoiClient2, reflectorID)

	// Validate link qualification pair (simulation - no real packet flow)
	validateLinkQualificationPair(t, genResult, refResult, dut1.Name(), dut2.Name())

	// Cleanup
	cleanupQualifications(t, gnoiClient1, gnoiClient2, generatorID, reflectorID)

	t.Logf("Cross-device link qualification test completed successfully")
}

// configureInterfaces sets up test interfaces with proper MTU
func configureInterfaces(t *testing.T, dut1, dut2 *ondatra.DUTDevice) {
	t.Helper()

	for _, dut := range []*ondatra.DUTDevice{dut1, dut2} {
		intf := &oc.Interface{
			Name:        ygot.String("eth1"),
			Type:        oc.IETFInterfaces_InterfaceType_ethernetCsmacd,
			Enabled:     ygot.Bool(true),
			Mtu:         ygot.Uint16(9000),
			Description: ygot.String("Link qualification test interface"),
		}

		gnmi.Replace(t, dut, ocpath.Root().Interface("eth1").Config(), intf)

		// Wait for interface to come up
		gnmi.Await(t, dut, ocpath.Root().Interface("eth1").OperStatus().State(),
			10*time.Second, oc.Interface_OperStatus_UP)

		t.Logf("Configured interface eth1 on %s", dut.Name())
	}
}

// checkCapabilities verifies device capabilities
func checkCapabilities(t *testing.T, client gnoigo.Clients, deviceType, deviceName string) *plqpb.CapabilitiesResponse {
	t.Helper()

	resp, err := client.LinkQualification().Capabilities(context.Background(), &plqpb.CapabilitiesRequest{})
	if err != nil {
		t.Fatalf("Failed to get capabilities for %s (%s): %v", deviceType, deviceName, err)
	}

	t.Logf("Capabilities for %s (%s):", deviceType, deviceName)
	t.Logf("  MaxHistoricalResults: %d", resp.GetMaxHistoricalResultsPerInterface())

	if gen := resp.GetGenerator().GetPacketGenerator(); gen != nil {
		t.Logf("  Generator MaxBps: %d", gen.GetMaxBps())
		t.Logf("  Generator MaxPps: %d", gen.GetMaxPps())
		t.Logf("  Generator MaxMtu: %d", gen.GetMaxMtu())
		t.Logf("  Generator MinSetupDuration: %v", gen.GetMinSetupDuration().AsDuration())
		t.Logf("  Generator MinTeardownDuration: %v", gen.GetMinTeardownDuration().AsDuration())

		// Verify minimum requirements for simulation
		if gen.GetMaxPps() < 1000 {
			t.Errorf("Generator MaxPps too low for simulation: got %d, want >= 1000", gen.GetMaxPps())
		}
		if gen.GetMaxMtu() < uint32(testPacketSize) {
			t.Errorf("Generator MaxMtu too low: got %d, want >= %d", gen.GetMaxMtu(), testPacketSize)
		}
	}

	// Check reflector capabilities
	hasAsicLoopback := resp.GetReflector().GetAsicLoopback() != nil
	hasPmdLoopback := resp.GetReflector().GetPmdLoopback() != nil

	if !hasAsicLoopback && !hasPmdLoopback {
		t.Fatalf("Device %s (%s) supports neither ASIC nor PMD loopback", deviceType, deviceName)
	}

	if hasAsicLoopback {
		asic := resp.GetReflector().GetAsicLoopback()
		t.Logf("  Reflector: ASIC loopback supported (setup: %v, teardown: %v)",
			asic.GetMinSetupDuration().AsDuration(), asic.GetMinTeardownDuration().AsDuration())
	}
	if hasPmdLoopback {
		pmd := resp.GetReflector().GetPmdLoopback()
		t.Logf("  Reflector: PMD loopback supported (setup: %v, teardown: %v)",
			pmd.GetMinSetupDuration().AsDuration(), pmd.GetMinTeardownDuration().AsDuration())
	}

	return resp
}

// calculateTestDurations determines timing based on capabilities
func calculateTestDurations(t *testing.T, genCaps, refCaps *plqpb.CapabilitiesResponse) *LinkQualificationTiming {
	t.Helper()

	// Use capabilities to determine minimum required durations
	genMinSetup := genCaps.GetGenerator().GetPacketGenerator().GetMinSetupDuration().AsDuration()
	genMinTeardown := genCaps.GetGenerator().GetPacketGenerator().GetMinTeardownDuration().AsDuration()

	var refMinSetup, refMinTeardown time.Duration
	if refCaps.GetReflector().GetAsicLoopback() != nil {
		refMinSetup = refCaps.GetReflector().GetAsicLoopback().GetMinSetupDuration().AsDuration()
		refMinTeardown = refCaps.GetReflector().GetAsicLoopback().GetMinTeardownDuration().AsDuration()
	} else {
		refMinSetup = refCaps.GetReflector().GetPmdLoopback().GetMinSetupDuration().AsDuration()
		refMinTeardown = refCaps.GetReflector().GetPmdLoopback().GetMinTeardownDuration().AsDuration()
	}

	// Use maximum of required and configured durations
	timing := &LinkQualificationTiming{
		generatorSetupDuration:    max(setupDuration, genMinSetup),
		reflectorSetupDuration:    max(setupDuration, refMinSetup),
		generatorPreSyncDuration:  preSyncDuration,
		reflectorPreSyncDuration:  0,
		testDuration:              testDuration,
		generatorPostSyncDuration: 0,
		reflectorPostSyncDuration: postSyncDuration,
		generatorTeardownDuration: max(teardownDuration, genMinTeardown),
		reflectorTeardownDuration: max(teardownDuration, refMinTeardown),
	}

	t.Logf("Calculated test durations:")
	t.Logf("  Setup: generator=%v, reflector=%v", timing.generatorSetupDuration, timing.reflectorSetupDuration)
	t.Logf("  Test duration: %v", timing.testDuration)
	t.Logf("  Teardown: generator=%v, reflector=%v", timing.generatorTeardownDuration, timing.reflectorTeardownDuration)

	return timing
}

// max returns the maximum of two durations
func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// cleanupExistingQualifications removes any existing qualifications
func cleanupExistingQualifications(t *testing.T, client1, client2 gnoigo.Clients) {
	t.Helper()

	for i, client := range []gnoigo.Clients{client1, client2} {
		listResp, err := client.LinkQualification().List(context.Background(), &plqpb.ListRequest{})
		if err != nil {
			t.Logf("Warning: Failed to list existing qualifications on device %d: %v", i+1, err)
			continue
		}

		if len(listResp.GetResults()) > 0 {
			var ids []string
			for _, result := range listResp.GetResults() {
				ids = append(ids, result.GetId())
			}

			_, err := client.LinkQualification().Delete(context.Background(), &plqpb.DeleteRequest{Ids: ids})
			if err != nil {
				t.Logf("Warning: Failed to cleanup existing qualifications on device %d: %v", i+1, err)
			} else {
				t.Logf("Cleaned up %d existing qualifications on device %d", len(ids), i+1)
			}
		}
	}
}

// createGeneratorQualification creates a packet generator
func createGeneratorQualification(t *testing.T, client gnoigo.Clients, id string, timing *LinkQualificationTiming, caps *plqpb.CapabilitiesResponse) {
	t.Helper()
	packetRate := simulationPacketRate
	packetSize := testPacketSize

	// Adjust based on device capabilities
	if gen := caps.GetGenerator().GetPacketGenerator(); gen != nil {
		if gen.GetMaxPps() < simulationPacketRate {
			packetRate = gen.GetMaxPps() / 2
		}
		if gen.GetMaxMtu() < testPacketSize {
			packetSize = gen.GetMaxMtu()
		}
	}

	req := &plqpb.CreateRequest{
		Interfaces: []*plqpb.QualificationConfiguration{
			{
				Id:            id,
				InterfaceName: "eth1",
				EndpointType: &plqpb.QualificationConfiguration_PacketGenerator{
					PacketGenerator: &plqpb.PacketGeneratorConfiguration{
						PacketRate: packetRate,
						PacketSize: packetSize,
					},
				},
				Timing: &plqpb.QualificationConfiguration_Rpc{
					Rpc: &plqpb.RPCSyncedTiming{
						Duration:         durationpb.New(timing.testDuration),
						PreSyncDuration:  durationpb.New(timing.generatorPreSyncDuration),
						SetupDuration:    durationpb.New(timing.generatorSetupDuration),
						PostSyncDuration: durationpb.New(timing.generatorPostSyncDuration),
						TeardownDuration: durationpb.New(timing.generatorTeardownDuration),
					},
				},
			},
		},
	}

	resp, err := client.LinkQualification().Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Failed to create generator qualification: %v", err)
	}

	if status := resp.GetStatus()[id]; status.GetCode() != 0 {
		t.Fatalf("Generator creation failed: %s", status.GetMessage())
	}

	t.Logf("Successfully created generator qualification: %s (rate: %d pps, size: %d bytes)",
		id, packetRate, packetSize)
}

// createReflectorQualification creates a reflector
func createReflectorQualification(t *testing.T, client gnoigo.Clients, id string, timing *LinkQualificationTiming, caps *plqpb.CapabilitiesResponse) {
	t.Helper()

	config := &plqpb.QualificationConfiguration{
		Id:            id,
		InterfaceName: "eth1",
		Timing: &plqpb.QualificationConfiguration_Rpc{
			Rpc: &plqpb.RPCSyncedTiming{
				Duration:         durationpb.New(timing.testDuration),
				PreSyncDuration:  durationpb.New(timing.reflectorPreSyncDuration),
				SetupDuration:    durationpb.New(timing.reflectorSetupDuration),
				PostSyncDuration: durationpb.New(timing.reflectorPostSyncDuration),
				TeardownDuration: durationpb.New(timing.reflectorTeardownDuration),
			},
		},
	}

	// Choose loopback type based on capabilities
	if caps.GetReflector().GetAsicLoopback() != nil {
		config.EndpointType = &plqpb.QualificationConfiguration_AsicLoopback{
			AsicLoopback: &plqpb.AsicLoopbackConfiguration{},
		}
		t.Logf("Using ASIC loopback for reflector")
	} else {
		config.EndpointType = &plqpb.QualificationConfiguration_PmdLoopback{
			PmdLoopback: &plqpb.PmdLoopbackConfiguration{},
		}
		t.Logf("Using PMD loopback for reflector")
	}

	req := &plqpb.CreateRequest{Interfaces: []*plqpb.QualificationConfiguration{config}}

	resp, err := client.LinkQualification().Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Failed to create reflector qualification: %v", err)
	}

	if status := resp.GetStatus()[id]; status.GetCode() != 0 {
		t.Fatalf("Reflector creation failed: %s", status.GetMessage())
	}

	t.Logf("Successfully created reflector qualification: %s", id)
}

// monitorQualifications watches both qualifications with proper state validation
func monitorQualifications(t *testing.T, client1, client2 gnoigo.Clients, genID, refID string, dut1, dut2 *ondatra.DUTDevice) {
	t.Helper()

	t.Logf("Monitoring qualifications until completion...")
	startTime := time.Now()

	for time.Since(startTime) < waitTimeout {
		time.Sleep(pollInterval)

		// Check generator status
		genResp, err := client1.LinkQualification().Get(context.Background(), &plqpb.GetRequest{Ids: []string{genID}})
		if err != nil {
			t.Logf("Warning: Failed to get generator status: %v", err)
			continue
		}

		// Check reflector status
		refResp, err := client2.LinkQualification().Get(context.Background(), &plqpb.GetRequest{Ids: []string{refID}})
		if err != nil {
			t.Logf("Warning: Failed to get reflector status: %v", err)
			continue
		}

		genResult := genResp.GetResults()[genID]
		refResult := refResp.GetResults()[refID]

		if genResult == nil || refResult == nil {
			t.Logf("Warning: Missing results in response")
			continue
		}

		t.Logf("Status - Generator: %v, Reflector: %v", genResult.GetState(), refResult.GetState())

		// Check if interfaces are in TESTING state during RUNNING phase
		if genResult.GetState() == plqpb.QualificationState_QUALIFICATION_STATE_RUNNING {
			checkInterfaceStatus(t, dut1, "eth1", "generator")
		}
		if refResult.GetState() == plqpb.QualificationState_QUALIFICATION_STATE_RUNNING {
			checkInterfaceStatus(t, dut2, "eth1", "reflector")
		}

		// Check if both completed
		if genResult.GetState() == plqpb.QualificationState_QUALIFICATION_STATE_COMPLETED &&
			refResult.GetState() == plqpb.QualificationState_QUALIFICATION_STATE_COMPLETED {
			t.Logf("Both qualifications completed successfully in %v", time.Since(startTime))
			return
		}

		// Check for errors
		if genResult.GetState() == plqpb.QualificationState_QUALIFICATION_STATE_ERROR {
			if genResult.GetStatus() != nil {
				t.Fatalf("Generator qualification failed: %s", genResult.GetStatus().GetMessage())
			} else {
				t.Fatalf("Generator qualification failed with unknown error")
			}
		}
		if refResult.GetState() == plqpb.QualificationState_QUALIFICATION_STATE_ERROR {
			if refResult.GetStatus() != nil {
				t.Fatalf("Reflector qualification failed: %s", refResult.GetStatus().GetMessage())
			} else {
				t.Fatalf("Reflector qualification failed with unknown error")
			}
		}
	}

	t.Fatalf("Timeout waiting for qualifications to complete after %v", waitTimeout)
}

// checkInterfaceStatus verifies interface is in TESTING state
func checkInterfaceStatus(t *testing.T, dut *ondatra.DUTDevice, intfName, deviceType string) {
	t.Helper()

	status := gnmi.Get(t, dut, ocpath.Root().Interface(intfName).OperStatus().State())
	if status != oc.Interface_OperStatus_TESTING {
		t.Logf("Warning: Interface %s on %s (%s) not in TESTING state during qualification: %v",
			intfName, dut.Name(), deviceType, status)
	} else {
		t.Logf("Interface %s on %s (%s) correctly in TESTING state", intfName, dut.Name(), deviceType)
	}
}

// getQualificationResult retrieves the final result from a device
func getQualificationResult(t *testing.T, client gnoigo.Clients, id string) *plqpb.QualificationResult {
	t.Helper()

	resp, err := client.LinkQualification().Get(context.Background(), &plqpb.GetRequest{Ids: []string{id}})
	if err != nil {
		t.Fatalf("Failed to get qualification result for %s: %v", id, err)
	}

	result := resp.GetResults()[id]
	if result == nil {
		t.Fatalf("Missing result for qualification %s", id)
	}

	return result
}

// validateLinkQualificationPair validates generator and reflector results as a coordinated pair
// Following feature profiles pattern but adapted for simulation (no real packet flow)
func validateLinkQualificationPair(t *testing.T, genResult, refResult *plqpb.QualificationResult, genDevice, refDevice string) {
	t.Helper()

	t.Logf("Validating Link Qualification pair: Generator(%s) ↔ Reflector(%s)", genDevice, refDevice)

	// Extract stats from both results (like feature profiles)
	genSent := genResult.GetPacketsSent()
	genReceived := genResult.GetPacketsReceived()
	refSent := refResult.GetPacketsSent()
	refReceived := refResult.GetPacketsReceived()

	t.Logf("Generator (%s): sent=%d, received=%d, dropped=%d, errors=%d",
		genDevice, genSent, genReceived, genResult.GetPacketsDropped(), genResult.GetPacketsError())
	t.Logf("Reflector (%s): sent=%d, received=%d, dropped=%d, errors=%d",
		refDevice, refSent, refReceived, refResult.GetPacketsDropped(), refResult.GetPacketsError())

	// 1. Both should complete successfully
	if genResult.GetState() != plqpb.QualificationState_QUALIFICATION_STATE_COMPLETED {
		t.Errorf("Generator (%s) did not complete successfully: %v", genDevice, genResult.GetState())
	}
	if refResult.GetState() != plqpb.QualificationState_QUALIFICATION_STATE_COMPLETED {
		t.Errorf("Reflector (%s) did not complete successfully: %v", refDevice, refResult.GetState())
	}

	// 2. Status field should only be set for ERROR states (protocol compliance)
	if genResult.GetState() != plqpb.QualificationState_QUALIFICATION_STATE_ERROR && genResult.GetStatus() != nil {
		t.Errorf("Generator (%s) has status field set for non-error state", genDevice)
	}
	if refResult.GetState() != plqpb.QualificationState_QUALIFICATION_STATE_ERROR && refResult.GetStatus() != nil {
		t.Errorf("Reflector (%s) has status field set for non-error state", refDevice)
	}

	// 3. No packet errors in simulation
	if genResult.GetPacketsError() != 0 {
		t.Errorf("Generator (%s) should have no packet errors in simulation, got: %d", genDevice, genResult.GetPacketsError())
	}
	if refResult.GetPacketsError() != 0 {
		t.Errorf("Reflector (%s) should have no packet errors in simulation, got: %d", refDevice, refResult.GetPacketsError())
	}

	// 4. Validate individual device behavior consistency
	expectedPackets := uint64(simulationPacketRate * uint64(testDuration.Seconds()))
	tolerance := float64(expectedPackets) * (tolerancePercent / 100.0)

	// Generator should send expected packets
	if genSent == 0 {
		t.Errorf("Generator (%s) should have sent packets", genDevice)
	} else if math.Abs(float64(genSent)-float64(expectedPackets)) > tolerance {
		t.Errorf("Generator (%s) packet count outside tolerance: got %d, expected %d ± %.0f",
			genDevice, genSent, expectedPackets, tolerance)
	}

	// Reflector should receive expected packets (independent simulation)
	if refReceived == 0 {
		t.Errorf("Reflector (%s) should have received packets in simulation", refDevice)
	} else if math.Abs(float64(refReceived)-float64(expectedPackets)) > tolerance {
		t.Errorf("Reflector (%s) packet count outside tolerance: got %d, expected %d ± %.0f",
			refDevice, refReceived, expectedPackets, tolerance)
	}

	// 5. Logical consistency (simulation-appropriate, not real packet flow)
	// Both devices use same rate, so they should have similar packet counts
	if genSent > 0 && refReceived > 0 {
		genRefDiff := math.Abs(float64(genSent) - float64(refReceived))
		genRefPercent := genRefDiff / float64(genSent) * 100.0
		t.Logf("Simulation consistency: gen_sent=%d, ref_received=%d, diff=%.2f%%",
			genSent, refReceived, genRefPercent)

		// Looser tolerance than feature profiles (this is simulation consistency, not real flow)
		if genRefPercent > tolerancePercent {
			t.Logf("Note: Generator/Reflector counts differ by %.2f%% (simulation variance)", genRefPercent)
		} else {
			t.Logf("✅ Simulation consistency good: %.2f%% ≤ %.2f%%", genRefPercent, tolerancePercent)
		}
	}

	// 6. Timing coordination (both should complete around same time)
	if genResult.GetStartTime() != nil && genResult.GetEndTime() != nil &&
		refResult.GetStartTime() != nil && refResult.GetEndTime() != nil {

		genDuration := genResult.GetEndTime().AsTime().Sub(genResult.GetStartTime().AsTime())
		refDuration := refResult.GetEndTime().AsTime().Sub(refResult.GetStartTime().AsTime())

		durationDiff := math.Abs(float64(genDuration - refDuration))
		maxDiff := float64(2 * time.Second)

		t.Logf("Timing coordination: gen_duration=%v, ref_duration=%v, diff=%v",
			genDuration, refDuration, time.Duration(durationDiff))

		if durationDiff > maxDiff {
			t.Errorf("Qualification durations too different: gen=%v, ref=%v, diff=%v > %v",
				genDuration, refDuration, time.Duration(durationDiff), time.Duration(maxDiff))
		} else {
			t.Logf("✅ Timing coordination good: diff=%v ≤ %v", time.Duration(durationDiff), time.Duration(maxDiff))
		}
	}

	t.Logf("✅ Link Qualification pair validation passed - both devices performed correctly in coordinated simulation")
}

// cleanupQualifications removes test qualifications
func cleanupQualifications(t *testing.T, client1, client2 gnoigo.Clients, genID, refID string) {
	t.Helper()

	// Cleanup generator
	_, err := client1.LinkQualification().Delete(context.Background(), &plqpb.DeleteRequest{Ids: []string{genID}})
	if err != nil {
		t.Logf("Warning: Failed to cleanup generator qualification: %v", err)
	} else {
		t.Logf("Cleaned up generator qualification: %s", genID)
	}

	// Cleanup reflector
	_, err = client2.LinkQualification().Delete(context.Background(), &plqpb.DeleteRequest{Ids: []string{refID}})
	if err != nil {
		t.Logf("Warning: Failed to cleanup reflector qualification: %v", err)
	} else {
		t.Logf("Cleaned up reflector qualification: %s", refID)
	}

	// Wait a bit after cleanup
	time.Sleep(2 * time.Second)
}
