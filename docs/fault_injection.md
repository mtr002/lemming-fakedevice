# Config-Based Fault Injection

This document describes how to use configuration-based fault injection in Lemming for testing gNOI service failures.

## Overview

Config-based fault injection allows you to pre-configure fault responses for specific gNOI RPC methods directly in the Lemming configuration file. This provides deterministic, reproducible fault scenarios for integration testing without requiring external fault injection clients.

## Configuration

### Basic Structure

```protobuf
fault_config {
  gnoi_faults {
    rpc_method: "/gnoi.system.System/Reboot"
    faults {
      msg_id: "reboot_fault_1"
      status {
        code: 3  # INVALID_ARGUMENT
        message: "Simulated reboot failure"
      }
    }
  }
}
```

### Configuration Fields

- **`rpc_method`**: Full gRPC method name (e.g., `/gnoi.system.System/Reboot`)
- **`faults`**: Array of fault messages to inject
  - **`msg_id`**: Unique identifier for the fault
  - **`msg`**: Optional modified request/response message  
  - **`status`**: gRPC status to return (code and message)

### Supported RPC Methods

Common gNOI RPC methods that can be fault-injected:

- `/gnoi.system.System/Reboot`
- `/gnoi.system.System/CancelReboot`
- `/gnoi.system.System/KillProcess`
- `/gnoi.system.System/Ping`
- `/gnoi.system.System/Time`
- `/gnoi.system.System/SwitchControlProcessor`
- `/gnoi.packet_link_qualification.LinkQualification/Create`
- `/gnoi.packet_link_qualification.LinkQualification/Get`
- `/gnoi.packet_link_qualification.LinkQualification/Delete`

## Fault Behavior

### Exhaustible Application

When multiple faults are configured for an RPC method, they are applied sequentially and then exhausted:

```protobuf
gnoi_faults {
  rpc_method: "/gnoi.system.System/Reboot"
  faults {
    msg_id: "fault_1"
    status { code: 3 message: "First fault" }
  }
  faults {
    msg_id: "fault_2" 
    status { code: 14 message: "Second fault" }
  }
}
```

- 1st call → fault_1 (INVALID_ARGUMENT)
- 2nd call → fault_2 (UNAVAILABLE)  
- 3rd call → **normal behavior** (faults exhausted)
- 4th call → **normal behavior** (continues normally)

### Fault Types

#### Status-Only Faults
Return an error without executing the RPC:

```protobuf
faults {
  msg_id: "permission_denied"
  status {
    code: 7  # PERMISSION_DENIED
    message: "Access denied in maintenance mode"
  }
}
```

#### Message Modification Faults
Modify the request before processing:

```protobuf
faults {  
  msg_id: "modify_request"
  msg {
    # Encoded modified request message
    type_url: "type.googleapis.com/gnoi.system.RebootRequest"
    value: "..."  # Modified request proto bytes
  }
  status {
    code: 0  # OK - process with modified request
  } 
}
```

## Usage Examples

### Test Configuration File

Create a configuration file with fault injection:

```protobuf
# lemming_with_faults.textproto

interfaces {
  interface {
    name: "eth0"
    if_index: 1
  }
}

fault_config {
  # Simulate reboot failures
  gnoi_faults {
    rpc_method: "/gnoi.system.System/Reboot"
    faults {
      msg_id: "reboot_maintenance_mode"
      status {
        code: 9  # FAILED_PRECONDITION
        message: "Cannot reboot: system in maintenance mode"
      }
    }
  }
  
  # Simulate network issues for ping
  gnoi_faults {
    rpc_method: "/gnoi.system.System/Ping"
    faults {
      msg_id: "network_unreachable"
      status {
        code: 14  # UNAVAILABLE
        message: "Network unreachable" 
      }
    }
  }
}
```

### Integration Testing

Use the fault config in integration tests:

```go
func TestRebootWithFaults(t *testing.T) {
    dut := ondatra.DUT(t, "dut")  // Uses fault config
    
    // This will fail with the configured fault
    _, err := system.New(dut).Reboot(ctx, &spb.RebootRequest{
        Method: spb.RebootMethod_COLD,
    })
    
    // Verify expected fault behavior
    st := status.Convert(err)
    if st.Code() != codes.FailedPrecondition {
        t.Errorf("Expected FailedPrecondition, got %v", st.Code())
    }
}
```

### Running Lemming with Fault Config

```bash
# Start lemming with fault configuration
./lemming \
  --gnmi_addr=localhost:9339 \
  --gribi_addr=localhost:9340 \
  --config_file=configs/lemming_with_faults.textproto
```

## Architecture

### Components

- **Config Loader** (`internal/config/loader.go`): Parses and validates fault configurations
- **Fault Manager** (`internal/fault/manager.go`): Manages fault rules and lifecycle  
- **Fault Interceptor** (`fault/fault.go`): Applies faults at gRPC layer
- **Proto Definitions** (`proto/config/`): Configuration schema

### Integration Flow

1. **Startup**: Lemming loads fault config and creates fault manager
2. **Registration**: Fault manager configures interceptor with fault rules
3. **Runtime**: gRPC interceptor applies faults based on RPC method
4. **Exhaustible**: Multiple faults are used once, then pass through normally

## Testing

### Unit Tests

```bash
# Test fault manager
go test ./internal/fault/...

# Test fault interceptor  
go test ./fault/...
```

### Integration Tests

```bash  
# Run fault injection integration tests
bazel test //integration_tests/onedut_tests/fault_config:fault_config_test
```

## Configuration Validation

The system validates fault configurations at startup:

- **RPC Method Format**: Must be valid gRPC method path
- **Unique Methods**: No duplicate RPC methods in configuration
- **Required Fields**: `msg_id` and either `msg` or `status` required
- **Status Codes**: Must be valid gRPC status codes

Invalid configurations will prevent Lemming from starting with clear error messages.

## Limitations

- **Unary RPCs**: Full request/response modification supported
- **Streaming RPCs**: Only error injection supported (no message modification)
- **Performance**: Minimal overhead when no faults configured
- **Concurrency**: Thread-safe for concurrent RPC calls

## Best Practices

1. **Test Isolation**: Use separate config files for different test scenarios
2. **Clear Naming**: Use descriptive `msg_id` values for fault identification
3. **Realistic Errors**: Use appropriate gRPC status codes for fault types
4. **Documentation**: Document expected fault behavior in test cases
5. **Cleanup**: Fault configuration only applies during Lemming lifetime

## Troubleshooting

### Common Issues

**Faults Not Applied**: Check RPC method name format and case sensitivity
**Config Validation Errors**: Review proto syntax and required fields  
**Unexpected Behavior**: Verify exhaustible behavior and fault ordering

### Debug Logging

Enable debug logging to see fault application:

```bash
./lemming --logtostderr --v=2 --config_file=fault_config.textproto
```

Look for log messages like:
- `"Loaded N fault rules for RPC method: ..."`
- `"Applying configured fault for RPC ... msg_id=..."`