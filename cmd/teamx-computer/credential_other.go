//go:build !unix

package main

import "os"

func credentialOwnedByRoot(os.FileInfo) bool { return false }
