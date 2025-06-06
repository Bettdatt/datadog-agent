// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package flare implements flare related subcommands
package flare

import (
	"bytes"
	"fmt"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"go.uber.org/fx"

	"github.com/DataDog/datadog-agent/cmd/security-agent/command"
	"github.com/DataDog/datadog-agent/comp/core"
	"github.com/DataDog/datadog-agent/comp/core/config"
	"github.com/DataDog/datadog-agent/comp/core/flare/helpers"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	"github.com/DataDog/datadog-agent/comp/core/secrets"
	"github.com/DataDog/datadog-agent/pkg/api/util"
	"github.com/DataDog/datadog-agent/pkg/flare/securityagent"
	"github.com/DataDog/datadog-agent/pkg/util/fxutil"
	"github.com/DataDog/datadog-agent/pkg/util/input"
)

type cliParams struct {
	*command.GlobalParams

	customerEmail string
	autoconfirm   bool
	caseID        string
}

// Commands returns the flare commands
func Commands(globalParams *command.GlobalParams) []*cobra.Command {
	cliParams := &cliParams{
		GlobalParams: globalParams,
	}

	flareCmd := &cobra.Command{
		Use:   "flare [caseID]",
		Short: "Collect a flare and send it to Datadog",
		Long:  ``,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				cliParams.caseID = args[0]
			}

			// The flare command should not log anything, all errors should be reported directly to the console without the log format
			return fxutil.OneShot(requestFlare,
				fx.Supply(cliParams),
				fx.Supply(core.BundleParams{
					ConfigParams: config.NewSecurityAgentParams(globalParams.ConfigFilePaths, config.WithIgnoreErrors(true)),
					SecretParams: secrets.NewEnabledParams(),
					LogParams:    log.ForOneShot(command.LoggerName, "off", true)}),
				core.Bundle(),
			)
		},
	}

	flareCmd.Flags().StringVarP(&cliParams.customerEmail, "email", "e", "", "Your email")
	flareCmd.Flags().BoolVarP(&cliParams.autoconfirm, "send", "s", false, "Automatically send flare (don't prompt for confirmation)")
	flareCmd.SetArgs([]string{"caseID"})

	return []*cobra.Command{flareCmd}
}

func requestFlare(_ log.Component, config config.Component, _ secrets.Component, params *cliParams) error {
	warnings := config.Warnings()
	if warnings != nil && warnings.Errors != nil {
		fmt.Fprintln(color.Error, color.YellowString("Config parsing warning: %v", warnings.Errors))
	}
	if params.customerEmail == "" {
		var err error
		params.customerEmail, err = input.AskForEmail()
		if err != nil {
			fmt.Println("Error reading email, please retry or contact support")
			return err
		}
	}

	fmt.Fprintln(color.Output, color.BlueString("Asking the Security Agent to build the flare archive."))
	var e error
	c := util.GetClient()
	urlstr := fmt.Sprintf("https://localhost:%v/agent/flare", config.GetInt("security_agent.cmd_port"))

	logFile := config.GetString("security_agent.log_file")

	// Set session token
	e = util.SetAuthToken(config)
	if e != nil {
		return e
	}

	r, e := util.DoPost(c, urlstr, "application/json", bytes.NewBuffer([]byte{}))
	sr := string(r)
	var filePath string
	if e != nil {
		if r != nil && sr != "" {
			fmt.Fprintf(color.Output, "The agent ran into an error while making the flare: %s\n", color.RedString(sr))
		} else {
			fmt.Fprintln(color.Output, color.RedString("The agent was unable to make a full flare: %s.", e.Error()))
		}
		fmt.Fprintln(color.Output, color.YellowString("Initiating flare locally, some logs will be missing."))
		filePath, e = securityagent.CreateSecurityAgentArchive(true, logFile, nil)
		if e != nil {
			fmt.Printf("The flare zipfile failed to be created: %s\n", e)
			return e
		}
	} else {
		filePath = sr
	}

	fmt.Fprintf(color.Output, "%s is going to be uploaded to Datadog\n", color.YellowString(filePath))
	if !params.autoconfirm {
		confirmation := input.AskForConfirmation("Are you sure you want to upload a flare? [y/N]")
		if !confirmation {
			fmt.Fprintf(color.Output, "Aborting. (You can still use %s)\n", color.YellowString(filePath))
			return nil
		}
	}

	response, e := helpers.SendFlare(config, filePath, params.caseID, params.customerEmail, helpers.NewLocalFlareSource())
	fmt.Println(response)
	if e != nil {
		return e
	}
	return nil
}
