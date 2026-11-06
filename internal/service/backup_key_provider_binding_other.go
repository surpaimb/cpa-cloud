//go:build !linux

package service

import "cpacloud.local/server/internal/keyprovider"

func backupKeyProviderInstanceBinding(Config) (keyprovider.InstanceBinding, func([]byte) error, error) {
	return keyprovider.InstanceBinding{}, nil, nil
}
