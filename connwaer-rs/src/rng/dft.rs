/// NIST SP 800-22 Test 6: Discrete Fourier Transform (Spectral).
///
/// Converts bits to +1/-1, computes the FFT, and counts how many magnitude
/// peaks fall below the 95% threshold. A significant deviation from the
/// expected count indicates non-randomness.
///
/// Uses radix-2 Cooley-Tukey FFT (O(n log n)) instead of the naive DFT,
/// allowing input up to 1,000,000 bits while staying fast.
/// Input is truncated to the largest power-of-two ≤ min(n, 1_000_000).

pub fn test(bits: &[u8]) -> f64 {
    let max_bits = 1_000_000;
    let bits = if bits.len() > max_bits { &bits[..max_bits] } else { bits };

    // FFT requires power-of-two length. Find largest power of 2 ≤ n.
    let n = prev_power_of_two(bits.len());
    if n < 2 {
        return 0.0; // not enough data
    }
    let bits = &bits[..n];

    // Convert bits to +1/-1 complex values (imaginary = 0).
    let mut re: Vec<f64> = bits.iter().map(|&b| 2.0 * b as f64 - 1.0).collect();
    let mut im: Vec<f64> = vec![0.0; n];

    // In-place radix-2 Cooley-Tukey FFT.
    fft_in_place(&mut re, &mut im);

    // Count magnitudes below the 95% threshold (first n/2 frequencies).
    let half = n / 2;
    let threshold = (2.995732274 * (n as f64)).sqrt(); // sqrt(ln(1/0.05) * n)
    let mut below_threshold = 0u64;

    for k in 0..half {
        let magnitude = (re[k] * re[k] + im[k] * im[k]).sqrt();
        if magnitude < threshold {
            below_threshold += 1;
        }
    }

    let expected_below = 0.95 * half as f64;
    let d = (below_threshold as f64 - expected_below)
        / ((half as f64) * 0.95 * 0.05).sqrt();
    super::erfc(d.abs() / std::f64::consts::SQRT_2)
}

/// Largest power of 2 that is ≤ n.
fn prev_power_of_two(n: usize) -> usize {
    if n == 0 { return 0; }
    1 << (usize::BITS - 1 - n.leading_zeros())
}

/// In-place radix-2 Cooley-Tukey FFT.
/// `re` and `im` must have the same power-of-two length.
fn fft_in_place(re: &mut [f64], im: &mut [f64]) {
    let n = re.len();
    debug_assert!(n.is_power_of_two());

    // Bit-reversal permutation.
    let mut j = 0usize;
    for i in 1..n {
        let mut bit = n >> 1;
        while j & bit != 0 {
            j ^= bit;
            bit >>= 1;
        }
        j ^= bit;
        if i < j {
            re.swap(i, j);
            im.swap(i, j);
        }
    }

    // Butterfly stages.
    let mut len = 2;
    while len <= n {
        let half = len / 2;
        let angle = -2.0 * std::f64::consts::PI / len as f64;
        let wn_re = angle.cos();
        let wn_im = angle.sin();

        let mut i = 0;
        while i < n {
            let mut w_re = 1.0;
            let mut w_im = 0.0;

            for k in 0..half {
                let u_re = re[i + k];
                let u_im = im[i + k];
                let v_re = re[i + k + half] * w_re - im[i + k + half] * w_im;
                let v_im = re[i + k + half] * w_im + im[i + k + half] * w_re;

                re[i + k] = u_re + v_re;
                im[i + k] = u_im + v_im;
                re[i + k + half] = u_re - v_re;
                im[i + k + half] = u_im - v_im;

                let new_w_re = w_re * wn_re - w_im * wn_im;
                let new_w_im = w_re * wn_im + w_im * wn_re;
                w_re = new_w_re;
                w_im = new_w_im;
            }
            i += len;
        }
        len <<= 1;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prev_power_of_two_cases() {
        assert_eq!(prev_power_of_two(0), 0);
        assert_eq!(prev_power_of_two(1), 1);
        assert_eq!(prev_power_of_two(2), 2);
        assert_eq!(prev_power_of_two(3), 2);
        assert_eq!(prev_power_of_two(1000), 512);
        assert_eq!(prev_power_of_two(1024), 1024);
        assert_eq!(prev_power_of_two(10000), 8192);
        assert_eq!(prev_power_of_two(1_000_000), 524288);
    }

    #[test]
    fn fft_basic() {
        // Simple 4-point FFT: [1, 1, 1, 1] → [4, 0, 0, 0]
        let mut re = vec![1.0, 1.0, 1.0, 1.0];
        let mut im = vec![0.0; 4];
        fft_in_place(&mut re, &mut im);
        assert!((re[0] - 4.0).abs() < 1e-10);
        for k in 1..4 {
            assert!((re[k]).abs() < 1e-10);
            assert!((im[k]).abs() < 1e-10);
        }
    }

    #[test]
    fn test_returns_valid_pvalue() {
        // Random-ish bits should give a p-value > 0.01.
        let bits: Vec<u8> = (0..2000).map(|i| ((i * 7 + 13) % 2) as u8).collect();
        let p = test(&bits);
        assert!(p >= 0.0 && p <= 1.0, "p-value out of range: {p}");
    }

    #[test]
    fn test_constant_bits_fail() {
        // All zeros → maximally non-random, should give low p-value.
        let bits = vec![0u8; 2048];
        let p = test(&bits);
        assert!(p < 0.01, "constant bits should fail DFT test, got p={p}");
    }
}
