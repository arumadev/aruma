package blob

import (
	"fmt"
	"syscall"
)

// The f_type of the network file systems, from <linux/magic.h> (CIFS_SUPER_MAGIC is fs/smb's
// CIFS_MAGIC_NUMBER).
var linuxFSNames = map[uint32]string{
	0x6969:     "nfs",  // NFS_SUPER_MAGIC
	0xFF534D42: "cifs", // CIFS_SUPER_MAGIC
	0xFE534D42: "smb2", // SMB2_SUPER_MAGIC
	0x517B:     "smb",  // SMB_SUPER_MAGIC
}

// statfsName returns the name of the file system type of path: the network ones by name, any
// other as its statfs f_type in hex.
func statfsName(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	magic := uint32(st.Type) // f_type is a long; the magics fit in 32 bits
	if name, ok := linuxFSNames[magic]; ok {
		return name, nil
	}
	return fmt.Sprintf("0x%x", magic), nil
}
