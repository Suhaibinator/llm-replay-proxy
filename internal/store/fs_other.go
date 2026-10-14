//go:build !linux

package store

func onNFS(string) bool { return false }
