//go:build !windows

package main

func installTrayAutostart() error { return runGatewayCLI("service", "install") }

func showTrayError(error) {}
