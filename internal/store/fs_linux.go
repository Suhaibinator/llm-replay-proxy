package store

import "syscall"

// nfsSuperMagic is NFS_SUPER_MAGIC, statfs's f_type for an NFS mount.
const nfsSuperMagic = 0x6969

// onNFS reports whether dir is on an NFS mount.
func onNFS(dir string) bool {
	var st syscall.Statfs_t
	return syscall.Statfs(dir, &st) == nil && st.Type == nfsSuperMagic
}
