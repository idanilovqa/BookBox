//go:build !windows

package main

func defaultWorkerConfigPath() string { return "/etc/bookbox/config.yml" }
