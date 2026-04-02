/// NIST SP 800-22 Test 3: Runs.
///
/// Determines whether the number of runs (uninterrupted sequences of
/// identical bits) is as expected for a random sequence. A pre-test checks
/// that the proportion of ones is close enough to 0.5.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len();
    let pi: f64 = bits.iter().map(|&b| b as f64).sum::<f64>() / n as f64;
    // Pre-test: if proportion deviates too far, sequence is non-random.
    if (pi - 0.5).abs() >= 2.0 / (n as f64).sqrt() {
        return 0.0;
    }
    let mut v_obs = 1u64;
    for i in 1..n {
        if bits[i] != bits[i - 1] {
            v_obs += 1;
        }
    }
    let nf = n as f64;
    super::erfc(
        (v_obs as f64 - 2.0 * nf * pi * (1.0 - pi)).abs()
            / (2.0 * (2.0 * nf).sqrt() * pi * (1.0 - pi)),
    )
}
