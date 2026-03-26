//go:build !linux

package main

import "os/exec"

type appRef struct{}

func (r *appRef) set(*exec.Cmd) {}

func startStatsServer(*appRef) {}
