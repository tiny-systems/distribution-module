package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/tiny-systems/module/cli"

	// Import components to register them
	_ "github.com/tiny-systems/distribution-module/components/registrycatalog"
	_ "github.com/tiny-systems/distribution-module/components/registrycopy"
)

// RootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "server",
	Short: "tiny-system's distribution module",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func main() {
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	viper.AutomaticEnv()
	if viper.GetBool("debug") {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cli.RegisterCommands(rootCmd)
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Printf("command execute error: %v\n", err)
	}
}
