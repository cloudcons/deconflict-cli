//go:build windows

package client

import "os"

// Windows has no flock, and the call that would replace it — LockFileEx — is
// not exported by the standard library's syscall package. Reaching it means
// taking a dependency on golang.org/x/sys, and this module deliberately has
// none.
//
// What remains is still right for the case that matters. Go opens an O_APPEND
// file with FILE_APPEND_DATA, and Windows serialises appends to such a handle,
// so two processes on one machine cannot interleave a line. What is lost is the
// case flock was belt-and-braces for: a log on a network share. Claims are
// advisory, so a lost race there produces two overlapping claims — which is a
// report, not a corruption, and is the same thing the registry exists to show.
func lockAppend(_ *os.File) (func(), error) { return func() {}, nil }
