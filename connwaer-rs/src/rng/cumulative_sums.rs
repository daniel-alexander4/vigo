/// NIST SP 800-22 Test 13: Cumulative Sums (Cusums).
///
/// Determines whether the cumulative sum of the adjusted (-1/+1) sequence
/// is too large or too small, indicating too many zeros or ones at the
/// start of the sequence. Tests both forward and reverse directions.

pub fn test(bits: &[u8]) -> (f64, f64) {
    let n = bits.len();
    let x: Vec<f64> = bits.iter().map(|&b| 2.0 * b as f64 - 1.0).collect();

    let p_fwd = cusum_p_value(&x, n);
    let x_rev: Vec<f64> = x.iter().rev().copied().collect();
    let p_rev = cusum_p_value(&x_rev, n);

    (p_fwd, p_rev)
}

fn cusum_p_value(x: &[f64], n: usize) -> f64 {
    let mut s = 0.0f64;
    let mut z = 0.0f64;
    for &xi in x {
        s += xi;
        z = z.max(s.abs());
    }

    let nf = n as f64;
    let mut sum = 0.0f64;
    let start = ((-nf / z + 1.0) / 4.0).floor() as i64;
    let end = ((nf / z - 1.0) / 4.0).floor() as i64;
    for k in start..=end {
        let kf = k as f64;
        sum += super::normal_cdf((4.0 * kf + 1.0) * z / nf.sqrt())
            - super::normal_cdf((4.0 * kf - 1.0) * z / nf.sqrt());
    }
    let mut sum2 = 0.0f64;
    let start2 = ((-nf / z - 3.0) / 4.0).floor() as i64;
    let end2 = ((nf / z - 1.0) / 4.0).floor() as i64;
    for k in start2..=end2 {
        let kf = k as f64;
        sum2 += super::normal_cdf((4.0 * kf + 3.0) * z / nf.sqrt())
            - super::normal_cdf((4.0 * kf + 1.0) * z / nf.sqrt());
    }

    1.0 - sum + sum2
}
