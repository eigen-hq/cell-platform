// cellctl is the admin CLI of a cell.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const usage = `usage: cellctl <command>

commands:
  status    state of the cell base services
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch os.Args[1] {
	case "status":
		c, err := kubeClient()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(status(ctx, c, os.Stdout))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func kubeClient() (client.Client, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	return client.New(cfg, client.Options{})
}
