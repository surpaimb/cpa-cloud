use sha2::{Digest, Sha256};

pub fn digest(contents: Option<&[u8]>) -> Option<String> {
    contents.map(|bytes| hex::encode(Sha256::digest(bytes)))
}
