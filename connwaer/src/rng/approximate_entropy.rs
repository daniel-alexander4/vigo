//! NIST SP 800-22 Test 12: Approximate Entropy.
//!
//! Similar to the serial test but uses an entropy-based statistic. Compares
//! the frequency of overlapping patterns of length m and m+1 to detect
//! regularity (lack of randomness) in the sequence.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len();
    let m = ((n as f64).log2() as usize).saturating_sub(5).clamp(2, 10);

    let phi_m = phi(bits, m);
    let phi_m1 = phi(bits, m + 1);

    let ap_en = phi_m - phi_m1;
    let chi_sq = 2.0 * n as f64 * ((2.0f64).ln() - ap_en);

    super::igamc((1u64 << (m - 1)) as f64, chi_sq / 2.0)
}

fn phi(bits: &[u8], m: usize) -> f64 {
    let n = bits.len();
    let num_patterns = 1usize << m;
    let mut counts = vec![0u64; num_patterns];

    let extended: Vec<u8> = bits
        .iter()
        .chain(bits.iter().take(m - 1))
        .copied()
        .collect();

    for i in 0..n {
        let mut pattern = 0usize;
        for j in 0..m {
            pattern = (pattern << 1) | extended[i + j] as usize;
        }
        counts[pattern] += 1;
    }

    let nf = n as f64;
    counts
        .iter()
        .filter(|&&c| c > 0)
        .map(|&c| {
            let p = c as f64 / nf;
            p * p.ln()
        })
        .sum()
}
