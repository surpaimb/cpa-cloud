//go:build !linux

package service

import "cpacloud.local/server/internal/keyprovider"

func backupKeyProviderInstanceBinding(Config) (keyprovider.InstanceBinding, error) {
	return keyprovider.InstanceBinding{}, nil
}
