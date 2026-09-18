package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	command := os.Args[1]
	var err error
	switch command {
	case "version", "--version":
		fmt.Println(Version)
	case "help", "--help", "-h":
		usage()
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ExitOnError)
		root := flags.String("root", "/", "filesystem root; a non-/ root disables hardware control")
		listen := flags.String("listen", ":8080", "HTTP listen address")
		flags.Parse(os.Args[2:])
		if flags.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "unexpected arguments")
			os.Exit(2)
		}
		var restart bool
		restart, err = serve(*root, *listen)
		if restart && err == nil {
			os.Exit(75)
		}
	case "screen":
		err = RunScreen()
	case "native-status":
		if len(os.Args) != 2 {
			err = fmt.Errorf("native-status accepts no arguments")
		} else {
			err = runNativeCameraStatus(os.Stdout)
		}
	case "network", "dhcp":
		var a *App
		a, err = appAt("/")
		if err == nil {
			err = a.RequireManaged()
			a.cancel()
		}
		if err == nil {
			a, err = NewApp("/")
		}
		if err == nil {
			defer a.cancel()
			if command == "network" {
				err = RunNetwork(a)
			} else {
				err = RunDHCP(a, os.Args[2:])
			}
		}
	case "install", "update", "uninstall", "import-config", "verify":
		err = RunInstaller(command, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Println(`Action Control — DJI Osmo Action 5 Pro
  action-control serve [--listen :8080] [--root /]
  action-control network
  action-control native-status
  action-control install --source PATH
  action-control update --source PATH
  action-control uninstall --reboot
  action-control import-config --file PATH
  action-control verify [--removed]
  action-control version
A non-/ filesystem root permits local file/UI verification but never hardware control.
Use install.sh/install.ps1 for ADB installation.`)
}
