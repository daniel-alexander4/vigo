// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

//! W3C traceparent generation and parsing for cross-process log correlation
//! (ADR-028). Format: `00-<trace_id_32hex>-<span_id_16hex>-<flags_2hex>`,
//! 55 chars. The 32-hex trace_id portion is the field surfaced to
//! `tracing` spans and the server-side `Event.Details["trace_id"]`.

use rand::RngCore;

const TRACEPARENT_LEN: usize = 55;

/// Mint a fresh W3C traceparent. Returns `(traceparent, trace_id)`.
///
/// The sampled flag (`01`) is set so the deferred OTel follow-on records
/// these spans without further config. Span_id is zero — trace-id-only
/// mode has no parent span.
pub fn generate() -> (String, String) {
    let mut tid = [0u8; 16];
    rand::thread_rng().fill_bytes(&mut tid);
    let trace_id = hex::encode(tid);
    let traceparent = format!("00-{trace_id}-0000000000000000-01");
    (traceparent, trace_id)
}

/// Extract the 32-hex trace_id portion of a well-formed traceparent. Returns
/// `None` if the input is not a valid W3C traceparent string.
pub fn trace_id_of(traceparent: &str) -> Option<&str> {
    if traceparent.len() != TRACEPARENT_LEN {
        return None;
    }
    let bytes = traceparent.as_bytes();
    if bytes[2] != b'-' || bytes[35] != b'-' || bytes[52] != b'-' {
        return None;
    }
    let tid = &traceparent[3..35];
    if !tid.bytes().all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase()) {
        return None;
    }
    Some(tid)
}

/// Convenience: if `traceparent` is empty or malformed, generate a fresh
/// one. Returns `(traceparent, trace_id)` ready to use.
pub fn ensure(traceparent: &str) -> (String, String) {
    if let Some(tid) = trace_id_of(traceparent) {
        return (traceparent.to_string(), tid.to_string());
    }
    generate()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generate_is_well_formed() {
        let (tp, tid) = generate();
        assert_eq!(tp.len(), TRACEPARENT_LEN);
        assert_eq!(tid.len(), 32);
        assert_eq!(trace_id_of(&tp), Some(tid.as_str()));
    }

    #[test]
    fn generate_is_unique() {
        let (_, a) = generate();
        let (_, b) = generate();
        assert_ne!(a, b);
    }

    #[test]
    fn trace_id_of_rejects_malformed() {
        assert_eq!(trace_id_of(""), None);
        assert_eq!(trace_id_of("00-deadbeef-0000-01"), None);
        assert_eq!(trace_id_of(&"x".repeat(55)), None);
        // Upper-case hex is non-canonical per W3C.
        assert_eq!(
            trace_id_of("00-DEADBEEFDEADBEEFDEADBEEFDEADBEEF-0000000000000000-01"),
            None
        );
    }

    #[test]
    fn ensure_passes_through_valid() {
        let (tp, _) = generate();
        let (out, _) = ensure(&tp);
        assert_eq!(out, tp);
    }

    #[test]
    fn ensure_replaces_invalid() {
        let (out, tid) = ensure("garbage");
        assert_eq!(out.len(), TRACEPARENT_LEN);
        assert_eq!(tid.len(), 32);
    }
}
