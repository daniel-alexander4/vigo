//! NIST SP 800-22 Test 8: Overlapping Template Matching.
//!
//! Similar to non-overlapping template matching, but allows overlapping
//! occurrences. Uses a template of 9 consecutive ones. The distribution
//! of match counts per block is compared to the Poisson expectation.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len();
    let m = 9;
    let template: Vec<u8> = vec![1; m];
    let block_size = 1032;
    let num_blocks = n / block_size;
    if num_blocks < 5 {
        return 1.0;
    }

    let lambda = (block_size - m + 1) as f64 / (1u64 << m) as f64;
    let eta = lambda / 2.0;

    // Compute expected probabilities using Poisson.
    let mut pi = [0.0f64; 6];
    pi[0] = (-eta).exp();
    for k in 1..5 {
        pi[k] = pi[k - 1] * eta / k as f64;
    }
    pi[5] = 1.0 - pi[0..5].iter().sum::<f64>();

    let mut freq = [0u64; 6];
    for i in 0..num_blocks {
        let block = &bits[i * block_size..std::cmp::min((i + 1) * block_size, n)];
        let count = count_overlapping(block, &template).min(5);
        freq[count] += 1;
    }

    let mut chi_sq = 0.0;
    for k in 0..6 {
        let expected = num_blocks as f64 * pi[k];
        if expected > 0.0 {
            chi_sq += (freq[k] as f64 - expected).powi(2) / expected;
        }
    }

    super::igamc(2.5, chi_sq / 2.0)
}

fn count_overlapping(block: &[u8], template: &[u8]) -> usize {
    let m = template.len();
    let mut count = 0;
    for i in 0..block.len().saturating_sub(m - 1) {
        if block[i..i + m] == *template {
            count += 1;
        }
    }
    count
}
