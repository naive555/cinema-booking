// Package lock implements distributed seat locking on a single Redis node.
// TODO: implement Acquire (SET NX EX with ownerToken UUID) and Release (atomic Lua
// script that checks ownerToken before DEL to avoid releasing a re-acquired lock).
package lock
