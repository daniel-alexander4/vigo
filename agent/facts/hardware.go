package facts

import "context"

// HardwareCollector gathers hardware facts: CPU count, memory in MB,
// and disk usage per mount point in GB.
type HardwareCollector struct{}
