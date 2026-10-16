//go:build !linux

package service

func linuxSystemdMasterKeyProviderSupported() bool { return false }

func loadLinuxSystemdMasterKeyCredential() (masterKeyMaterial, error) {
	return masterKeyMaterial{}, errMasterKeyProviderRejected
}

func readLinuxMasterKeyBinding(string, string, int) ([]byte, error) {
	return nil, errMasterKeyProviderRejected
}

func createLinuxMasterKeyBinding(string, string, []byte) error {
	return errMasterKeyProviderRejected
}
