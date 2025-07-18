// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fault

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/openconfig/ondatra"
	"github.com/openconfig/testt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/openconfig/ondatra/gnmi"

	"github.com/openconfig/lemming/fault"
	"github.com/openconfig/lemming/internal/binding"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	plqpb "github.com/openconfig/gnoi/packet_link_qualification"
	spb "github.com/openconfig/gnoi/system"
	ctypes "github.com/openconfig/gnoi/types"
)

func TestMain(m *testing.M) {
	ondatra.RunTests(m, binding.KNE(".."))
}

type grpcDialer interface {
	DialGRPC(ctx context.Context, serviceName string, opts ...grpc.DialOption) (*grpc.ClientConn, error)
}

func TestGNMIFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)
	conn, err := ld.DialGRPC(context.Background(), "fault")
	if err != nil {
		t.Fatal(err)
	}
	s := fault.NewClient(conn).GNMISubscribe(t)
	s.SetReqCallback(func(sr *gpb.SubscribeRequest) (*gpb.SubscribeRequest, error) {
		return nil, fmt.Errorf("fake error")
	})

	testt.ExpectFatal(t, func(t testing.TB) {
		gnmi.Get(t, dut, gnmi.OC().System().State())
	})
}

// TestGNOIRebootFault tests reboot fault injection scenarios
func TestGNOIRebootFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	// Connect to gNOI service
	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("reboot fails with hardware error", func(t *testing.T) {
		// Add panic recovery
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		// Create dedicated fault connection for this test case
		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		// Set up fault injection with proper isolation
		faultClient := fault.NewClient(faultConn)
		rebootInterceptor := faultClient.GNOIReboot(t)
		defer func() {
			rebootInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// Inject hardware failure error
		rebootInterceptor.SetBypass(func(req *spb.RebootRequest) (*spb.RebootResponse, error) {
			return nil, status.Errorf(codes.Internal, "hardware failure during reboot")
		})

		// Wait for interceptor to be fully registered
		time.Sleep(100 * time.Millisecond)

		// Perform reboot operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.System().Reboot(ctx, &spb.RebootRequest{
			Method: spb.RebootMethod_COLD,
		})

		if err == nil {
			t.Errorf("Expected reboot to fail with hardware error, but it succeeded")
		} else {
			if !contains(err.Error(), "hardware failure during reboot") {
				t.Errorf("Expected error to contain 'hardware failure during reboot', got: %v", err)
			}
			if status.Code(err) != codes.Internal {
				t.Errorf("Expected error code %v, got %v", codes.Internal, status.Code(err))
			}
		}
	})
}

// TestGNOISwitchoverFault tests switchover fault injection scenarios
func TestGNOISwitchoverFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("switchover fails with supervisor unavailable", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		faultClient := fault.NewClient(faultConn)
		switchoverInterceptor := faultClient.GNOISwitchControlProcessor(t)
		defer func() {
			switchoverInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// Inject supervisor unavailable error
		switchoverInterceptor.SetBypass(func(req *spb.SwitchControlProcessorRequest) (*spb.SwitchControlProcessorResponse, error) {
			return nil, status.Errorf(codes.Unavailable, "target supervisor not available for switchover")
		})
		time.Sleep(100 * time.Millisecond)

		// Perform switchover operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.System().SwitchControlProcessor(ctx, &spb.SwitchControlProcessorRequest{
			ControlProcessor: &ctypes.Path{
				Elem: []*ctypes.PathElem{
					{Name: "components"},
					{Name: "component", Key: map[string]string{"name": "Supervisor2"}},
				},
			},
		})

		if err == nil {
			t.Errorf("Expected switchover to fail with supervisor unavailable, but it succeeded")
		} else {
			if !contains(err.Error(), "target supervisor not available for switchover") {
				t.Errorf("Expected error to contain 'target supervisor not available for switchover', got: %v", err)
			}
			if status.Code(err) != codes.Unavailable {
				t.Errorf("Expected error code %v, got %v", codes.Unavailable, status.Code(err))
			}
		}
	})
}

// TestGNOIProcessKillFault tests process kill fault injection scenarios
func TestGNOIProcessKillFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("kill process fails with permission denied", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		// Set up fault injection with proper isolation
		faultClient := fault.NewClient(faultConn)
		killInterceptor := faultClient.GNOIKillProcess(t)
		defer func() {
			killInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		killInterceptor.SetBypass(func(req *spb.KillProcessRequest) (*spb.KillProcessResponse, error) {
			return nil, status.Errorf(codes.PermissionDenied, "insufficient permissions to kill critical process")
		})

		time.Sleep(100 * time.Millisecond)

		// Perform kill process operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.System().KillProcess(ctx, &spb.KillProcessRequest{
			Name:   "grpc_server",
			Signal: spb.KillProcessRequest_SIGNAL_TERM,
		})

		if err == nil {
			t.Errorf("Expected kill process to fail with permission denied, but it succeeded")
		} else {
			if !contains(err.Error(), "insufficient permissions to kill critical process") {
				t.Errorf("Expected error to contain 'insufficient permissions to kill critical process', got: %v", err)
			}
			if status.Code(err) != codes.PermissionDenied {
				t.Errorf("Expected error code %v, got %v", codes.PermissionDenied, status.Code(err))
			}
		}
	})
}

// TestGNOIPingFault tests ping streaming fault injection scenarios
func TestGNOIPingFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("ping fails with subsystem error", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		faultClient := fault.NewClient(faultConn)
		pingInterceptor := faultClient.GNOIPing(t)
		defer func() {
			pingInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// For streaming RPCs, inject error on first request
		pingInterceptor.SetReqCallback(func(req *spb.PingRequest) (*spb.PingRequest, error) {
			return nil, status.Errorf(codes.Internal, "ping subsystem failure")
		})

		time.Sleep(100 * time.Millisecond)

		// Perform ping operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		pingStream, err := gnoiConn.System().Ping(ctx, &spb.PingRequest{
			Destination: "192.168.1.1",
			Count:       2,
			Interval:    1000000000, // 1 second
		})

		var finalErr error
		if err == nil {
			_, streamErr := pingStream.Recv()
			finalErr = streamErr
		} else {
			finalErr = err
		}

		if finalErr == nil {
			t.Errorf("Expected ping to fail with subsystem error, but it succeeded")
		} else {
			if !contains(finalErr.Error(), "ping subsystem failure") {
				t.Errorf("Expected error to contain 'ping subsystem failure', got: %v", finalErr)
			}
			if status.Code(finalErr) != codes.Internal {
				t.Errorf("Expected error code %v, got %v", codes.Internal, status.Code(finalErr))
			}
		}
	})
}

// TestGNOILinkQualCapabilitiesFault tests link qualification capabilities fault injection scenarios
func TestGNOILinkQualCapabilitiesFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("capabilities fails hardware error", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		faultClient := fault.NewClient(faultConn)
		capInterceptor := faultClient.GNOILinkQualificationCapabilities(t)
		defer func() {
			capInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// Inject hardware capability error
		capInterceptor.SetBypass(func(req *plqpb.CapabilitiesRequest) (*plqpb.CapabilitiesResponse, error) {
			return nil, status.Errorf(codes.Internal, "hardware measurement subsystem unavailable")
		})
		time.Sleep(100 * time.Millisecond)

		// Perform capabilities operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.LinkQualification().Capabilities(ctx, &plqpb.CapabilitiesRequest{})

		if err == nil {
			t.Errorf("Expected capabilities to fail with hardware error, but it succeeded")
		} else {
			if !contains(err.Error(), "hardware measurement subsystem unavailable") {
				t.Errorf("Expected error to contain 'hardware measurement subsystem unavailable', got: %v", err)
			}
			if status.Code(err) != codes.Internal {
				t.Errorf("Expected error code %v, got %v", codes.Internal, status.Code(err))
			}
		}
	})
}

// TestGNOILinkQualCreateFault tests link qualification create fault injection scenarios
func TestGNOILinkQualCreateFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("create fails with resource exhausted", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		faultClient := fault.NewClient(faultConn)
		createInterceptor := faultClient.GNOILinkQualificationCreate(t)
		defer func() {
			createInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// Inject resource exhausted error
		createInterceptor.SetBypass(func(req *plqpb.CreateRequest) (*plqpb.CreateResponse, error) {
			return nil, status.Errorf(codes.ResourceExhausted, "maximum number of concurrent link qualification tests reached")
		})
		time.Sleep(100 * time.Millisecond)

		// Perform create operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.LinkQualification().Create(ctx, &plqpb.CreateRequest{
			Interfaces: []*plqpb.QualificationConfiguration{
				{
					Id:            "test-linkqual-1",
					InterfaceName: "eth0",
				},
			},
		})

		if err == nil {
			t.Errorf("Expected create to fail with resource exhausted, but it succeeded")
		} else {
			if !contains(err.Error(), "maximum number of concurrent link qualification tests reached") {
				t.Errorf("Expected error to contain 'maximum number of concurrent link qualification tests reached', got: %v", err)
			}
			if status.Code(err) != codes.ResourceExhausted {
				t.Errorf("Expected error code %v, got %v", codes.ResourceExhausted, status.Code(err))
			}
		}
	})
}

// TestGNOILinkQualGetFault tests link qualification get fault injection scenarios
func TestGNOILinkQualGetFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("get fails with internal system error", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		faultClient := fault.NewClient(faultConn)
		getInterceptor := faultClient.GNOILinkQualificationGet(t)
		defer func() {
			getInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// Inject internal error
		getInterceptor.SetBypass(func(req *plqpb.GetRequest) (*plqpb.GetResponse, error) {
			return nil, status.Errorf(codes.Internal, "failed to access test result database")
		})
		time.Sleep(100 * time.Millisecond)

		// Perform get operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.LinkQualification().Get(ctx, &plqpb.GetRequest{
			Ids: []string{"existing-test-id"},
		})

		if err == nil {
			t.Errorf("Expected get to fail with internal error, but it succeeded")
		} else {
			if !contains(err.Error(), "failed to access test result database") {
				t.Errorf("Expected error to contain 'failed to access test result database', got: %v", err)
			}
			if status.Code(err) != codes.Internal {
				t.Errorf("Expected error code %v, got %v", codes.Internal, status.Code(err))
			}
		}
	})
}

// TestGNOILinkQualDeleteFault tests link qualification delete fault injection scenarios
func TestGNOILinkQualDeleteFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("delete fails with not found", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		faultClient := fault.NewClient(faultConn)
		deleteInterceptor := faultClient.GNOILinkQualificationDelete(t)
		defer func() {
			deleteInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// Inject not found error
		deleteInterceptor.SetBypass(func(req *plqpb.DeleteRequest) (*plqpb.DeleteResponse, error) {
			return nil, status.Errorf(codes.NotFound, "link qualification test not found")
		})
		time.Sleep(100 * time.Millisecond)

		// Perform delete operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.LinkQualification().Delete(ctx, &plqpb.DeleteRequest{
			Ids: []string{"nonexistent-test-id"},
		})

		if err == nil {
			t.Errorf("Expected delete to fail with not found, but it succeeded")
		} else {
			if !contains(err.Error(), "link qualification test not found") {
				t.Errorf("Expected error to contain 'link qualification test not found', got: %v", err)
			}
			if status.Code(err) != codes.NotFound {
				t.Errorf("Expected error code %v, got %v", codes.NotFound, status.Code(err))
			}
		}
	})
}

// TestGNOILinkQualListFault tests link qualification list fault injection scenarios
func TestGNOILinkQualListFault(t *testing.T) {
	dut := ondatra.DUT(t, "dut")
	ld := dut.RawAPIs().BindingDUT().(grpcDialer)

	gnoiConn, err := dut.RawAPIs().BindingDUT().DialGNOI(context.Background())
	if err != nil {
		t.Fatalf("Failed to dial gNOI service: %v", err)
	}

	t.Run("list fails with deadline exceeded", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Test panicked: %v", r)
			}
		}()

		faultConn, err := ld.DialGRPC(context.Background(), "fault")
		if err != nil {
			t.Fatalf("Failed to dial fault service: %v", err)
		}
		defer faultConn.Close()

		faultClient := fault.NewClient(faultConn)
		listInterceptor := faultClient.GNOILinkQualificationList(t)
		defer func() {
			listInterceptor.Stop()
			time.Sleep(200 * time.Millisecond)
		}()

		// Inject deadline exceeded error
		listInterceptor.SetBypass(func(req *plqpb.ListRequest) (*plqpb.ListResponse, error) {
			return nil, status.Errorf(codes.DeadlineExceeded, "operation timed out while querying test results")
		})
		time.Sleep(100 * time.Millisecond)

		// Perform list operation
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err = gnoiConn.LinkQualification().List(ctx, &plqpb.ListRequest{})

		if err == nil {
			t.Errorf("Expected list to fail with deadline exceeded, but it succeeded")
		} else {
			if !contains(err.Error(), "operation timed out while querying test results") {
				t.Errorf("Expected error to contain 'operation timed out while querying test results', got: %v", err)
			}
			if status.Code(err) != codes.DeadlineExceeded {
				t.Errorf("Expected error code %v, got %v", codes.DeadlineExceeded, status.Code(err))
			}
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > len(substr) && containsHelper(s, substr)))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
