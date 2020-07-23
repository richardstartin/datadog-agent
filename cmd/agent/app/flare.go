// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-2020 Datadog, Inc.

package app

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/DataDog/datadog-agent/cmd/agent/common"
	"github.com/DataDog/datadog-agent/pkg/api/util"
	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/flare"
	"github.com/DataDog/datadog-agent/pkg/util/input"
)

var (
	cpuProfURL = fmt.Sprintf("http://127.0.0.1:%s/debug/pprof/profile?seconds=%d",
		config.Datadog.GetString("expvar_port"), profiling)
	heapProfURL = fmt.Sprintf("http://127.0.0.1:%s/debug/pprof/heap?debug=2",
		config.Datadog.GetString("expvar_port"))

	customerEmail string
	autoconfirm   bool
	forceLocal    bool
	profiling     int
)

func init() {
	AgentCmd.AddCommand(flareCmd)

	flareCmd.Flags().StringVarP(&customerEmail, "email", "e", "", "Your email")
	flareCmd.Flags().BoolVarP(&autoconfirm, "send", "s", false, "Automatically send flare (don't prompt for confirmation)")
	flareCmd.Flags().BoolVarP(&forceLocal, "local", "l", false, "Force the creation of the flare by the command line instead of the agent process (useful when running in a containerized env)")
	flareCmd.Flags().IntVarP(&profiling, "profile", "p", 0, "Add performance profiling data to the flare. Will collect the CPU profile for the configured amount of seconds, with a minimum of 30s")
	flareCmd.SetArgs([]string{"caseID"})
}

var flareCmd = &cobra.Command{
	Use:   "flare [caseID]",
	Short: "Collect a flare and send it to Datadog",
	Long:  ``,
	RunE: func(cmd *cobra.Command, args []string) error {

		if flagNoColor {
			color.NoColor = true
		}

		err := common.SetupConfig(confFilePath)
		if err != nil {
			return fmt.Errorf("unable to set up global agent configuration: %v", err)
		}

		// The flare command should not log anything, all errors should be reported directly to the console without the log format
		err = config.SetupLogger(loggerName, "off", "", "", false, true, false)
		if err != nil {
			fmt.Printf("Cannot setup logger, exiting: %v\n", err)
			return err
		}

		caseID := ""
		if len(args) > 0 {
			caseID = args[0]
		}

		if customerEmail == "" {
			var err error
			customerEmail, err = input.AskForEmail()
			if err != nil {
				fmt.Println("Error reading email, please retry or contact support")
				return err
			}
		}

		return makeFlare(caseID)
	},
}

func makeFlare(caseID string) error {
	logFile := config.Datadog.GetString("log_file")
	if logFile == "" {
		logFile = common.DefaultLogFile
	}

	profileDir, err := flare.CreateTempDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(profileDir)

	if profiling >= 30 {
		fmt.Fprintln(color.Output, color.BlueString("Creating a %d second performance profile.", profiling))
		if err := writePerformanceProfile(profileDir); err != nil {
			fmt.Fprintln(color.Output, color.RedString(fmt.Sprintf("Could not collect performance profile: %s", err)))
			return err
		}
	} else {
		profileDir = ""
	}

	var filePath string
	if forceLocal {
		filePath, err = createArchive(logFile, profileDir)
	} else {
		filePath, err = requestArchive(logFile, profileDir)
	}

	if err != nil {
		return err
	}

	if _, err := os.Stat(filePath); err != nil {
		fmt.Fprintln(color.Output, color.RedString(fmt.Sprintf("The flare zipfile \"%s\" does not exist.", filePath)))
		fmt.Fprintln(color.Output, color.RedString("If the agent running in a different container try the '--local' option to generate the flare locally"))
		return err
	}

	fmt.Fprintln(color.Output, fmt.Sprintf("%s is going to be uploaded to Datadog", color.YellowString(filePath)))
	if !autoconfirm {
		confirmation := input.AskForConfirmation("Are you sure you want to upload a flare? [y/N]")
		if !confirmation {
			fmt.Fprintln(color.Output, fmt.Sprintf("Aborting. (You can still use %s)", color.YellowString(filePath)))
			return nil
		}
	}

	response, e := flare.SendFlare(filePath, caseID, customerEmail)
	fmt.Println(response)
	if e != nil {
		return e
	}
	return nil
}

func requestArchive(logFile, profileDir string) (string, error) {
	fmt.Fprintln(color.Output, color.BlueString("Asking the agent to build the flare archive."))
	var e error
	c := util.GetClient(false) // FIX: get certificates right then make this true
	ipcAddress, err := config.GetIPCAddress()
	if err != nil {
		fmt.Fprintln(color.Output, color.RedString(fmt.Sprintf("Error getting IPC address for the agent: %s", err)))
		return createArchive(logFile, profileDir)
	}

	urlstr := fmt.Sprintf("https://%v:%v/agent/flare%v", ipcAddress, config.Datadog.GetInt("cmd_port"), profileDir)

	// Set session token
	e = util.SetAuthToken()
	if e != nil {
		fmt.Fprintln(color.Output, color.RedString(fmt.Sprintf("Error: %s", e)))
		return createArchive(logFile, profileDir)
	}

	fmt.Printf("POSTING TO %s\n", urlstr)
	r, e := util.DoPost(c, urlstr, "application/json", bytes.NewBuffer([]byte{}))
	if e != nil {
		if r != nil && string(r) != "" {
			fmt.Fprintln(color.Output, fmt.Sprintf("The agent ran into an error while making the flare: %s", color.RedString(string(r))))
		} else {
			fmt.Fprintln(color.Output, color.RedString("The agent was unable to make the flare. (is it running?)"))
		}
		return createArchive(logFile, profileDir)
	}
	return string(r), nil
}

func createArchive(logFile, profileDir string) (string, error) {
	fmt.Fprintln(color.Output, color.YellowString("Initiating flare locally."))
	filePath, e := flare.CreateArchive(true, common.GetDistPath(), common.PyChecksPath, logFile, profileDir)
	if e != nil {
		fmt.Printf("The flare zipfile failed to be created: %s\n", e)
		return "", e
	}
	return filePath, nil
}

func writePerformanceProfile(profileDir string) error {
	// Two heap profiles for diff
	err := writeHTTPCallContent(profileDir, flare.HeapProfileName, heapProfURL)
	if err != nil {
		return err
	}

	err = writeHTTPCallContent(profileDir, flare.CPUProfileName, cpuProfURL)
	if err != nil {
		return err
	}

	err = writeHTTPCallContent(profileDir, flare.HeapProfileName, heapProfURL)
	if err != nil {
		return err
	}

	return nil
}

func writeHTTPCallContent(profileDir, filename, url string) error {
	res, err := http.Get(url)
	if err != nil {
		return err
	}

	path := filepath.Join(profileDir, filename)
	err = flare.EnsureParentDirsExist(path)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, os.ModePerm)
	if err != nil {
		return err
	}

	_, err = io.Copy(f, res.Body)
	return err
}
