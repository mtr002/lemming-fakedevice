# Config-Based Fault Injection

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
- 3rd call → **normal behavior** (continues normally)

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
}

```

### Running Lemming with Fault Config

```bash
# Start lemming with fault configuration
./lemming \
  --gnmi_addr=localhost:9339 \
  --gribi_addr=localhost:9340 \
  --config_file=path_to_fault_config
```
