package config

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/flowcontrol"
)

type FlowControlConfig = flowcontrol.Config

func (c *Config) ValidateFlowControl() error {
	if c.Home.Enabled && c.FlowControl.Enabled {
		return fmt.Errorf("flow-control: local flow control cannot be enabled in Home mode")
	}
	return c.FlowControl.Validate()
}
