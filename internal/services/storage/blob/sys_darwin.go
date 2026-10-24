package blob

import "syscall"

// statfsName returns the name of the file system type of path (statfs f_fstypename: "apfs",
// "nfs", "smbfs", …).
func statfsName(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	b := make([]byte, 0, len(st.Fstypename))
	for _, c := range st.Fstypename {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b), nil
}
