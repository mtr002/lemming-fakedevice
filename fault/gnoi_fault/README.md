# gNOI Fault Injection for Lemming

Fault package provides fault injection capabilities for gNOI services in Lemming, allowing you to simulate failures and test error handling in your applications.

## Overview

The fault injection system uses gRPC interceptors to capture and potentially modify gNOI requests and responses. This allows you to:

- Simulate hardware failures
- Test retry logic
- Validate error handling
- Create complex failure scenarios
- Perform deterministic negative testing

## Usage

### Enable Fault Injection in Lemming

```bash
# Start lemming with fault injection enabled
lemming --enable_fault=true --fault_addr=":9399"
```

## Available gNOI Methods

The following gNOI methods are available for fault injection:

### System Service Methods
- `GNOIReboot` - Intercept device reboots
- `GNOICancelReboot` - Intercept reboot cancellations
- `GNOISwitchControlProcessor` - Intercept supervisor switchovers
- `GNOIKillProcess` - Intercept process kills
- `GNOITime` - Intercept time requests
- `GNOIPing` - Intercept ping operations

### Link Qualification Service Methods
- `GNOILinkQualificationCapabilities` - Intercept capability queries
- `GNOILinkQualificationCreate` - Intercept link test creation
- `GNOILinkQualificationGet` - Intercept link test retrieval
- `GNOILinkQualificationDelete` - Intercept link test deletion
- `GNOILinkQualificationList` - Intercept link test listing

## Fault Injection Patterns

### 1. Complete Bypass (Return Error Immediately)

```go
interceptor.SetBypass(func(req *spb.RebootRequest) (*spb.RebootResponse, error) {
    return nil, status.Errorf(codes.Internal, "simulated hardware failure")
})
```

### 2. Conditional Failures

```go
interceptor.SetBypass(func(req *spb.RebootRequest) (*spb.RebootResponse, error) {
    if req.GetMethod() == spb.RebootMethod_POWERUP {
        return nil, status.Errorf(codes.Unimplemented, "powerup not supported")
    }
    return nil, nil // Allow other methods to proceed normally
})
```

### 3. Request Modification

```go
interceptor.SetReqMod(func(req *spb.RebootRequest) *spb.RebootRequest {
    modifiedReq := proto.Clone(req).(*spb.RebootRequest)
    modifiedReq.Delay = 10000000000 // Force 10 second delay
    return modifiedReq
})
```

### 4. Response Modification

```go
interceptor.SetRespMod(func(resp *spb.RebootResponse, err error) (*spb.RebootResponse, error) {
    if err != nil {
        return resp, err
    }
    // Modify successful response
    return resp, nil
})
```
