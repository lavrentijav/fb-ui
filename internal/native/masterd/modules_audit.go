//go:build !no_audit

package masterd

import (
	"github.com/mhsanaei/3x-ui/v3/internal/native/auditmod"
	"github.com/mhsanaei/3x-ui/v3/internal/native/module"
)

func init() { module.Register(auditmod.New()) }
