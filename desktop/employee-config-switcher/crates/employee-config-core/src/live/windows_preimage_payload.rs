//! Unauthenticated Windows pre-image payload byte relationships only.
//!
//! Independently authored from the project's frozen PR130 W1/W2 contract.
//! This private, unwired source candidate grants no trust or restore authority.
//! Expected B is a separate input; its trusted production source is UNKNOWN.
//! No I/O, provider, AEAD, encryption, nonce reservation or plaintext return.

use super::windows_preimage_frame::UntrustedFrame;
use sha2::{Digest, Sha256};

const MAX_BINDING_BYTES: usize = 128 * 1024;
const MAX_ORIGINAL_BYTES: u64 = 4 * 1024 * 1024;
const MAX_PAYLOAD_BYTES: usize = 4 * 1024 * 1024 + MAX_BINDING_BYTES + 4;
const PROFILE_HEADER_BYTES: usize = 158;
const MAX_FRAME_BYTES: usize = MAX_PAYLOAD_BYTES + 204 + 16;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PayloadCheckError {
    InputOutOfBounds,
    HeaderMismatch,
    LengthMismatch,
    BindingTruncated,
    BindingMismatch,
    BindingCommitmentMismatch,
    OriginalLengthMismatch,
    OriginalDigestMismatch,
    ArithmeticOverflow,
}

/// Check supplied byte relationships without authenticating or returning P.
///
/// The caller supplies expected B independently. This does not establish its
/// origin, parse B's internal fields, check a tag, consume authorization or
/// admit any restore. Even Ok(()) leaves the frame and candidate untrusted.
fn check_payload_bytes(
    frame: &UntrustedFrame<'_>,
    candidate_payload: &[u8],
    expected_binding: &[u8],
    expected_original_length: u64,
    expected_original_sha256: &[u8; 32],
) -> Result<(), PayloadCheckError> {
    if !(4..=MAX_PAYLOAD_BYTES).contains(&candidate_payload.len())
        || !(4..=MAX_PAYLOAD_BYTES).contains(&frame.ciphertext.len())
        || expected_binding.len() > MAX_BINDING_BYTES
        || expected_original_length > MAX_ORIGINAL_BYTES
    {
        return Err(PayloadCheckError::InputOutOfBounds);
    }
    let original_length = usize::try_from(expected_original_length)
        .map_err(|_| PayloadCheckError::ArithmeticOverflow)?;
    let declared_length = usize::try_from(frame.header.payload_length)
        .map_err(|_| PayloadCheckError::ArithmeticOverflow)?;
    if !(4..=MAX_PAYLOAD_BYTES).contains(&declared_length) {
        return Err(PayloadCheckError::InputOutOfBounds);
    }
    if frame.header_bytes.len() != PROFILE_HEADER_BYTES {
        return Err(PayloadCheckError::HeaderMismatch);
    }
    let frame_length = frame
        .header_bytes
        .len()
        .checked_add(frame.ciphertext.len())
        .and_then(|length| length.checked_add(frame.tag.len()))
        .ok_or(PayloadCheckError::ArithmeticOverflow)?;
    if frame_length > MAX_FRAME_BYTES {
        return Err(PayloadCheckError::InputOutOfBounds);
    }

    let raw_length = read_u32_le(
        frame
            .header_bytes
            .get(154..158)
            .ok_or(PayloadCheckError::HeaderMismatch)?,
    )?;
    if raw_length != frame.header.payload_length
        || frame.ciphertext.len() != declared_length
        || candidate_payload.len() != declared_length
    {
        return Err(PayloadCheckError::LengthMismatch);
    }
    let raw_commitment = frame
        .header_bytes
        .get(122..154)
        .ok_or(PayloadCheckError::HeaderMismatch)?;
    if raw_commitment != frame.header.binding_commitment.as_slice() {
        return Err(PayloadCheckError::HeaderMismatch);
    }

    let binding_length = usize::try_from(read_u32_le(
        candidate_payload
            .get(..4)
            .ok_or(PayloadCheckError::LengthMismatch)?,
    )?)
    .map_err(|_| PayloadCheckError::ArithmeticOverflow)?;
    if binding_length > MAX_BINDING_BYTES {
        return Err(PayloadCheckError::InputOutOfBounds);
    }
    let binding_end = 4usize
        .checked_add(binding_length)
        .ok_or(PayloadCheckError::ArithmeticOverflow)?;
    let binding = candidate_payload
        .get(4..binding_end)
        .ok_or(PayloadCheckError::BindingTruncated)?;
    if binding != expected_binding {
        return Err(PayloadCheckError::BindingMismatch);
    }
    let expected_payload_length = 4usize
        .checked_add(expected_binding.len())
        .and_then(|length| length.checked_add(original_length))
        .ok_or(PayloadCheckError::ArithmeticOverflow)?;
    let original = candidate_payload
        .get(binding_end..)
        .ok_or(PayloadCheckError::BindingTruncated)?;
    if candidate_payload.len() != expected_payload_length || original.len() != original_length {
        return Err(PayloadCheckError::OriginalLengthMismatch);
    }

    let binding_sha256: [u8; 32] = Sha256::digest(binding).into();
    if &binding_sha256 != frame.header.binding_commitment {
        return Err(PayloadCheckError::BindingCommitmentMismatch);
    }
    let original_sha256: [u8; 32] = Sha256::digest(original).into();
    if &original_sha256 != expected_original_sha256 {
        return Err(PayloadCheckError::OriginalDigestMismatch);
    }
    Ok(())
}

fn read_u32_le(input: &[u8]) -> Result<u32, PayloadCheckError> {
    let bytes = input
        .try_into()
        .map_err(|_| PayloadCheckError::LengthMismatch)?;
    Ok(u32::from_le_bytes(bytes))
}
#[cfg(test)]
mod tests {
    use super::super::windows_preimage_frame::parse_untrusted_frame;
    use super::*;

    // Separate pre-encryption public literals, not a product encoder or Go code.
    // Input-file SHA256: f2513d2a444c5f1129386bdad4a59a3183313801fe8a987dd3074311f8ad0b99.
    // Native SID/SD/ID validity is not asserted. No DEK is embedded or used.
    const HEADER_HEX: &str = concat!(
        "4350412d57494e2d505245494d473400040000000100000001000000101112131415161718191a1b",
        "1c1d1e1f1200000077696e646f77732d64706170692d7573657201000000d843dd05c96b05b41aaa",
        "65047d3420b1a871785e83d7d8b74d04d1c37cad81ac0100000002000000a0a1a2a3a4a5a6a70000",
        "000125e09465a27334ec7fb7830a2b53b3e3fac75198a87607cd0d3632cec11cb73592010000",
    );
    const BINDING_HEX: &str = concat!(
        "0d0000004350412d57494e2d42494e4434101112131415161718191a1b1c1d1e1f1300000077696e",
        "646f77732d7265706c6163656d656e74010000001200000077696e646f77732d64706170692d7573",
        "657201000000d843dd05c96b05b41aaa65047d3420b1a871785e83d7d8b74d04d1c37cad81ac0100",
        "0000000000000102030405060708090a0b0c0d0e0f1001000000000000001112131415161718191a",
        "1b1c1d1e1f200100000002000000449d753a34263d2b0308c9d30a4b1c7b8d8010746515da429db0",
        "a1786be6d83102000000620001000000000000003132333435363738393a3b3c3d3e3f4003000000",
        "00000000ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015adcb8379ac",
        "2098aa165029e3938a51da0bcecfc008fd6795f401178647f96c5b34800000000c00000001010000",
        "00000005120000003c00000001000490140000000000000000000000200000000101000000000005",
        "1200000002001c000100000000001400ff011f00010100000000000512000000049000",
    );
    const PAYLOAD_HEX: &str = concat!(
        "8b0100000d0000004350412d57494e2d42494e4434101112131415161718191a1b1c1d1e1f130000",
        "0077696e646f77732d7265706c6163656d656e74010000001200000077696e646f77732d64706170",
        "692d7573657201000000d843dd05c96b05b41aaa65047d3420b1a871785e83d7d8b74d04d1c37cad",
        "81ac01000000000000000102030405060708090a0b0c0d0e0f100100000000000000111213141516",
        "1718191a1b1c1d1e1f200100000002000000449d753a34263d2b0308c9d30a4b1c7b8d8010746515",
        "da429db0a1786be6d83102000000620001000000000000003132333435363738393a3b3c3d3e3f40",
        "0300000000000000ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
        "cb8379ac2098aa165029e3938a51da0bcecfc008fd6795f401178647f96c5b34800000000c000000",
        "0101000000000005120000003c000000010004901400000000000000000000002000000001010000",
        "000000051200000002001c000100000000001400ff011f0001010000000000051200000004900061",
        "6263",
    );
    const ORIGINAL_SHA256_HEX: &str =
        "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";

    fn expected_binding() -> Vec<u8> {
        hex::decode(BINDING_HEX).expect("fixed public B literal")
    }

    fn candidate_payload() -> Vec<u8> {
        hex::decode(PAYLOAD_HEX).expect("fixed public P literal")
    }

    fn original_digest() -> [u8; 32] {
        hex::decode(ORIGINAL_SHA256_HEX)
            .expect("fixed public original digest")
            .try_into()
            .expect("SHA256 width")
    }

    // Only H is retained. Ciphertext and tag are deliberately dummy zeroes.
    // This helper never encrypts, decrypts, authenticates or derives expected B.
    fn dummy_frame(payload_length: usize) -> Vec<u8> {
        let mut bytes = hex::decode(HEADER_HEX).expect("fixed public H literal");
        let length = u32::try_from(payload_length).expect("bounded test length");
        bytes[154..158].copy_from_slice(&length.to_le_bytes());
        bytes.resize(158 + payload_length + 16, 0);
        bytes
    }

    #[test]
    fn frozen_public_p402_b395_original_three_bytes_match_only() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy untrusted framing");
        let binding = expected_binding();
        let payload = candidate_payload();
        assert_eq!(binding.len(), 395);
        assert_eq!(payload.len(), 402);
        assert_eq!(&payload[399..], b"abc");
        assert_eq!(
            check_payload_bytes(&frame, &payload, &binding, 3, &original_digest()),
            Ok(())
        );
    }

    #[test]
    fn rejects_same_length_independent_expected_binding_mismatch() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        let mut binding = expected_binding();
        binding[200] ^= 1;
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload(),
                &binding,
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::BindingMismatch)
        );
    }

    #[test]
    fn rejects_same_length_candidate_binding_mismatch() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        let mut payload = candidate_payload();
        payload[204] ^= 1;
        assert_eq!(
            check_payload_bytes(&frame, &payload, &expected_binding(), 3, &original_digest()),
            Err(PayloadCheckError::BindingMismatch)
        );
    }

    #[test]
    fn rejects_wrong_independent_original_digest() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        let mut digest = original_digest();
        digest[0] ^= 1;
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload(),
                &expected_binding(),
                3,
                &digest
            ),
            Err(PayloadCheckError::OriginalDigestMismatch)
        );
    }

    #[test]
    fn rejects_wrong_independent_original_length() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        for length in [2, 4] {
            assert_eq!(
                check_payload_bytes(
                    &frame,
                    &candidate_payload(),
                    &expected_binding(),
                    length,
                    &original_digest()
                ),
                Err(PayloadCheckError::OriginalLengthMismatch)
            );
        }
    }

    #[test]
    fn rejects_changed_original_of_the_same_length() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        let mut payload = candidate_payload();
        payload[401] ^= 1;
        assert_eq!(
            check_payload_bytes(&frame, &payload, &expected_binding(), 3, &original_digest()),
            Err(PayloadCheckError::OriginalDigestMismatch)
        );
    }

    #[test]
    fn rejects_every_proper_payload_prefix() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        let payload = candidate_payload();
        let binding = expected_binding();
        for end in 0..payload.len() {
            assert!(
                check_payload_bytes(&frame, &payload[..end], &binding, 3, &original_digest())
                    .is_err()
            );
        }
    }

    #[test]
    fn rejects_truncated_binding_even_with_consistent_header_lengths() {
        let bytes = dummy_frame(398);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload()[..398],
                &expected_binding(),
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::BindingTruncated)
        );
    }

    #[test]
    fn rejects_truncated_original_even_with_consistent_header_lengths() {
        let bytes = dummy_frame(401);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload()[..401],
                &expected_binding(),
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::OriginalLengthMismatch)
        );
    }

    #[test]
    fn rejects_trailing_byte_even_with_consistent_header_lengths() {
        let mut payload = candidate_payload();
        payload.push(0);
        let bytes = dummy_frame(payload.len());
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        assert_eq!(
            check_payload_bytes(&frame, &payload, &expected_binding(), 3, &original_digest()),
            Err(PayloadCheckError::OriginalLengthMismatch)
        );
    }

    #[test]
    fn rejects_forged_binding_prefix_within_the_size_bound() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        for length in [394u32, 396] {
            let mut payload = candidate_payload();
            payload[..4].copy_from_slice(&length.to_le_bytes());
            assert_eq!(
                check_payload_bytes(&frame, &payload, &expected_binding(), 3, &original_digest()),
                Err(PayloadCheckError::BindingMismatch)
            );
        }
    }

    #[test]
    fn rejects_oversize_and_u32_max_binding_prefix_before_slicing() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        for length in [131_073u32, u32::MAX] {
            let mut payload = candidate_payload();
            payload[..4].copy_from_slice(&length.to_le_bytes());
            assert_eq!(
                check_payload_bytes(&frame, &payload, &expected_binding(), 3, &original_digest()),
                Err(PayloadCheckError::InputOutOfBounds)
            );
        }
    }

    #[test]
    fn rejects_oversize_expected_binding_before_hashing() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload(),
                &vec![0; 131_073],
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::InputOutOfBounds)
        );
    }

    #[test]
    fn rejects_oversize_and_u64_max_expected_original_before_conversion() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        for length in [4_194_305u64, u64::MAX] {
            assert_eq!(
                check_payload_bytes(
                    &frame,
                    &candidate_payload(),
                    &expected_binding(),
                    length,
                    &original_digest()
                ),
                Err(PayloadCheckError::InputOutOfBounds)
            );
        }
    }

    #[test]
    fn rejects_oversize_candidate_before_hashing() {
        let bytes = dummy_frame(402);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        assert_eq!(
            check_payload_bytes(
                &frame,
                &vec![0; 4_325_381],
                &expected_binding(),
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::InputOutOfBounds)
        );
    }

    #[test]
    fn rejects_oversize_ciphertext_view_before_hashing() {
        let bytes = dummy_frame(402);
        let mut frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        let ciphertext = vec![0; 4_325_381];
        frame.ciphertext = &ciphertext;
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload(),
                &expected_binding(),
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::InputOutOfBounds)
        );
    }

    #[test]
    fn rejects_out_of_bounds_declared_payload_view() {
        let bytes = dummy_frame(402);
        for length in [0u32, 3, 4_325_381, u32::MAX] {
            let mut frame = parse_untrusted_frame(&bytes).expect("dummy framing");
            frame.header.payload_length = length;
            assert_eq!(
                check_payload_bytes(
                    &frame,
                    &candidate_payload(),
                    &expected_binding(),
                    3,
                    &original_digest()
                ),
                Err(PayloadCheckError::InputOutOfBounds)
            );
        }
    }

    #[test]
    fn rejects_truncated_or_extended_raw_header_views() {
        let bytes = dummy_frame(402);
        for length in [0, 157, 159] {
            let mut frame = parse_untrusted_frame(&bytes).expect("dummy framing");
            frame.header_bytes = &bytes[..length];
            assert_eq!(
                check_payload_bytes(
                    &frame,
                    &candidate_payload(),
                    &expected_binding(),
                    3,
                    &original_digest()
                ),
                Err(PayloadCheckError::HeaderMismatch)
            );
        }
    }

    #[test]
    fn rejects_each_redundant_payload_length_conflict() {
        let bytes = dummy_frame(402);
        let payload = candidate_payload();
        let binding = expected_binding();
        let mut field_conflict = parse_untrusted_frame(&bytes).expect("dummy framing");
        field_conflict.header.payload_length = 403;
        assert_eq!(
            check_payload_bytes(&field_conflict, &payload, &binding, 3, &original_digest()),
            Err(PayloadCheckError::LengthMismatch)
        );
        let mut raw_header = bytes[..158].to_vec();
        raw_header[154..158].copy_from_slice(&403u32.to_le_bytes());
        let mut raw_conflict = parse_untrusted_frame(&bytes).expect("dummy framing");
        raw_conflict.header_bytes = &raw_header;
        assert_eq!(
            check_payload_bytes(&raw_conflict, &payload, &binding, 3, &original_digest()),
            Err(PayloadCheckError::LengthMismatch)
        );
        let mut ciphertext_conflict = parse_untrusted_frame(&bytes).expect("dummy framing");
        ciphertext_conflict.ciphertext = &bytes[158..559];
        assert_eq!(
            check_payload_bytes(
                &ciphertext_conflict,
                &payload,
                &binding,
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::LengthMismatch)
        );
    }

    #[test]
    fn rejects_raw_and_parsed_binding_commitment_conflicts() {
        let bytes = dummy_frame(402);
        let other = [0u8; 32];
        let mut field_conflict = parse_untrusted_frame(&bytes).expect("dummy framing");
        field_conflict.header.binding_commitment = &other;
        assert_eq!(
            check_payload_bytes(
                &field_conflict,
                &candidate_payload(),
                &expected_binding(),
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::HeaderMismatch)
        );
        let mut raw_header = bytes[..158].to_vec();
        raw_header[122] ^= 1;
        let mut raw_conflict = parse_untrusted_frame(&bytes).expect("dummy framing");
        raw_conflict.header_bytes = &raw_header;
        assert_eq!(
            check_payload_bytes(
                &raw_conflict,
                &candidate_payload(),
                &expected_binding(),
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::HeaderMismatch)
        );
    }

    #[test]
    fn rejects_consistent_header_commitment_that_does_not_hash_binding() {
        let mut bytes = dummy_frame(402);
        bytes[122] ^= 1;
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload(),
                &expected_binding(),
                3,
                &original_digest()
            ),
            Err(PayloadCheckError::BindingCommitmentMismatch)
        );
    }

    #[test]
    fn accepts_zero_original_length_with_its_independent_digest() {
        let bytes = dummy_frame(399);
        let frame = parse_untrusted_frame(&bytes).expect("dummy framing");
        let empty_sha: [u8; 32] =
            hex::decode("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
                .expect("fixed empty SHA256")
                .try_into()
                .expect("SHA256 width");
        assert_eq!(
            check_payload_bytes(
                &frame,
                &candidate_payload()[..399],
                &expected_binding(),
                0,
                &empty_sha
            ),
            Ok(())
        );
    }

    #[test]
    fn accepts_exact_binding_and_original_upper_bounds_as_bytes_only() {
        // Expected inputs are constructed independently before the candidate P.
        let binding = vec![0x42; 131_072];
        let original = vec![0x61; 4_194_304];
        let digest: [u8; 32] = Sha256::digest(&original).into();
        let mut payload = 131_072u32.to_le_bytes().to_vec();
        payload.extend_from_slice(&binding);
        payload.extend_from_slice(&original);
        assert_eq!(payload.len(), 4_325_380);
        let mut bytes = dummy_frame(payload.len());
        bytes[122..154].copy_from_slice(&Sha256::digest(&binding));
        let frame = parse_untrusted_frame(&bytes).expect("maximum dummy framing");
        assert_eq!(
            check_payload_bytes(&frame, &payload, &binding, 4_194_304, &digest),
            Ok(())
        );
    }

    #[test]
    fn changed_tag_still_passes_only_unauthenticated_byte_relationships() {
        let bytes = dummy_frame(402);
        let mut changed = bytes.clone();
        changed[575] ^= 1;
        let first = parse_untrusted_frame(&bytes).expect("dummy framing");
        let second = parse_untrusted_frame(&changed).expect("changed dummy framing");
        assert_ne!(first.tag, second.tag);
        let payload = candidate_payload();
        let binding = expected_binding();
        assert_eq!(
            check_payload_bytes(&first, &payload, &binding, 3, &original_digest()),
            Ok(())
        );
        assert_eq!(
            check_payload_bytes(&second, &payload, &binding, 3, &original_digest()),
            Ok(())
        );
        // Neither result authenticates this dummy ciphertext/tag or releases P.
    }
}
