use serde::{Deserialize, Deserializer};

#[derive(Clone)]
pub struct SecretString(zeroize::Zeroizing<String>);

impl SecretString {
    pub fn new(value: impl Into<String>) -> Self {
        Self(zeroize::Zeroizing::new(value.into()))
    }

    pub fn expose_secret(&self) -> &str {
        self.0.as_str()
    }
}

impl std::fmt::Debug for SecretString {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("SecretString([REDACTED])")
    }
}

impl<'de> Deserialize<'de> for SecretString {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        String::deserialize(deserializer).map(Self::new)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn debug_is_always_redacted() {
        let secret = SecretString::new("sentinel-employee-key");
        let rendered = format!("{secret:?}");
        assert_eq!(rendered, "SecretString([REDACTED])");
        assert!(!rendered.contains("sentinel-employee-key"));
    }
}
