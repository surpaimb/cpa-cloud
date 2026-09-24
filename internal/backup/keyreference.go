package backup

// Independently implemented for docs/adr/0004-portable-backup-recovery-material.md.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

// InspectKeyReference performs bounded structural parsing of a v2 package and
// returns its unauthenticated key reference. Callers must authenticate the
// package with VerifyWithKeyMaterial or RestoreWithKeyMaterial before trusting
// any other content.
func InspectKeyReference(ctx context.Context, input string) (KeyReference, error) {
	if ctx == nil {
		return KeyReference{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return KeyReference{}, err
	}
	input, err := cleanExplicitFilePath(input)
	if err != nil {
		return KeyReference{}, fmt.Errorf("input path: %w", err)
	}
	encoded, err := readRegularFile(input, maxPackageBytes)
	if err != nil {
		return KeyReference{}, fmt.Errorf("read backup package: %w", err)
	}
	defer wipe(encoded)
	if len(encoded) < keyMaterialFixedHeader+tagSize || !bytes.Equal(encoded[:16], packageMagic[:]) || binary.BigEndian.Uint16(encoded[16:18]) != keyMaterialFormatVersion {
		return KeyReference{}, errors.New("backup package is not a supported key-material package")
	}
	fixed := encoded[:keyMaterialFixedHeader]
	headerSize := int(binary.BigEndian.Uint16(fixed[18:20]))
	idLength := int(binary.BigEndian.Uint16(fixed[22:24]))
	kindLength := int(binary.BigEndian.Uint16(fixed[24:26]))
	if binary.BigEndian.Uint16(fixed[20:22]) != keyMaterialKDFHKDFSHA256 || binary.BigEndian.Uint16(fixed[26:28]) != 0 || headerSize != keyMaterialFixedHeader+idLength+kindLength || idLength < 1 || idLength > maxProviderIDBytes || kindLength < 1 || kindLength > maxProviderKindBytes || headerSize > len(encoded)-tagSize {
		return KeyReference{}, errKeyMaterialAuthentication
	}
	ciphertextLength := binary.BigEndian.Uint64(fixed[36:44])
	if ciphertextLength < tagSize || ciphertextLength > maxPackageBytes || ciphertextLength != uint64(len(encoded)-headerSize) {
		return KeyReference{}, errKeyMaterialAuthentication
	}
	reference := KeyReference{ID: string(encoded[72 : 72+idLength]), Kind: string(encoded[72+idLength : headerSize]), Version: binary.BigEndian.Uint64(fixed[28:36])}
	if !validProviderIdentifier(reference.ID, maxProviderIDBytes) || reference.Kind != windowsDPAPIUserKind || reference.Version == 0 || reference.Version > maxPublicVersion {
		return KeyReference{}, errKeyMaterialAuthentication
	}
	return reference, nil
}
