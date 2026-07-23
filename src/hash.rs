use sha2::{Digest, Sha256};

const LOWER_HEX: &[u8; 16] = b"0123456789abcdef";

pub(crate) fn encode_lower(bytes: impl AsRef<[u8]>) -> String {
    let bytes = bytes.as_ref();
    let mut encoded = String::with_capacity(bytes.len() * 2);
    for &byte in bytes {
        encoded.push(LOWER_HEX[(byte >> 4) as usize] as char);
        encoded.push(LOWER_HEX[(byte & 0x0f) as usize] as char);
    }
    encoded
}

pub(crate) fn sha256_hex(data: impl AsRef<[u8]>) -> String {
    encode_lower(Sha256::digest(data))
}

pub(crate) fn sha256_hex_prefix(data: impl AsRef<[u8]>, length: usize) -> String {
    sha256_hex(data)[..length].to_owned()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sha256_hex_matches_known_vector() {
        assert_eq!(
            sha256_hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }

    #[test]
    fn sha256_hex_prefix_preserves_leading_hash_characters() {
        assert_eq!(sha256_hex_prefix(b"abc", 20), "ba7816bf8f01cfea4141");
    }

    #[test]
    fn encode_lower_preserves_leading_zeroes_and_uses_lowercase() {
        assert_eq!(encode_lower([0x00, 0x01, 0x0f, 0x10, 0xff]), "00010f10ff");
    }
}
