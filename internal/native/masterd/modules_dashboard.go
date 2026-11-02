//go:build !no_dashboard

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/dashboardmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func init() { module.Register(dashboardmod.New()) }
