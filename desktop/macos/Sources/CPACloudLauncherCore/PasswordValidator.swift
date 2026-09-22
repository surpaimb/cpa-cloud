import Foundation

public enum PasswordValidationError: LocalizedError, Equatable {
    case mismatch
    case invalidUTF8Length(actual: Int)

    public var errorDescription: String? {
        switch self {
        case .mismatch:
            return "The passwords do not match."
        case .invalidUTF8Length:
            return "Use a password between 12 and 72 UTF-8 bytes."
        }
    }
}

public enum PasswordValidator {
    public static let validUTF8ByteRange = 12...72

    public static func validate(_ password: String, confirmation: String) throws {
        guard password == confirmation else {
            throw PasswordValidationError.mismatch
        }
        let byteCount = password.lengthOfBytes(using: .utf8)
        guard validUTF8ByteRange.contains(byteCount) else {
            throw PasswordValidationError.invalidUTF8Length(actual: byteCount)
        }
    }
}
