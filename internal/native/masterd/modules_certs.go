//go:build !no_certs

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/certsmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func init() { module.Register(certsmod.New()) }
