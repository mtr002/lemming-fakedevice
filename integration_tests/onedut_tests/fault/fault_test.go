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

// TestGNOIRebootFaultInjection tests reboot fault injection scenarios
func TestGNOIRebootFaultInjection(t *testing.T) {
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

// TestGNOISwitchoverFaultInjection tests switchover fault injection scenarios
func TestGNOISwitchoverFaultInjection(t *testing.T) {
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

// TestGNOIProcessKillFaultInjection tests process kill fault injection scenarios
func TestGNOIProcessKillFaultInjection(t *testing.T) {
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

// TestGNOIPingFaultInjection tests ping streaming fault injection scenarios
func TestGNOIPingFaultInjection(t *testing.T) {
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
