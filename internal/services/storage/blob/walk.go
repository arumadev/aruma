package blob

import "time"

// Walk implements Store.
func (d *Disk) Walk(prefix string, fn func(key string, modTime time.Time) error) error {
	panic("S2-A: Disk.Walk")
}

// DeletePrefix implements Store.
func (d *Disk) DeletePrefix(prefix string) error { panic("S2-A: Disk.DeletePrefix") }

// FreeBytes implements Store (syscall.Statfs; an explicit "not supported" where there is none).
func (d *Disk) FreeBytes() (int64, error) { panic("S2-A: Disk.FreeBytes") }
