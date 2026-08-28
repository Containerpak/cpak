/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import (
	"time"
)

// Container is the struct that represents a container in the store and
// in the cpak context.
type Container struct {
	ID        uint      `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// CpakId is the unique identifier of the container, it is expected to be
	// unique across all the containers in the store.
	CpakId string `json:"cpak_id"`

	// ApplicationCpakId is the application the container is based on.
	ApplicationCpakId string `json:"application_cpak_id"`

	// Pid is the pid of the main spawned container process inside the namespace.
	Pid int `json:"pid"`

	// ProcessStartTime is the kernel start time of Pid. It prevents a stale
	// container record from signalling a process that later reused the PID.
	ProcessStartTime uint64 `json:"process_start_time"`

	// CreateTimestamp is the time the container was created in the store.
	CreateTimestamp time.Time `json:"create_timestamp"`

	// StatePath is the path to the state directory of the container, the
	// actual workdir for the layer mounts.
	StatePath string `json:"state_path"`

	// WritableLayerPath and WritableWorkPath may outlive the container when it
	// belongs to a persistent environment. Empty values use StatePath.
	WritableLayerPath string `json:"writable_layer_path,omitempty"`
	WritableWorkPath  string `json:"writable_work_path,omitempty"`

	// DataID selects the private home and machine identity for this container.
	// Empty values keep application-scoped storage for older records.
	DataID string `json:"data_id,omitempty"`

	// SystemBrokerSocketPath is the broker socket mounted into the container.
	SystemBrokerSocketPath string `json:"system_broker_socket_path"`

	// SystemBrokerTokenPath is a private capability file mounted read-only into the container.
	SystemBrokerTokenPath string `json:"system_broker_token_path"`

	// NestedToken is what this container presents when it asks to run one of
	// its declared dependencies. It replaces the application identifier the
	// request used to carry, which was public metadata anybody could compute,
	// so a container now proves which one it is instead of naming one.
	NestedToken string `json:"nested_token"`

	// SystemBrokerPolicyPath is the policy registered for this container.
	SystemBrokerPolicyPath string `json:"system_broker_policy_path"`

	// DesktopBusProxyPid is the PID of the policy-gated session bus proxy.
	DesktopBusProxyPid       int    `json:"desktop_bus_proxy_pid"`
	DesktopBusProxyStartTime uint64 `json:"desktop_bus_proxy_start_time"`

	// DesktopBusSocketPath is the private session bus socket mounted into the container.
	DesktopBusSocketPath string `json:"desktop_bus_socket_path"`

	// BluetoothBusProxyPid is the PID of the BlueZ-only system bus proxy.
	BluetoothBusProxyPid       int    `json:"bluetooth_bus_proxy_pid"`
	BluetoothBusProxyStartTime uint64 `json:"bluetooth_bus_proxy_start_time"`

	// BluetoothBusSocketPath is the private BlueZ bus mounted into the container.
	BluetoothBusSocketPath string `json:"bluetooth_bus_socket_path"`

	// X11BridgePid is the PID of the per-container nested X server.
	X11BridgePid       int    `json:"x11_bridge_pid"`
	X11BridgeStartTime uint64 `json:"x11_bridge_start_time"`

	// X11Display is the private display name used inside the container.
	X11Display string `json:"x11_display"`

	// X11SocketPath is the nested server socket mounted into the container.
	X11SocketPath string `json:"x11_socket_path"`

	// X11SocketTarget is the conventional display socket path inside the container.
	X11SocketTarget string `json:"x11_socket_target"`

	// X11AuthorityPath authenticates the container to its nested server.
	X11AuthorityPath string `json:"x11_authority_path"`

	// ExecSocketPath is the host path used to submit commands to the container init.
	ExecSocketPath string `json:"exec_socket_path"`

	// GrantSocketPath is the host path used to mount user-selected files.
	GrantSocketPath string `json:"grant_socket_path"`

	// PolicyHash identifies the effective permissions used to create the namespaces.
	PolicyHash string `json:"policy_hash"`

	// CgroupPath is the delegated cgroup used for optional resource limits.
	CgroupPath string `json:"cgroup_path"`

	// FVSLayerMountId identifies the read-only layer view held by fvs2d.
	FVSLayerMountId string `json:"fvs_layer_mount_id"`

	// FVSLayerMountPath is the lower directory passed to OverlayFS.
	FVSLayerMountPath string `json:"fvs_layer_mount_path"`

	// FVSManagerSocketPath identifies the storage service that owns the layer view.
	FVSManagerSocketPath string `json:"fvs_manager_socket_path"`

	// Instance identifies the optional application instance.
	Instance string `json:"instance"`

	// LogPath is the host path where command output is retained.
	LogPath string `json:"log_path"`
}
