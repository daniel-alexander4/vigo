/// NIST SP 800-22 Test 11: Serial.
///
/// Counts overlapping m-bit patterns and determines whether the frequency
/// of all 2^m patterns is approximately uniform, as expected for a random
/// sequence. Returns two p-values (delta1 and delta2).

pub fn test(bits: &[u8]) -> (f64, f64) {
    let n = bits.len();
    let m = ((n as f64).log2() as usize).saturating_sub(2).max(2).min(16);

    let psi_m = psi_sq(bits, m);
    let psi_m1 = psi_sq(bits, m.saturating_sub(1).max(1));
    let psi_m2 = if m >= 2 { psi_sq(bits, m - 2) } else { 0.0 };

    let del1 = psi_m - psi_m1;
    let del2 = psi_m - 2.0 * psi_m1 + psi_m2;

    let p1 = super::igamc((1u64 << (m - 1)) as f64 / 2.0, del1 / 2.0);
    let p2 = super::igamc((1u64 << (m - 2)) as f64 / 2.0, del2 / 2.0);

    (p1.max(0.0).min(1.0), p2.max(0.0).min(1.0))
}

fn psi_sq(bits: &[u8], m: usize) -> f64 {
    let n = bits.len();
    let num_patterns = 1usize << m;
    let mut counts = vec![0u64; num_patterns];

    // Wrap-around: append first m-1 bits.
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
    let sum: f64 = counts.iter().map(|&c| (c as f64).powi(2)).sum();
    (num_patterns as f64 / nf) * sum - nf
}
