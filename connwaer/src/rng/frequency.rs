/// NIST SP 800-22 Test 1: Frequency (Monobit).
///
/// Determines whether the number of ones and zeros in the sequence are
/// approximately equal, as expected for a truly random sequence.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len() as f64;
    let sum: f64 = bits.iter().map(|&b| 2.0 * b as f64 - 1.0).sum();
    let s_obs = sum.abs() / n.sqrt();
    super::erfc(s_obs / std::f64::consts::SQRT_2)
}
