//! NIST SP 800-22 Test 7: Non-overlapping Template Matching.
//!
//! Searches for occurrences of a non-periodic template pattern ("000000001")
//! across blocks of the sequence. When a match is found, the search skips
//! past it (non-overlapping). The distribution of match counts is tested
//! against the expected chi-squared distribution.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len();
    let template: &[u8] = &[0, 0, 0, 0, 0, 0, 0, 0, 1];
    let m = template.len();
    let block_size = n / 8; // N blocks
    let num_blocks = 8;
    if block_size < m {
        return 1.0;
    }

    let mu = (block_size - m + 1) as f64 / (1u64 << m) as f64;
    let sigma_sq = block_size as f64
        * (1.0 / (1u64 << m) as f64 - (2.0 * m as f64 - 1.0) / (1u64 << (2 * m)) as f64);
    if sigma_sq <= 0.0 {
        return 1.0;
    }

    let mut chi_sq = 0.0;
    for i in 0..num_blocks {
        let block = &bits[i * block_size..std::cmp::min((i + 1) * block_size, n)];
        let count = count_non_overlapping(block, template);
        chi_sq += (count as f64 - mu).powi(2) / sigma_sq;
    }

    super::igamc(num_blocks as f64 / 2.0, chi_sq / 2.0)
}

fn count_non_overlapping(block: &[u8], template: &[u8]) -> usize {
    let m = template.len();
    let mut count = 0;
    let mut i = 0;
    while i + m <= block.len() {
        if &block[i..i + m] == template {
            count += 1;
            i += m; // non-overlapping: skip past match
        } else {
            i += 1;
        }
    }
    count
}
