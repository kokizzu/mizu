package cmd

import (
	"errors"
	"fmt"
	"github.com/up9inc/mizu/cli/mizu"
	"regexp"

	"github.com/spf13/cobra"
)

type MizuTapOptions struct {
	GuiPort        uint16
	Namespace      string
	KubeConfigPath string
	MizuImage      string
	MizuPodPort    uint16
}


var mizuTapOptions = &MizuTapOptions{}

var tapCmd = &cobra.Command{
	Use:   "tap [POD REGEX]",
	Short: "Record ingoing traffic of a kubernetes pod",
	Long: `Record the ingoing traffic of a kubernetes pod.
 Supported protocols are HTTP and gRPC.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return errors.New("POD REGEX argument is required")
		} else if len(args) > 1 {
			return errors.New("unexpected number of arguments")
		}

		regex, err := regexp.Compile(args[0])
		if err != nil {
			return errors.New(fmt.Sprintf("%s is not a valid regex %s", args[0], err))
		}

		RunMizuTap(regex, mizuTapOptions)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(tapCmd)

	tapCmd.Flags().Uint16VarP(&mizuTapOptions.GuiPort, "gui-port", "p", 8899, "Provide a custom port for the web interface webserver")
	tapCmd.Flags().StringVarP(&mizuTapOptions.Namespace, "namespace", "n", "", "Namespace selector")
	tapCmd.Flags().StringVarP(&mizuTapOptions.KubeConfigPath, "kube-config", "k", "", "Path to kube-config file")
	tapCmd.Flags().StringVarP(&mizuTapOptions.MizuImage, "mizu-image", "", fmt.Sprintf("gcr.io/up9-docker-hub/mizu/%s:latest", mizu.Branch), "Custom image for mizu collector")
	tapCmd.Flags().Uint16VarP(&mizuTapOptions.MizuPodPort, "mizu-port", "", 8899, "Port which mizu cli will attempt to forward from the mizu collector pod")
}