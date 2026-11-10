// Package private writes files only their owner should read: the sign-in
// token, a key for a local model server.
//
// On a Mac or Linux that is mode 0600. On Windows a mode means nothing (it
// is only the read-only attribute), so the file's access list is cut down
// to its owner instead: no inherited entries, so no "Users" or "Everyone",
// before the secret is put in it.
package private
