package fault

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/testing/protocmp"
	"google.golang.org/protobuf/types/known/anypb"

	spb "github.com/openconfig/gnoi/system"
	"github.com/openconfig/lemming/fault"
	configpb "github.com/openconfig/lemming/proto/config"
	faultpb "github.com/openconfig/lemming/proto/fault"
)

func TestNewManager(t *testing.T) {
	tests := []struct {
		name        string
		config      *configpb.FaultServiceConfiguration
		interceptor *fault.Interceptor
		wantErr     bool
	}{
		{
			name:        "nil interceptor",
			config:      nil,
			interceptor: nil,
			wantErr:     true,
		},
		{
			name:        "valid interceptor, nil config",
			config:      nil,
			interceptor: fault.NewInterceptor(),
			wantErr:     false,
		},
		{
			name: "valid config and interceptor",
			config: &configpb.FaultServiceConfiguration{
				GnoiFaults: []*configpb.GNOIFaults{
					{
						RpcMethod: "/gnoi.system.System/Reboot",
						Faults: []*faultpb.FaultMessage{
							{
								MsgId: "test_fault_1",
								Status: &statuspb.Status{
									Code:    int32(codes.Internal),
									Message: "test error",
								},
							},
						},
					},
				},
			},
			interceptor: fault.NewInterceptor(),
			wantErr:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager, err := NewManager(tc.config, tc.interceptor)

			if tc.wantErr {
				if err == nil {
					t.Errorf("NewManager() expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("NewManager() unexpected error: %v", err)
				return
			}

			if manager == nil {
				t.Errorf("NewManager() returned nil manager")
			}
		})
	}
}

func TestManagerGetFaultRules(t *testing.T) {
	interceptor := fault.NewInterceptor()

	// Create test fault messages
	testFault := &faultpb.FaultMessage{
		MsgId: "test_fault",
		Status: &statuspb.Status{
			Code:    int32(codes.Internal),
			Message: "test error",
		},
	}

	config := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Reboot",
				Faults:    []*faultpb.FaultMessage{testFault},
			},
		},
	}

	manager, err := NewManager(config, interceptor)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	tests := []struct {
		name      string
		rpcMethod string
		want      []*faultpb.FaultMessage
	}{
		{
			name:      "existing method",
			rpcMethod: "/gnoi.system.System/Reboot",
			want:      []*faultpb.FaultMessage{testFault},
		},
		{
			name:      "non-existing method",
			rpcMethod: "/gnoi.system.System/Ping",
			want:      nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := manager.GetFaultRules(tc.rpcMethod)
			if diff := cmp.Diff(tc.want, got, protocmp.Transform()); diff != "" {
				t.Errorf("GetFaultRules() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestManagerHasFaults(t *testing.T) {
	interceptor := fault.NewInterceptor()

	config := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Reboot",
				Faults: []*faultpb.FaultMessage{
					{MsgId: "test_fault"},
				},
			},
			{
				RpcMethod: "/gnoi.system.System/EmptyFaults",
				Faults:    []*faultpb.FaultMessage{}, // Empty faults
			},
		},
	}

	manager, err := NewManager(config, interceptor)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	tests := []struct {
		name      string
		rpcMethod string
		want      bool
	}{
		{
			name:      "has faults",
			rpcMethod: "/gnoi.system.System/Reboot",
			want:      true,
		},
		{
			name:      "empty faults",
			rpcMethod: "/gnoi.system.System/EmptyFaults",
			want:      false,
		},
		{
			name:      "no config",
			rpcMethod: "/gnoi.system.System/Ping",
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := manager.HasFaults(tc.rpcMethod)
			if got != tc.want {
				t.Errorf("HasFaults(%q) = %v, want %v", tc.rpcMethod, got, tc.want)
			}
		})
	}
}

func TestManagerGetConfiguredMethods(t *testing.T) {
	interceptor := fault.NewInterceptor()

	config := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Reboot",
				Faults:    []*faultpb.FaultMessage{{MsgId: "test1"}},
			},
			{
				RpcMethod: "/gnoi.system.System/Ping",
				Faults:    []*faultpb.FaultMessage{{MsgId: "test2"}},
			},
		},
	}

	manager, err := NewManager(config, interceptor)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	got := manager.GetConfiguredMethods()
	want := []string{
		"/gnoi.system.System/Reboot",
		"/gnoi.system.System/Ping",
	}

	if len(got) != len(want) {
		t.Errorf("GetConfiguredMethods() returned %d methods, want %d", len(got), len(want))
	}

	// Check that all expected methods are present
	gotMap := make(map[string]bool)
	for _, method := range got {
		gotMap[method] = true
	}

	for _, method := range want {
		if !gotMap[method] {
			t.Errorf("GetConfiguredMethods() missing method: %s", method)
		}
	}
}

func TestManagerUpdateConfiguration(t *testing.T) {
	interceptor := fault.NewInterceptor()

	// Initial config
	initialConfig := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Reboot",
				Faults:    []*faultpb.FaultMessage{{MsgId: "initial"}},
			},
		},
	}

	manager, err := NewManager(initialConfig, interceptor)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	// Verify initial state
	if !manager.HasFaults("/gnoi.system.System/Reboot") {
		t.Error("Expected initial fault configuration")
	}

	// Update config
	newConfig := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Ping",
				Faults:    []*faultpb.FaultMessage{{MsgId: "updated"}},
			},
		},
	}

	err = manager.UpdateConfiguration(newConfig)
	if err != nil {
		t.Fatalf("UpdateConfiguration() failed: %v", err)
	}

	// Verify updated state
	if manager.HasFaults("/gnoi.system.System/Reboot") {
		t.Error("Expected old fault configuration to be cleared")
	}

	if !manager.HasFaults("/gnoi.system.System/Ping") {
		t.Error("Expected new fault configuration")
	}

	// Test updating to nil config
	if err := manager.UpdateConfiguration(nil); err != nil {
		t.Errorf("UpdateConfiguration(nil) should not fail: %v", err)
	}

	if manager.HasFaults("/gnoi.system.System/Ping") {
		t.Error("Expected all fault configuration to be cleared after nil update")
	}
}

func TestManagerStop(t *testing.T) {
	interceptor := fault.NewInterceptor()

	config := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Reboot",
				Faults:    []*faultpb.FaultMessage{{MsgId: "test"}},
			},
		},
	}

	manager, err := NewManager(config, interceptor)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	// Verify initial state
	if !manager.HasFaults("/gnoi.system.System/Reboot") {
		t.Error("Expected fault configuration before stop")
	}

	// Stop manager
	manager.Stop()

	// Verify cleared state
	if manager.HasFaults("/gnoi.system.System/Reboot") {
		t.Error("Expected fault configuration to be cleared after stop")
	}

	if len(manager.GetConfiguredMethods()) != 0 {
		t.Error("Expected no configured methods after stop")
	}
}

// TestManagerConcurrency tests concurrent access to manager methods
func TestManagerConcurrency(t *testing.T) {
	interceptor := fault.NewInterceptor()
	config := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Reboot",
				Faults:    []*faultpb.FaultMessage{{MsgId: "test"}},
			},
		},
	}

	manager, err := NewManager(config, interceptor)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	// Run concurrent operations
	const numGoroutines = 100
	done := make(chan bool, numGoroutines)

	// Concurrent reads
	for i := 0; i < numGoroutines/2; i++ {
		go func() {
			defer func() { done <- true }()
			_ = manager.HasFaults("/gnoi.system.System/Reboot")
			_ = manager.GetFaultRules("/gnoi.system.System/Reboot")
			_ = manager.GetConfiguredMethods()
		}()
	}

	// Concurrent config updates
	for i := 0; i < numGoroutines/2; i++ {
		go func(id int) {
			defer func() { done <- true }()
			newConfig := &configpb.FaultServiceConfiguration{
				GnoiFaults: []*configpb.GNOIFaults{
					{
						RpcMethod: "/gnoi.system.System/Ping",
						Faults:    []*faultpb.FaultMessage{{MsgId: "concurrent"}},
					},
				},
			}
			_ = manager.UpdateConfiguration(newConfig)
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}
}

// Integration test with actual fault messages
func TestManagerWithRealFaultMessages(t *testing.T) {
	interceptor := fault.NewInterceptor()

	// Create a real reboot request message
	rebootReq := &spb.RebootRequest{
		Method: spb.RebootMethod_COLD,
		Delay:  1000,
	}
	rebootReqAny, err := anypb.New(rebootReq)
	if err != nil {
		t.Fatalf("Failed to create Any message: %v", err)
	}

	config := &configpb.FaultServiceConfiguration{
		GnoiFaults: []*configpb.GNOIFaults{
			{
				RpcMethod: "/gnoi.system.System/Reboot",
				Faults: []*faultpb.FaultMessage{
					{
						MsgId: "reboot_fault",
						Msg:   rebootReqAny,
						Status: &statuspb.Status{
							Code:    int32(codes.PermissionDenied),
							Message: "Reboot not allowed in maintenance mode",
						},
					},
				},
			},
		},
	}

	manager, err := NewManager(config, interceptor)
	if err != nil {
		t.Fatalf("NewManager() failed: %v", err)
	}

	// Verify the fault message is stored correctly
	faults := manager.GetFaultRules("/gnoi.system.System/Reboot")
	if len(faults) != 1 {
		t.Fatalf("Expected 1 fault, got %d", len(faults))
	}

	fault := faults[0]
	if fault.GetMsgId() != "reboot_fault" {
		t.Errorf("Expected msg_id 'reboot_fault', got %q", fault.GetMsgId())
	}

	// Verify the status
	st := status.FromProto(fault.GetStatus())
	if st.Code() != codes.PermissionDenied {
		t.Errorf("Expected PermissionDenied, got %v", st.Code())
	}

	if st.Message() != "Reboot not allowed in maintenance mode" {
		t.Errorf("Expected specific error message, got %q", st.Message())
	}
}
