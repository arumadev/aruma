package blob

// CreateStaging implements Store (O_EXCL, at an upload's start only).
func (d *Disk) CreateStaging(key string) (Staging, error) { panic("S2-A: Disk.CreateStaging") }

// OpenStaging implements Store; it never creates the file.
func (d *Disk) OpenStaging(key string) (Staging, error) { panic("S2-A: Disk.OpenStaging") }
