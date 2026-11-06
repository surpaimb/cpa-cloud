package service

// SystemdRecoveryIdentity contains only non-secret expectations for offline
// recovery. No initializer, database or business App is opened by this bridge.
// Provenance: docs/linux-systemd-offline-recovery-cli-contract.md.
type SystemdRecoveryIdentity struct {
	ProviderID, InstanceID, MasterID, WrappingID string
	MasterVersion, WrappingVersion               uint64
}
