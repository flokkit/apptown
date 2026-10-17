//go:build !darwin

package gittest

func copyTree(src, dst string) error { return plainCopy(src, dst) }
