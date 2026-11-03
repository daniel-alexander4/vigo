// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: Apache-2.0

//! Blob metadata types.

/// Standard chunk size: 1 MiB.
pub const CHUNK_SIZE: u64 = 1 << 20; // 1048576

/// Metadata for a blob being downloaded or served.
#[derive(Debug, Clone)]
pub struct BlobMeta {
    pub sha256: String,
    pub size: u64,
    pub chunk_count: u32,
    pub label: String,
}

impl BlobMeta {
    pub fn new(sha256: String, size: u64, label: String) -> Self {
        Self {
            sha256,
            chunk_count: chunks_for_size(size),
            size,
            label,
        }
    }
}

/// Compute the number of chunks for a given blob size.
pub fn chunks_for_size(size: u64) -> u32 {
    if size == 0 {
        return 0;
    }
    let n = size / CHUNK_SIZE;
    let rem = if !size.is_multiple_of(CHUNK_SIZE) { 1 } else { 0 };
    (n + rem) as u32
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_chunks_for_size() {
        assert_eq!(chunks_for_size(0), 0);
        assert_eq!(chunks_for_size(1), 1);
        assert_eq!(chunks_for_size(CHUNK_SIZE), 1);
        assert_eq!(chunks_for_size(CHUNK_SIZE + 1), 2);
        assert_eq!(chunks_for_size(CHUNK_SIZE * 10), 10);
        assert_eq!(chunks_for_size(CHUNK_SIZE * 10 + 500), 11);
    }
}
