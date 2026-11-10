//go:build !darwin

package main

// Only the Mac has the caveira menu; see appmenu_darwin.go.
func hideOthers() {}
func showAll()    {}
