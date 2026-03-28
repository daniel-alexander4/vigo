/// NIST SP 800-22 Test 9: Maurer's Universal Statistical Test.
///
/// Measures the compressibility of the sequence. A truly random sequence
/// cannot be significantly compressed. Uses L=7 bit blocks with Q=1280
/// initialization blocks.

pub fn test(bits: &[u8]) -> f64 {
    let n = bits.len();
    let l = 7usize;
    let q = 1280usize;
    let k = n / l - q;
    if k == 0 {
        return 1.0;
    }

    let expected_value = 6.1962507;
    let variance = 3.125;

    let mut table = vec![0u64; 1 << l];

    // Initialize table with Q blocks.
    for i in 0..q {
        let block_val = block_to_int(bits, i * l, l);
        table[block_val] = (i + 1) as u64;
    }

    // Test phase.
    let mut sum = 0.0f64;
    for i in q..q + k {
        let block_val = block_to_int(bits, i * l, l);
        let distance = (i + 1) as f64 - table[block_val] as f64;
        sum += distance.log2();
        table[block_val] = (i + 1) as u64;
    }

    let fn_val = sum / k as f64;
    let c = 0.7 - 0.8 / l as f64
        + (4.0 + 32.0 / l as f64) * (k as f64).powf(-3.0 / l as f64) / 15.0;
    let sigma = (c * variance / k as f64).sqrt();

    let z = (fn_val - expected_value) / sigma;
    super::erfc(z.abs() / std::f64::consts::SQRT_2)
}

fn block_to_int(bits: &[u8], start: usize, length: usize) -> usize {
    let mut val = 0usize;
    for i in 0..length {
        val = (val << 1) | bits[start + i] as usize;
    }
    val
}
