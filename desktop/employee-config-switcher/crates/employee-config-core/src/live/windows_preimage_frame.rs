//! Unauthenticated, bounded Windows pre-image v4 framing only.
//!
//! Independently authored from this project's PR130 W1/W2 contract,
//! ff6093ff5d4ef81a050725de86e4761612d74f4c, lines 562-657.
//! No archived implementation, external codec or producer is used.
//! No I/O, key opening, AEAD, journal, nonce reservation or native admission.
//! Every returned field remains untrusted, including K, B's commitment and tag.

use super::recovery_key::ReplacementTransactionId;

const MAGIC: &[u8; 16] = b"CPA-WIN-PREIMG4\0";
const VERSION: u32 = 4;
const WINDOWS_PLATFORM: u32 = 1;
const AES_256_GCM: u32 = 1;
const PROVIDER_KIND: &[u8] = b"windows-dpapi-user";
const PROVIDER_VERSION: u32 = 1;
const MAX_PROVIDER_KIND_BYTES: usize = 64;
const PROFILE_HEADER_BYTES: usize = 158;
const MAX_HEADER_BYTES: usize = 204;
const MIN_PAYLOAD_BYTES: usize = 4;
const MAX_PAYLOAD_BYTES: usize = 4 * 1024 * 1024 + 128 * 1024 + 4;
const TAG_BYTES: usize = 16;
const MAX_FRAME_BYTES: usize = MAX_PAYLOAD_BYTES + MAX_HEADER_BYTES + TAG_BYTES;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum FrameParseError {
    FrameTooLarge,
    Truncated,
    BadMagic,
    UnsupportedVersion,
    UnsupportedPlatform,
    UnsupportedAlgorithm,
    InvalidProvider,
    InvalidTransaction,
    InvalidCount,
    InvalidOrdinal,
    NonceOrdinalMismatch,
    PayloadLengthOutOfBounds,
    InvalidHeaderLength,
    ArithmeticOverflow,
    TrailingBytes,
}

// These views confer no authentication, freshness, restore or apply authority.
// They deliberately have no serialization or input-dumping Debug implementation.
pub(crate) struct UntrustedHeader<'a> {
    pub(crate) transaction_id: ReplacementTransactionId,
    pub(crate) provider_kind: &'a [u8],
    pub(crate) provider_version: u32,
    pub(crate) key_reference_commitment: &'a [u8; 32],
    pub(crate) ordinal: u32,
    pub(crate) count: u32,
    pub(crate) nonce: &'a [u8; 12],
    pub(crate) binding_commitment: &'a [u8; 32],
    pub(crate) payload_length: u32,
}

pub(crate) struct UntrustedFrame<'a> {
    pub(crate) header: UntrustedHeader<'a>,
    pub(crate) header_bytes: &'a [u8],
    pub(crate) ciphertext: &'a [u8],
    pub(crate) tag: &'a [u8; TAG_BYTES],
}

/// Parse public framing from an already supplied byte slice without allocation.
///
/// Payload length is |P| = |ciphertext| and excludes the 16-byte tag. The
/// four-byte minimum is only the necessary LP(B) prefix bound; neither B's
/// contents nor any decrypted payload structure is inspected here.
/// Callers remain responsible for bounded acquisition; this function opens
/// nothing and cannot authenticate an input or reserve/consume an ordinal.
pub(crate) fn parse_untrusted_frame(input: &[u8]) -> Result<UntrustedFrame<'_>, FrameParseError> {
    if input.len() > MAX_FRAME_BYTES {
        return Err(FrameParseError::FrameTooLarge);
    }
    let mut cursor = Cursor { input, position: 0 };

    if cursor.array::<16>()? != MAGIC {
        return Err(FrameParseError::BadMagic);
    }
    if cursor.u32_le()? != VERSION {
        return Err(FrameParseError::UnsupportedVersion);
    }
    if cursor.u32_le()? != WINDOWS_PLATFORM {
        return Err(FrameParseError::UnsupportedPlatform);
    }
    if cursor.u32_le()? != AES_256_GCM {
        return Err(FrameParseError::UnsupportedAlgorithm);
    }
    let transaction_id = ReplacementTransactionId::from_bytes(*cursor.array::<16>()?)
        .map_err(|_| FrameParseError::InvalidTransaction)?;
    let provider_length =
        usize::try_from(cursor.u32_le()?).map_err(|_| FrameParseError::ArithmeticOverflow)?;
    if provider_length > MAX_PROVIDER_KIND_BYTES || provider_length != PROVIDER_KIND.len() {
        return Err(FrameParseError::InvalidProvider);
    }
    let provider_kind = cursor.take(provider_length)?;
    if provider_kind != PROVIDER_KIND {
        return Err(FrameParseError::InvalidProvider);
    }
    let provider_version = cursor.u32_le()?;
    if provider_version != PROVIDER_VERSION {
        return Err(FrameParseError::InvalidProvider);
    }
    let key_reference_commitment = cursor.array::<32>()?;
    let ordinal = cursor.u32_le()?;
    let count = cursor.u32_le()?;
    if !(1..=16).contains(&count) {
        return Err(FrameParseError::InvalidCount);
    }
    if ordinal >= count {
        return Err(FrameParseError::InvalidOrdinal);
    }
    let nonce = cursor.array::<12>()?;
    let counter = u32::from_be_bytes([nonce[8], nonce[9], nonce[10], nonce[11]]);
    if counter != ordinal {
        return Err(FrameParseError::NonceOrdinalMismatch);
    }
    let binding_commitment = cursor.array::<32>()?;
    let payload_length = cursor.u32_le()?;
    let payload_bytes =
        usize::try_from(payload_length).map_err(|_| FrameParseError::ArithmeticOverflow)?;
    if !(MIN_PAYLOAD_BYTES..=MAX_PAYLOAD_BYTES).contains(&payload_bytes) {
        return Err(FrameParseError::PayloadLengthOutOfBounds);
    }
    let header_end = cursor.position;
    if header_end != PROFILE_HEADER_BYTES || header_end > MAX_HEADER_BYTES {
        return Err(FrameParseError::InvalidHeaderLength);
    }
    let header_bytes = input.get(..header_end).ok_or(FrameParseError::Truncated)?;
    let ciphertext = cursor.take(payload_bytes)?;
    let tag = cursor.array::<TAG_BYTES>()?;
    if cursor.position != input.len() {
        return Err(FrameParseError::TrailingBytes);
    }

    Ok(UntrustedFrame {
        header: UntrustedHeader {
            transaction_id,
            provider_kind,
            provider_version,
            key_reference_commitment,
            ordinal,
            count,
            nonce,
            binding_commitment,
            payload_length,
        },
        header_bytes,
        ciphertext,
        tag,
    })
}

struct Cursor<'a> {
    input: &'a [u8],
    position: usize,
}

impl<'a> Cursor<'a> {
    fn take(&mut self, length: usize) -> Result<&'a [u8], FrameParseError> {
        let end = self
            .position
            .checked_add(length)
            .ok_or(FrameParseError::ArithmeticOverflow)?;
        let bytes = self
            .input
            .get(self.position..end)
            .ok_or(FrameParseError::Truncated)?;
        self.position = end;
        Ok(bytes)
    }

    fn array<const N: usize>(&mut self) -> Result<&'a [u8; N], FrameParseError> {
        self.take(N)?
            .try_into()
            .map_err(|_| FrameParseError::Truncated)
    }

    fn u32_le(&mut self) -> Result<u32, FrameParseError> {
        Ok(u32::from_le_bytes(*self.array::<4>()?))
    }
}

#[cfg(test)]
mod tests {
    use super::{parse_untrusted_frame, FrameParseError};

    // Handwritten from the PR130 W1/W2 field order and byte widths, not from
    // a product encoder. Ciphertext, commitments and tag are public dummy bytes.
    // This is a well-framed untrusted fixture, not an authenticated pre-image.
    #[rustfmt::skip]
    static PUBLIC_FRAME: [u8; 182] = [
        // 0..16: fifteen ASCII magic bytes and one NUL.
        0x43, 0x50, 0x41, 0x2d, 0x57, 0x49, 0x4e, 0x2d,
        0x50, 0x52, 0x45, 0x49, 0x4d, 0x47, 0x34, 0x00,
        // 16..28: version 4, Windows 1, AES-256-GCM 1, all little-endian.
        4, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0,
        // 28..44: nonzero public transaction ID.
        1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
        // 44..66: LP("windows-dpapi-user"), eighteen ASCII bytes.
        18, 0, 0, 0,
        0x77, 0x69, 0x6e, 0x64, 0x6f, 0x77, 0x73, 0x2d, 0x64,
        0x70, 0x61, 0x70, 0x69, 0x2d, 0x75, 0x73, 0x65, 0x72,
        // 66..70: provider version 1.
        1, 0, 0, 0,
        // 70..102: opaque, unauthenticated K bytes.
        0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28,
        0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30,
        0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38,
        0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40,
        // 102..110: ordinal 0, count 1, little-endian.
        0, 0, 0, 0, 1, 0, 0, 0,
        // 110..122: eight-byte prefix and big-endian ordinal 0.
        0x91, 0x92, 0x93, 0x94, 0x95, 0x96, 0x97, 0x98, 0, 0, 0, 0,
        // 122..154: opaque, unauthenticated SHA-256(B) bytes.
        0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58,
        0x59, 0x5a, 0x5b, 0x5c, 0x5d, 0x5e, 0x5f, 0x60,
        0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68,
        0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x6f, 0x70,
        // 154..158: payload/ciphertext length 8, excluding the tag.
        8, 0, 0, 0,
        // 158..166: dummy ciphertext.
        0x81, 0x82, 0x83, 0x84, 0x85, 0x86, 0x87, 0x88,
        // 166..182: dummy tag; no authentication is asserted.
        0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8,
        0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0,
    ];

    fn expect_error(bytes: &[u8], expected: FrameParseError) {
        match parse_untrusted_frame(bytes) {
            Err(actual) => assert_eq!(actual, expected),
            Ok(_) => panic!("expected fixed frame rejection"),
        }
    }

    // Test-only assembly from the independent literal header and tag. No
    // production serialization or cryptographic routine is used.
    fn public_payload_frame(payload: &[u8], length_le: [u8; 4]) -> Vec<u8> {
        let mut bytes = PUBLIC_FRAME[..158].to_vec();
        bytes[154..158].copy_from_slice(&length_le);
        bytes.extend_from_slice(payload);
        bytes.extend_from_slice(&PUBLIC_FRAME[166..182]);
        bytes
    }

    #[test]
    fn handwritten_public_frame_yields_untrusted_borrowed_views() {
        let frame = parse_untrusted_frame(&PUBLIC_FRAME).expect("public untrusted frame");
        assert_eq!(frame.header_bytes, &PUBLIC_FRAME[..158]);
        assert_eq!(frame.ciphertext, &PUBLIC_FRAME[158..166]);
        assert_eq!(frame.tag.as_slice(), &PUBLIC_FRAME[166..182]);
        assert_eq!(frame.header.provider_kind, b"windows-dpapi-user");
        assert_eq!(frame.header.provider_version, 1);
        assert_eq!(
            frame.header.transaction_id.as_bytes(),
            &[1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16]
        );
        assert_eq!(frame.header.ordinal, 0);
        assert_eq!(frame.header.count, 1);
        assert_eq!(frame.header.payload_length, 8);
        assert_eq!(
            frame.header.key_reference_commitment,
            &PUBLIC_FRAME[70..102]
        );
        assert_eq!(frame.header.binding_commitment, &PUBLIC_FRAME[122..154]);
        assert_eq!(frame.header.nonce, &PUBLIC_FRAME[110..122]);
        assert_eq!(frame.header_bytes.as_ptr(), PUBLIC_FRAME.as_ptr());
        assert_eq!(frame.ciphertext.as_ptr(), PUBLIC_FRAME[158..].as_ptr());
        assert_eq!(frame.tag.as_ptr(), PUBLIC_FRAME[166..].as_ptr());
    }

    #[test]
    fn rejects_every_proper_prefix_of_the_handwritten_frame() {
        for end in 0..PUBLIC_FRAME.len() {
            expect_error(&PUBLIC_FRAME[..end], FrameParseError::Truncated);
        }
    }

    #[test]
    fn rejects_unknown_magic_version_platform_and_algorithm() {
        for offset in [0usize, 15] {
            let mut bytes = PUBLIC_FRAME;
            bytes[offset] ^= 1;
            expect_error(&bytes, FrameParseError::BadMagic);
        }
        for (offset, value, expected) in [
            (16usize, [1, 0, 0, 0], FrameParseError::UnsupportedVersion),
            (16, [2, 0, 0, 0], FrameParseError::UnsupportedVersion),
            (16, [3, 0, 0, 0], FrameParseError::UnsupportedVersion),
            (16, [0xff; 4], FrameParseError::UnsupportedVersion),
            (20, [0, 0, 0, 0], FrameParseError::UnsupportedPlatform),
            (20, [2, 0, 0, 0], FrameParseError::UnsupportedPlatform),
            (24, [0, 0, 0, 0], FrameParseError::UnsupportedAlgorithm),
            (24, [2, 0, 0, 0], FrameParseError::UnsupportedAlgorithm),
        ] {
            let mut bytes = PUBLIC_FRAME;
            bytes[offset..offset + 4].copy_from_slice(&value);
            expect_error(&bytes, expected);
        }
    }

    #[test]
    fn rejects_noncanonical_provider_kind_lengths_bytes_and_version() {
        for length in [[0, 0, 0, 0], [17, 0, 0, 0], [65, 0, 0, 0], [0xff; 4]] {
            let mut bytes = PUBLIC_FRAME;
            bytes[44..48].copy_from_slice(&length);
            expect_error(&bytes, FrameParseError::InvalidProvider);
        }
        for value in [b'W', b'/', 0, 0xff] {
            let mut bytes = PUBLIC_FRAME;
            bytes[48] = value;
            expect_error(&bytes, FrameParseError::InvalidProvider);
        }
        for version in [[0, 0, 0, 0], [2, 0, 0, 0], [0xff; 4]] {
            let mut bytes = PUBLIC_FRAME;
            bytes[66..70].copy_from_slice(&version);
            expect_error(&bytes, FrameParseError::InvalidProvider);
        }
    }

    #[test]
    fn rejects_zero_transaction_invalid_count_and_ordinal() {
        let mut bytes = PUBLIC_FRAME;
        bytes[28..44].fill(0);
        expect_error(&bytes, FrameParseError::InvalidTransaction);
        for count in [[0, 0, 0, 0], [17, 0, 0, 0], [0xff; 4]] {
            let mut bytes = PUBLIC_FRAME;
            bytes[106..110].copy_from_slice(&count);
            expect_error(&bytes, FrameParseError::InvalidCount);
        }
        let mut bytes = PUBLIC_FRAME;
        bytes[102..106].copy_from_slice(&[1, 0, 0, 0]);
        expect_error(&bytes, FrameParseError::InvalidOrdinal);
    }

    #[test]
    fn nonce_counter_is_big_endian_and_must_match_ordinal() {
        let mut bytes = PUBLIC_FRAME;
        bytes[102..106].copy_from_slice(&[1, 0, 0, 0]);
        bytes[106..110].copy_from_slice(&[2, 0, 0, 0]);
        bytes[118..122].copy_from_slice(&[0, 0, 0, 1]);
        let frame = parse_untrusted_frame(&bytes).expect("big-endian public counter");
        assert_eq!(frame.header.ordinal, 1);
        bytes[118..122].copy_from_slice(&[1, 0, 0, 0]);
        expect_error(&bytes, FrameParseError::NonceOrdinalMismatch);
    }

    #[test]
    fn accepts_highest_count_and_ordinal_without_claiming_freshness() {
        let mut bytes = PUBLIC_FRAME;
        bytes[102..106].copy_from_slice(&[15, 0, 0, 0]);
        bytes[106..110].copy_from_slice(&[16, 0, 0, 0]);
        bytes[118..122].copy_from_slice(&[0, 0, 0, 15]);
        let frame = parse_untrusted_frame(&bytes).expect("highest public ordinal");
        assert_eq!(frame.header.count, 16);
        assert_eq!(frame.header.ordinal, 15);
    }

    #[test]
    fn payload_bounds_are_checked_before_body_access() {
        for length in [
            [0, 0, 0, 0],
            [3, 0, 0, 0],
            [0x05, 0x00, 0x42, 0x00],
            [0xff; 4],
        ] {
            let mut bytes = PUBLIC_FRAME;
            bytes[154..158].copy_from_slice(&length);
            expect_error(&bytes, FrameParseError::PayloadLengthOutOfBounds);
        }
    }

    #[test]
    fn accepts_minimum_and_maximum_bounded_payload_frames() {
        let minimum = public_payload_frame(&[0x11, 0x22, 0x33, 0x44], [4, 0, 0, 0]);
        let frame = parse_untrusted_frame(&minimum).expect("necessary LP prefix bound");
        assert_eq!(frame.ciphertext.len(), 4);

        let payload = vec![0x5a; 4_325_380];
        let maximum = public_payload_frame(&payload, [0x04, 0x00, 0x42, 0x00]);
        let frame = parse_untrusted_frame(&maximum).expect("maximum public frame");
        assert_eq!(maximum.len(), 4_325_554);
        assert_eq!(frame.ciphertext.len(), 4_325_380);
        assert_eq!(frame.tag.as_slice(), &PUBLIC_FRAME[166..182]);
    }

    #[test]
    fn rejects_truncated_tag_and_trailing_data() {
        expect_error(&PUBLIC_FRAME[..181], FrameParseError::Truncated);
        let mut extra = PUBLIC_FRAME.to_vec();
        extra.push(0);
        expect_error(&extra, FrameParseError::TrailingBytes);

        let mut longer = PUBLIC_FRAME;
        longer[154..158].copy_from_slice(&[9, 0, 0, 0]);
        expect_error(&longer, FrameParseError::Truncated);
        let mut shorter = PUBLIC_FRAME;
        shorter[154..158].copy_from_slice(&[7, 0, 0, 0]);
        expect_error(&shorter, FrameParseError::TrailingBytes);
    }

    #[test]
    fn rejects_global_artifact_oversize_before_inspecting_header() {
        let bytes = vec![0; 4_325_601];
        expect_error(&bytes, FrameParseError::FrameTooLarge);
    }

    #[test]
    fn commitment_ciphertext_and_tag_tampering_remain_untrusted() {
        for offset in [70usize, 122, 158, 181] {
            let mut bytes = PUBLIC_FRAME;
            bytes[offset] ^= 1;
            let frame = parse_untrusted_frame(&bytes).expect("framing cannot authenticate");
            assert_eq!(frame.header_bytes, &bytes[..158]);
            assert_eq!(frame.ciphertext, &bytes[158..166]);
            assert_eq!(frame.tag.as_slice(), &bytes[166..182]);
        }
    }

    #[test]
    fn repeated_frames_are_only_reparsed_not_consumed() {
        let first = parse_untrusted_frame(&PUBLIC_FRAME).expect("first untrusted view");
        let second = parse_untrusted_frame(&PUBLIC_FRAME).expect("repeated untrusted view");
        assert_eq!(first.header_bytes.as_ptr(), second.header_bytes.as_ptr());
        assert_eq!(first.ciphertext.as_ptr(), second.ciphertext.as_ptr());
        assert_eq!(first.tag.as_ptr(), second.tag.as_ptr());
    }
}
