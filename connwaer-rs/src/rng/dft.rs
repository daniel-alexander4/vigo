/// NIST SP 800-22 Test 6: Discrete Fourier Transform (Spectral).
///
/// Converts bits to +1/-1, computes the DFT, and counts how many magnitude
/// peaks fall below the 95% threshold. A significant deviation from the
/// expected count indicates non-randomness.
///
/// Input is truncated to 10000 bits max to keep runtime reasonable.

pub fn test(bits: &[u8]) -> f64 {
    let bits = if bits.len() > 10000 { &bits[..10000] } else { bits };
    let n = bits.len();
    let x: Vec<f64> = bits.iter().map(|&b| 2.0 * b as f64 - 1.0).collect();

    // Compute DFT magnitudes for first n/2 frequencies.
    let half = n / 2;
    let threshold = (2.995732274 * (n as f64)).sqrt(); // sqrt(ln(1/0.05) * n)
    let mut below_threshold = 0u64;

    for k in 0..half {
        let mut re = 0.0f64;
        let mut im = 0.0f64;
        let freq = 2.0 * std::f64::consts::PI * k as f64 / n as f64;
        for (j, &xj) in x.iter().enumerate() {
            re += xj * (freq * j as f64).cos();
            im -= xj * (freq * j as f64).sin();
        }
        let magnitude = (re * re + im * im).sqrt();
        if magnitude < threshold {
            below_threshold += 1;
        }
    }

    let expected_below = 0.95 * half as f64;
    let d = (below_threshold as f64 - expected_below)
        / ((half as f64) * 0.95 * 0.05).sqrt();
    super::erfc(d.abs() / std::f64::consts::SQRT_2)
}
