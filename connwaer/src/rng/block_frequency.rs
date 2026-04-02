/// NIST SP 800-22 Test 2: Frequency within a Block.
///
/// Divides the sequence into non-overlapping blocks and determines whether
/// the proportion of ones in each block is approximately 0.5.

pub fn test(bits: &[u8], block_size: usize) -> f64 {
    let n = bits.len();
    let num_blocks = n / block_size;
    if num_blocks == 0 {
        return 0.0;
    }
    let mut chi_sq = 0.0;
    for i in 0..num_blocks {
        let block = &bits[i * block_size..(i + 1) * block_size];
        let pi: f64 = block.iter().map(|&b| b as f64).sum::<f64>() / block_size as f64;
        chi_sq += (pi - 0.5) * (pi - 0.5);
    }
    chi_sq *= 4.0 * block_size as f64;
    super::igamc(num_blocks as f64 / 2.0, chi_sq / 2.0)
}
