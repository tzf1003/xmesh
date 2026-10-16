// Package privdir creates and verifies the private directories that hold node
// state (keys, tokens, audit logs).
//
// On Unix a private directory is mode 0700. On Windows os.MkdirAll ignores the
// mode and the directory takes the ACL of its parent, so a state directory
// created under C:\ is readable and writable by every local user. Ensure sets an
// explicit protected DACL instead: full control for the running user, SYSTEM and
// Administrators, with inheritance from the parent disabled.
package privdir
