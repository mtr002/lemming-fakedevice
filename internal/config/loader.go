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

// Package config handles lemming device configuration loading and validation.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"

	log "github.com/golang/glog"
	configpb "github.com/openconfig/lemming/proto/config"
)

// Load loads the lemming configuration with smart merging and environment variable support.
// User config is merged with defaults for any missing sections.
func Load(configFile string) (*configpb.LemmingConfig, error) {
	var userConfig *configpb.LemmingConfig
	var err error

	// Check for environment variable override if no config file provided
	if configFile == "" {
		if envConfigFile := os.Getenv("LEMMING_CONFIG_FILE"); envConfigFile != "" {
			configFile = envConfigFile
			log.Infof("Using config file from LEMMING_CONFIG_FILE environment variable: %s", configFile)
		}
	}

	if configFile != "" {
		// Try to parse user config file (without validation)
		log.Infof("Loading configuration from file: %s", configFile)
		userConfig, err = parseFromFile(configFile)
		if err != nil {
			// Only fall back to defaults on parsing errors (malformed file)
			// Not on validation errors (incomplete but well-formed file)
			defaultConfigFile := getDefaultConfigFile()
			if defaultConfig, defaultErr := loadFromFile(defaultConfigFile); defaultErr == nil {
				log.Infof("User config parsing failed, using default config file: %s", defaultConfigFile)
				return defaultConfig, nil
			}
			log.Errorf("Failed to parse config file %s: %v", configFile, err)
			log.Info("Using complete defaults")
		}
	} else {
		// Try default config file first
		defaultConfigFile := getDefaultConfigFile()
		if defaultConfig, defaultErr := loadFromFile(defaultConfigFile); defaultErr == nil {
			log.Infof("Using default config file: %s", defaultConfigFile)
			return defaultConfig, nil
		}
		log.Info("No config file provided, using complete defaults")
	}

	// Merge user config with defaults (or use all defaults if no user config)
	config := mergeWithDefaults(userConfig)

	if err := validate(config); err != nil {
		return nil, fmt.Errorf("invalid merged configuration: %v", err)
	}

	return config, nil
}

// getDefaultConfigFile returns the default config file path with environment variable override support
func getDefaultConfigFile() string {
	if envDefaultFile := os.Getenv("LEMMING_DEFAULT_CONFIG_FILE"); envDefaultFile != "" {
		return envDefaultFile
	}
	return "configs/lemming_default.textproto"
}

// mergeWithDefaults merges user config with defaults for any missing sections
func mergeWithDefaults(userConfig *configpb.LemmingConfig) *configpb.LemmingConfig {
	config := &configpb.LemmingConfig{}

	// Use user config or defaults for each section
	if userConfig != nil && userConfig.Vendor != nil {
		config.Vendor = userConfig.Vendor
	} else {
		config.Vendor = getDefaultVendor()
	}

	if userConfig != nil && userConfig.Components != nil {
		config.Components = userConfig.Components
	} else {
		config.Components = getDefaultComponents()
	}

	if userConfig != nil && len(userConfig.Processes) > 0 {
		config.Processes = userConfig.Processes
	} else {
		config.Processes = getDefaultProcesses()
	}

	if userConfig != nil && userConfig.Timing != nil {
		config.Timing = userConfig.Timing
	} else {
		config.Timing = getDefaultTiming()
	}

	if userConfig != nil && userConfig.NetworkSim != nil {
		config.NetworkSim = userConfig.NetworkSim
	} else {
		config.NetworkSim = getDefaultNetworkSim()
	}

	return config
}

// getDefaultVendor returns default vendor configuration
func getDefaultVendor() *configpb.VendorConfig {
	return &configpb.VendorConfig{
		Name:      "OpenConfig",
		Model:     "Lemming",
		OsVersion: "1.0.0",
	}
}

// getDefaultComponents returns default component configuration
func getDefaultComponents() *configpb.ComponentConfig {
	return &configpb.ComponentConfig{
		Supervisor1Name: "Supervisor1",
		Supervisor2Name: "Supervisor2",
		ChassisName:     "chassis",
		LinecardPrefix:  "Linecard",
		FabricPrefix:    "Fabric",
		Linecard: &configpb.ComponentTypeConfig{
			Count:      8,
			StartIndex: 0,
			Step:       1,
		},
		Fabric: &configpb.ComponentTypeConfig{
			Count:      6,
			StartIndex: 0,
			Step:       1,
		},
	}
}

// getDefaultProcesses returns default process configurations
func getDefaultProcesses() []*configpb.ProcessConfig {
	return []*configpb.ProcessConfig{
		{Name: "Octa", Pid: 1001, CpuUsageUser: 1000000, CpuUsageSystem: 500000, CpuUtilization: 1, MemoryUsage: 10485760, MemoryUtilization: 2},
		{Name: "Gribi", Pid: 1002, CpuUsageUser: 1000000, CpuUsageSystem: 500000, CpuUtilization: 1, MemoryUsage: 10485760, MemoryUtilization: 2},
		{Name: "emsd", Pid: 1003, CpuUsageUser: 1000000, CpuUsageSystem: 500000, CpuUtilization: 1, MemoryUsage: 10485760, MemoryUtilization: 2},
		{Name: "kim", Pid: 1004, CpuUsageUser: 1000000, CpuUsageSystem: 500000, CpuUtilization: 1, MemoryUsage: 10485760, MemoryUtilization: 2},
		{Name: "grpc_server", Pid: 1005, CpuUsageUser: 1000000, CpuUsageSystem: 500000, CpuUtilization: 1, MemoryUsage: 10485760, MemoryUtilization: 2},
		{Name: "fibd", Pid: 1006, CpuUsageUser: 1000000, CpuUsageSystem: 500000, CpuUtilization: 1, MemoryUsage: 10485760, MemoryUtilization: 2},
		{Name: "rpd", Pid: 1007, CpuUsageUser: 1000000, CpuUsageSystem: 500000, CpuUtilization: 1, MemoryUsage: 10485760, MemoryUtilization: 2},
	}
}

// getDefaultTiming returns default timing configuration
func getDefaultTiming() *configpb.TimingConfig {
	return &configpb.TimingConfig{
		SwitchoverDurationMs: 2000,
		RebootDurationMs:     2000,
	}
}

// getDefaultNetworkSim returns default network simulation configuration
func getDefaultNetworkSim() *configpb.NetworkSimConfig {
	return &configpb.NetworkSimConfig{
		BaseLatencyMs:   50,
		LatencyJitterMs: 20,
		PacketLossRate:  0.0,
		DefaultTtl:      64,
	}
}

// parseFromFile parses configuration from a file without validation
func parseFromFile(filename string) (*configpb.LemmingConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %v", filename, err)
	}

	config := &configpb.LemmingConfig{}
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".textproto", ".pb.txt", ".pbtxt":
		// Protobuf text format
		if err := prototext.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("failed to parse protobuf text config file %s: %v", filename, err)
		}
	default:
		return nil, fmt.Errorf("unsupported config file format: %s (supported: .textproto, .pb.txt, .pbtxt)", ext)
	}

	return config, nil
}

// loadFromFile loads and validates configuration from a file, supporting protobuf text format
func loadFromFile(filename string) (*configpb.LemmingConfig, error) {
	config, err := parseFromFile(filename)
	if err != nil {
		return nil, err
	}

	if err := validate(config); err != nil {
		return nil, fmt.Errorf("invalid configuration in %s: %v", filename, err)
	}

	return config, nil
}

// validate validates the configuration for comprehensive consistency and correctness
func validate(config *configpb.LemmingConfig) error {
	// Validate required components configuration
	if config.Components == nil {
		return fmt.Errorf("components configuration is required")
	}

	if err := validateComponents(config.Components); err != nil {
		return fmt.Errorf("components validation failed: %v", err)
	}

	// Validate process PIDs are unique if any are provided
	if err := validateProcesses(config.Processes); err != nil {
		return fmt.Errorf("processes validation failed: %v", err)
	}

	// Validate network simulation parameters if provided
	if config.NetworkSim != nil {
		if err := validateNetworkSim(config.NetworkSim); err != nil {
			return fmt.Errorf("network simulation validation failed: %v", err)
		}
	}

	// Validate timing configuration if provided
	if config.Timing != nil {
		if err := validateTiming(config.Timing); err != nil {
			return fmt.Errorf("timing validation failed: %v", err)
		}
	}

	// Validate vendor configuration if provided
	if config.Vendor != nil {
		if err := validateVendor(config.Vendor); err != nil {
			return fmt.Errorf("vendor validation failed: %v", err)
		}
	}

	return nil
}

// validateComponents validates component configuration structure and names
// If components section is provided, all required component names must be specified
func validateComponents(comp *configpb.ComponentConfig) error {
	// Require critical component names when components section is provided
	if comp.ChassisName == "" {
		return fmt.Errorf("chassis name is required")
	}
	if comp.Supervisor1Name == "" {
		return fmt.Errorf("components section requires supervisor1_name to be specified")
	}
	if comp.Supervisor2Name == "" {
		return fmt.Errorf("components section requires supervisor2_name to be specified")
	}

	// Note: linecard_prefix and fabric_prefix are optional
	// Empty string means component names will be just indices (e.g., "0", "1", "2")

	// Validate supervisor names are different
	if comp.Supervisor1Name == comp.Supervisor2Name {
		return fmt.Errorf("supervisor1_name and supervisor2_name must be different")
	}

	// Validate component type configurations are provided
	if comp.Linecard == nil {
		return fmt.Errorf("components section requires linecard configuration")
	}
	if err := validateComponentType("linecard", comp.Linecard); err != nil {
		return err
	}

	if comp.Fabric == nil {
		return fmt.Errorf("components section requires fabric configuration")
	}
	if err := validateComponentType("fabric", comp.Fabric); err != nil {
		return err
	}
	return nil
}

// validateComponentType validates component type configuration parameters
func validateComponentType(typeName string, config *configpb.ComponentTypeConfig) error {
	if config.Count <= 0 {
		return fmt.Errorf("%s count must be positive, got %d", typeName, config.Count)
	}
	if config.Step <= 0 {
		return fmt.Errorf("%s step must be positive, got %d", typeName, config.Step)
	}

	// Reasonable upper bounds for component counts
	if config.Count > 64 {
		return fmt.Errorf("%s count %d exceeds reasonable maximum 64", typeName, config.Count)
	}

	return nil
}

// validateProcesses validates process configuration including PIDs, names, and resource utilization
func validateProcesses(processes []*configpb.ProcessConfig) error {
	pidSet := make(map[uint32]bool)
	nameSet := make(map[string]bool)

	for i, proc := range processes {
		if proc.Name == "" {
			return fmt.Errorf("process[%d] name is required", i)
		}

		if proc.Pid == 0 {
			return fmt.Errorf("process[%d] '%s' has invalid PID 0", i, proc.Name)
		}

		// Check for duplicate PIDs
		if pidSet[proc.Pid] {
			return fmt.Errorf("duplicate PID %d found in process configuration", proc.Pid)
		}
		pidSet[proc.Pid] = true

		// Check for duplicate process names
		if nameSet[proc.Name] {
			return fmt.Errorf("duplicate process name '%s' found in configuration", proc.Name)
		}
		nameSet[proc.Name] = true

		// Validate utilization percentages
		if proc.CpuUtilization > 100 {
			return fmt.Errorf("process[%d] '%s' cpu_utilization cannot exceed 100%%, got %d", i, proc.Name, proc.CpuUtilization)
		}
		if proc.MemoryUtilization > 100 {
			return fmt.Errorf("process[%d] '%s' memory_utilization cannot exceed 100%%, got %d", i, proc.Name, proc.MemoryUtilization)
		}
	}

	return nil
}

// validateNetworkSim validates network simulation parameters for realistic values
func validateNetworkSim(netSim *configpb.NetworkSimConfig) error {
	if netSim.PacketLossRate < 0 || netSim.PacketLossRate > 1 {
		return fmt.Errorf("packet_loss_rate must be between 0.0 and 1.0, got %f", netSim.PacketLossRate)
	}
	if netSim.BaseLatencyMs < 0 {
		return fmt.Errorf("base_latency_ms must be non-negative, got %d", netSim.BaseLatencyMs)
	}
	if netSim.LatencyJitterMs < 0 {
		return fmt.Errorf("latency_jitter_ms must be non-negative, got %d", netSim.LatencyJitterMs)
	}
	if netSim.DefaultTtl < 0 || netSim.DefaultTtl > 255 {
		return fmt.Errorf("default_ttl must be between 0 and 255, got %d", netSim.DefaultTtl)
	}
	// Validate reasonable upper bounds for network simulation
	if netSim.BaseLatencyMs > 10000 {
		return fmt.Errorf("base_latency_ms %d exceeds reasonable maximum 10000ms", netSim.BaseLatencyMs)
	}
	if netSim.LatencyJitterMs > 5000 {
		return fmt.Errorf("latency_jitter_ms %d exceeds reasonable maximum 5000ms", netSim.LatencyJitterMs)
	}
	return nil
}

// validateTiming validates timing configuration for system operations
func validateTiming(timing *configpb.TimingConfig) error {
	if timing.SwitchoverDurationMs < 0 {
		return fmt.Errorf("switchover_duration_ms must be non-negative, got %d", timing.SwitchoverDurationMs)
	}
	if timing.RebootDurationMs < 0 {
		return fmt.Errorf("reboot_duration_ms must be non-negative, got %d", timing.RebootDurationMs)
	}
	return nil
}

// validateVendor validates vendor configuration for reasonable values
func validateVendor(vendor *configpb.VendorConfig) error {
	// Vendor fields are optional, but if provided should not be excessively long
	if len(vendor.Name) > 64 {
		return fmt.Errorf("vendor name too long: %d characters (max 64)", len(vendor.Name))
	}
	if len(vendor.Model) > 64 {
		return fmt.Errorf("vendor model too long: %d characters (max 64)", len(vendor.Model))
	}
	if len(vendor.OsVersion) > 32 {
		return fmt.Errorf("vendor os_version too long: %d characters (max 32)", len(vendor.OsVersion))
	}
	return nil
}
